// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package guard

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GSI-HPC/slurm-temperature-check/internal/hwmon"
)

// fakeSource stands in for the hwmon sensors.
type fakeSource struct {
	reading hwmon.MilliCelsius
	err     error
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Read() (hwmon.MilliCelsius, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.reading, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// fileState says what to do with the disable or override file before a case
// runs. "absent" leaves it missing.
type fileState struct {
	create     bool
	content    string
	unreadable bool
}

func (fs fileState) apply(t *testing.T, path string) {
	t.Helper()
	if !fs.create {
		return
	}
	if fs.unreadable && os.Geteuid() == 0 {
		// Mode bits do not stop root, and CI builds the RPM in a container
		// that runs as root. The case is still exercised by every
		// unprivileged run, which is how the service itself runs.
		t.Skip("cannot make a file unreadable as root")
	}
	if err := os.WriteFile(path, []byte(fs.content), 0o644); err != nil {
		t.Fatal(err)
	}
	if fs.unreadable {
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTruthTable is the specification of the guard, one row per situation the
// package has to handle. The expectation is the sequence of verdicts from
// consecutive passes, which pins both the immediate decision and how the
// retry budget is spent: a failed reading is tolerated read-retries times and
// trips on the pass after that.
func TestTruthTable(t *testing.T) {
	const limit = hwmon.MilliCelsius(85000)

	tests := []struct {
		desc     string
		disable  fileState
		override fileState
		sensors  *fakeSource
		want     []Verdict
	}{
		{
			desc:    "1: the disable file suspends checking, whatever the sensors say",
			disable: fileState{create: true},
			sensors: &fakeSource{reading: 99000},
			want:    []Verdict{Disabled, Disabled, Disabled},
		},
		{
			desc:     "2: the disable file outranks a tripping override as well",
			disable:  fileState{create: true},
			override: fileState{create: true, content: "120"},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Disabled, Disabled},
		},
		{
			desc:     "3: an unreadable override trips once the budget is spent",
			override: fileState{create: true, content: "40", unreadable: true},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Tolerated, Tolerated, Trip},
		},
		{
			desc:     "4: an override holding garbage trips once the budget is spent",
			override: fileState{create: true, content: "warm"},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Tolerated, Tolerated, Trip},
		},
		{
			desc:     "5: an empty override, the shape of a torn write, is tolerated first",
			override: fileState{create: true, content: ""},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Tolerated, Tolerated, Trip},
		},
		{
			desc:     "6: an override above the limit trips immediately",
			override: fileState{create: true, content: "86"},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Trip},
		},
		{
			desc:     "7: an override exactly at the limit keeps running",
			override: fileState{create: true, content: "85"},
			sensors:  &fakeSource{reading: 99000},
			want:     []Verdict{OK, OK, OK},
		},
		{
			desc:     "8: an override below the limit keeps running and hides the sensors",
			override: fileState{create: true, content: "25"},
			sensors:  &fakeSource{err: errors.New("sensor is on fire")},
			want:     []Verdict{OK, OK},
		},
		{
			desc:     "9: a fractional override is compared exactly",
			override: fileState{create: true, content: "85.001"},
			sensors:  &fakeSource{reading: 40000},
			want:     []Verdict{Trip},
		},
		{
			desc:    "10: an unreadable sensor trips once the budget is spent",
			sensors: &fakeSource{err: errors.New("input/output error")},
			want:    []Verdict{Tolerated, Tolerated, Trip},
		},
		{
			desc:    "11: a sensor above the limit trips immediately",
			sensors: &fakeSource{reading: 85001},
			want:    []Verdict{Trip},
		},
		{
			desc:    "12: a sensor exactly at the limit keeps running",
			sensors: &fakeSource{reading: 85000},
			want:    []Verdict{OK, OK, OK},
		},
		{
			desc:    "13: a sensor below the limit keeps running",
			sensors: &fakeSource{reading: 42500},
			want:    []Verdict{OK, OK},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			disablePath := filepath.Join(dir, "disable")
			overridePath := filepath.Join(dir, "override")
			tc.disable.apply(t, disablePath)
			tc.override.apply(t, overridePath)

			g := &Guard{
				Board:        "TESTBOARD",
				Max:          limit,
				Sensors:      tc.sensors,
				DisablePath:  disablePath,
				OverridePath: overridePath,
				Interval:     time.Millisecond,
				ReadRetries:  2,
				Log:          quietLogger(),
			}

			for i, want := range tc.want {
				got := g.Check()
				if got.Verdict != want {
					t.Fatalf("pass %d: verdict = %s, want %s (reading %s, err %v)",
						i+1, got.Verdict, want, got.Reading, got.Err)
				}
				if got.Verdict == Trip && got.Err == nil {
					t.Fatalf("pass %d: a trip must carry a reason", i+1)
				}
			}
		})
	}
}

// TestRetryBudgetResets makes sure a tolerated failure does not accumulate
// across unrelated successes: a sensor that hiccups once an hour must not
// trip the node after three hours.
func TestRetryBudgetResets(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{reading: 40000}
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      src,
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
	}

	for range 5 {
		src.err = errors.New("transient")
		if got := g.Check().Verdict; got != Tolerated {
			t.Fatalf("verdict = %s, want %s", got, Tolerated)
		}
		src.err = nil
		if got := g.Check().Verdict; got != OK {
			t.Fatalf("verdict = %s, want %s", got, OK)
		}
	}
}

// TestDisableFileResetsRetryBudget covers the operator who suspends checking
// while a sensor is being serviced: on resuming, the budget is whole again.
func TestDisableFileResetsRetryBudget(t *testing.T) {
	dir := t.TempDir()
	disablePath := filepath.Join(dir, "disable")
	src := &fakeSource{err: errors.New("being serviced")}
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      src,
		DisablePath:  disablePath,
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
	}

	if got := g.Check().Verdict; got != Tolerated {
		t.Fatalf("verdict = %s, want %s", got, Tolerated)
	}
	if err := os.WriteFile(disablePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := g.Check().Verdict; got != Disabled {
		t.Fatalf("verdict = %s, want %s", got, Disabled)
	}
	if err := os.Remove(disablePath); err != nil {
		t.Fatal(err)
	}
	for i, want := range []Verdict{Tolerated, Tolerated, Trip} {
		if got := g.Check().Verdict; got != want {
			t.Fatalf("pass %d after re-arming: verdict = %s, want %s", i+1, got, want)
		}
	}
}

// TestZeroRetriesTripsAtOnce covers --read-retries=0, which a site that
// prefers the strictest possible guard can set.
func TestZeroRetriesTripsAtOnce(t *testing.T) {
	dir := t.TempDir()
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{err: errors.New("input/output error")},
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  0,
		Log:          quietLogger(),
	}
	if got := g.Check().Verdict; got != Trip {
		t.Fatalf("verdict = %s, want %s", got, Trip)
	}
}

// TestRunStopsCleanly is the property that keeps `systemctl stop` from firing
// the emergency stop: a cancelled context is not a failure.
func TestRunStopsCleanly(t *testing.T) {
	dir := t.TempDir()
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{reading: 40000},
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := g.Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on a cancelled context", err)
	}
}

// TestRunReportsTrip is the other half: a trip must reach the caller as an
// error, so main exits non-zero and systemd arms the emergency stop.
func TestRunReportsTrip(t *testing.T) {
	dir := t.TempDir()
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{reading: 90000},
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
	}

	err := g.Run(context.Background())
	if !errors.Is(err, ErrTripped) {
		t.Fatalf("Run returned %v, want an error wrapping ErrTripped", err)
	}
}

// TestObserveSeesEveryPass is what the watchdog keep-alive hangs off.
func TestObserveSeesEveryPass(t *testing.T) {
	dir := t.TempDir()
	passes := 0
	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{reading: 90000},
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: filepath.Join(dir, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
		Observe:      func(Result) { passes++ },
	}
	if err := g.Run(context.Background()); !errors.Is(err, ErrTripped) {
		t.Fatalf("Run returned %v, want a trip", err)
	}
	if passes != 1 {
		t.Errorf("Observe called %d times, want 1", passes)
	}
}

// TestSensorsReadsTheHottest covers a board watched on both sockets: the
// guard compares the hottest of them, and a socket that cannot be read makes
// the whole reading fail rather than silently halving the coverage.
func TestSensorsReadsTheHottest(t *testing.T) {
	dir := t.TempDir()
	// Built directly rather than through Discover: what is under test here is
	// the reduction over several sensors, not the sysfs layout.
	classDir := filepath.Join(dir, "class", "hwmon")
	for i, reading := range []string{"50000", "72000"} {
		devDir := filepath.Join(dir, "devices", "d", string(rune('a'+i)))
		hwmonDir := filepath.Join(devDir, "hwmon", "hwmon"+string(rune('0'+i)))
		if err := os.MkdirAll(hwmonDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(classDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"name": "k10temp", "temp1_input": reading, "temp1_label": "Tctl",
		} {
			if err := os.WriteFile(filepath.Join(hwmonDir, name), []byte(content+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(hwmonDir, filepath.Join(classDir, "hwmon"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}

	sensors, err := hwmon.Discover(classDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sensors) != 2 {
		t.Fatalf("discovered %d sensors, want 2", len(sensors))
	}

	src := NewSensors(sensors)
	got, err := src.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != 72000 {
		t.Errorf("reading = %s, want 72.000C", got)
	}

	if _, err := NewSensors(nil).Read(); err == nil {
		t.Error("reading an empty sensor set succeeded, want an error")
	}
}

// TestUnresolvableDisablePathKeepsTheGuardArmed pins the fail-closed rule for
// the escape hatch: only a disable file that can actually be stated suspends
// checking. A path that errors for any other reason is not an operator asking
// for the guard to stand down.
func TestUnresolvableDisablePathKeepsTheGuardArmed(t *testing.T) {
	dir := t.TempDir()
	// A regular file used as a directory component makes stat fail with
	// ENOTDIR rather than ENOENT.
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{reading: 99000},
		DisablePath:  filepath.Join(blocker, "disable"),
		OverridePath: filepath.Join(blocker, "override"),
		Interval:     time.Millisecond,
		ReadRetries:  2,
		Log:          quietLogger(),
	}
	if got := g.Check().Verdict; got != Trip {
		t.Fatalf("verdict = %s, want %s: an unresolvable disable path must not suspend checking", got, Trip)
	}
}

// TestUnreadableOverrideStillCountsAsPresent is the other half of that rule:
// stating a file needs no permission on the file itself, so a mode 0000
// override is present and spends the retry budget rather than silently
// handing the decision back to the hardware.
func TestUnreadableOverrideStillCountsAsPresent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("cannot make a file unreadable as root")
	}
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "override")
	if err := os.WriteFile(overridePath, []byte("25"), 0o000); err != nil {
		t.Fatal(err)
	}

	g := &Guard{
		Board:        "TESTBOARD",
		Max:          85000,
		Sensors:      &fakeSource{reading: 40000},
		DisablePath:  filepath.Join(dir, "disable"),
		OverridePath: overridePath,
		Interval:     time.Millisecond,
		ReadRetries:  0,
		Log:          quietLogger(),
	}
	res := g.Check()
	if res.Verdict != Trip {
		t.Fatalf("verdict = %s, want %s", res.Verdict, Trip)
	}
	if !strings.Contains(res.Source, "override") {
		t.Errorf("source = %q, want the override file: the hardware must not have been consulted", res.Source)
	}
}
