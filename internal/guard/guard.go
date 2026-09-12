// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package guard implements the thermal guard: a loop that decides, once per
// interval, whether this node may keep running SLURM jobs.
//
// The guard never stops anything itself. It reports its verdict by exiting:
// zero when it was asked to stop, non-zero when the node must not keep
// running jobs. systemd turns the non-zero exit into the emergency stop
// through OnFailure=, which keeps every privileged action in a separate unit
// and lets this one run unprivileged.
//
// Every path that does not end in a temperature at or below the threshold
// ends in a trip. An unreadable sensor, an unparseable reading, a board that
// is not in the table and a threshold that cannot be evaluated are all
// "the temperature is unknown", and an unknown temperature is not a safe one.
package guard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/GSI-HPC/slurm-temperature-check/internal/hwmon"
)

// Verdict is the outcome of one check.
type Verdict int

const (
	// OK means the reading is at or below the threshold.
	OK Verdict = iota
	// Disabled means the disable file is present and no reading was taken.
	Disabled
	// Tolerated means the reading failed but the retry budget is not spent.
	Tolerated
	// Trip means the node must stop running jobs.
	Trip
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Disabled:
		return "disabled"
	case Tolerated:
		return "tolerated"
	case Trip:
		return "trip"
	}
	return "unknown"
}

// Source is where a reading comes from. Both the hwmon sensors and the
// override file implement it, which is what lets the override file stand in
// for the hardware in a test on a real node.
type Source interface {
	// Name identifies the source in a log line.
	Name() string
	// Read returns the current reading, or an error when it cannot be
	// determined.
	Read() (hwmon.MilliCelsius, error)
}

// Guard holds everything one node's guard needs.
type Guard struct {
	// Board is the mainboard this guard was resolved for, for log lines.
	Board string
	// Max is the highest reading the node may report.
	Max hwmon.MilliCelsius
	// Sensors is the hwmon source, used whenever no override is present.
	Sensors Source
	// DisablePath, while it exists, suspends checking entirely. It is stated
	// on every pass, so creating or removing it takes effect within one
	// interval without touching the unit or this program.
	DisablePath string
	// OverridePath, while it exists, is read instead of the sensors. It is
	// the supported way to prove on a live node that the mechanism works.
	OverridePath string
	// Interval is the delay between checks.
	Interval time.Duration
	// ReadRetries is how many consecutive failed readings are tolerated
	// before the guard trips.
	ReadRetries int
	// Log receives the guard's diagnostics.
	Log *slog.Logger
	// Observe, when set, is called after every pass. It is how the caller
	// hangs a watchdog keep-alive off the loop: a ping sent from here proves
	// the loop is turning, which a ping on a timer of its own would not.
	Observe func(Result)

	// failures counts consecutive failed readings.
	failures int
	// lastVerdict suppresses the log line when nothing changed: at a ten
	// second interval an "ok" per pass would be nine thousand journal
	// entries a day and would bury the transitions that matter.
	lastVerdict Verdict
	// lastSource is the other half of that key. Where a reading came from
	// is part of what the log line says, so a change of source has to
	// defeat the suppression even when the verdict is unchanged.
	lastSource string
	// started guards the first log line, so the initial verdict is always
	// reported even when it equals the zero value of lastVerdict.
	started bool
}

// Result is one check's outcome.
type Result struct {
	Verdict Verdict
	// Reading is the value that was compared. It is only meaningful when
	// HasReading is set: a suspended check takes none, and a failed one has
	// none to report.
	Reading hwmon.MilliCelsius
	// HasReading says whether a reading was obtained at all.
	HasReading bool
	// Source names where the reading came from.
	Source string
	// Err is why the reading failed, for Tolerated and for a Trip caused by
	// a failed reading rather than by heat.
	Err error
}

// ErrTripped is returned by Run when the guard tripped.
var ErrTripped = errors.New("thermal guard tripped")

// Check performs one pass of the state machine.
//
// The order is fixed and each step is a gate on the next: the disable file
// outranks everything, an override outranks the hardware, and only a reading
// that was obtained and parsed is compared against the threshold.
func (g *Guard) Check() Result {
	if exists(g.DisablePath) {
		g.failures = 0
		return Result{Verdict: Disabled, Source: g.DisablePath}
	}

	src := g.Sensors
	if exists(g.OverridePath) {
		src = &fileSource{path: g.OverridePath}
	}

	reading, err := src.Read()
	if err != nil {
		g.failures++
		if g.failures <= g.ReadRetries {
			return Result{Verdict: Tolerated, Source: src.Name(), Err: err}
		}
		return Result{Verdict: Trip, Source: src.Name(), Err: err}
	}
	g.failures = 0

	if reading > g.Max {
		return Result{
			Verdict:    Trip,
			Reading:    reading,
			HasReading: true,
			Source:     src.Name(),
			Err:        fmt.Errorf("%s exceeds the %s limit of board %s", reading, g.Max, g.Board),
		}
	}
	return Result{Verdict: OK, Reading: reading, HasReading: true, Source: src.Name()}
}

// Run checks until the guard trips or ctx is cancelled.
//
// A cancelled context is a clean stop and returns nil, so that `systemctl
// stop` does not look like a failure and does not fire the emergency stop. A
// trip returns ErrTripped wrapping the reason.
func (g *Guard) Run(ctx context.Context) error {
	g.Log.Info("guard armed",
		"board", g.Board,
		"limit", g.Max.String(),
		"sensors", g.Sensors.Name(),
		"interval", g.Interval.String(),
		"read_retries", g.ReadRetries)

	t := time.NewTicker(g.Interval)
	defer t.Stop()

	for {
		res := g.Check()
		g.report(res)
		if g.Observe != nil {
			g.Observe(res)
		}
		if res.Verdict == Trip {
			return fmt.Errorf("%w: %w", ErrTripped, res.Err)
		}

		select {
		case <-ctx.Done():
			g.Log.Info("asked to stop, exiting cleanly", "board", g.Board)
			return nil
		case <-t.C:
		}
	}
}

// report logs a pass, quietly when nothing changed.
//
// "Nothing changed" covers where the reading came from as well as the
// verdict. While the override file is present it is read in place of the
// hardware, and that transition leaves the verdict at OK, so a suppression
// keyed on the verdict alone said nothing at all: an override forgotten after
// a test left the node reporting a number an operator once typed, for as long
// as the file was there, with a clean journal.
func (g *Guard) report(res Result) {
	unchanged := g.started && res.Verdict == g.lastVerdict && res.Source == g.lastSource

	switch res.Verdict {
	case Trip:
		g.Log.Error("guard tripped, the node must stop running jobs",
			"board", g.Board, "source", res.Source, "reason", res.Err.Error())
	case Tolerated:
		// Always logged: a tolerated failure is rare, and the sequence of
		// them is what explains a later trip.
		g.Log.Warn("reading failed, within the retry budget",
			"source", res.Source, "failures", g.failures,
			"read_retries", g.ReadRetries, "error", res.Err.Error())
	case Disabled:
		if unchanged {
			break
		}
		g.Log.Warn("checking suspended: the disable file is present",
			"path", res.Source)
	case OK:
		if unchanged {
			g.Log.Debug("ok", "reading", res.Reading.String(), "source", res.Source)
			break
		}
		// The override standing in for the hardware is a warning rather than
		// information: it is the supported way to test the mechanism, and it
		// is also the state a node must not be left in.
		if g.OverridePath != "" && res.Source == g.OverridePath {
			g.Log.Warn("the override file is being read in place of the sensors",
				"reading", res.Reading.String(), "override_file", res.Source,
				"sensors", g.Sensors.Name(), "limit", g.Max.String())
			break
		}
		g.Log.Info("checking resumed", "reading", res.Reading.String(),
			"source", res.Source, "limit", g.Max.String())
	}
	g.lastVerdict = res.Verdict
	g.lastSource = res.Source
	g.started = true
}

// exists reports whether path can be stated. An empty path is never present,
// so leaving the disable or override path unset switches that feature off.
//
// Only a successful stat counts, which is what keeps the disable file failing
// closed: any error, not just ENOENT, leaves the guard armed, because a path
// the guard cannot resolve is not an operator saying "suspend checking". The
// same rule applied to the override leaves the hardware as the ground truth
// whenever the override cannot be resolved.
//
// This does not weaken the "override present but unreadable" case. Stating a
// file needs no permission on the file itself, so a mode 0000 override still
// counts as present, and reading it then fails and spends the retry budget.
func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Bounds on a reading from the override file, in degrees Celsius. They are
// deliberately far wider than any sensor a mainboard carries, because their
// job is to reject values that are not temperatures at all rather than to
// second-guess the operator: absolute zero below, and above it enough room
// for the documented drill value of 999 and any other deliberately absurd
// figure someone picks to prove the mechanism fires.
const (
	minReadingCelsius = -273.15
	maxReadingCelsius = 10000
)

// fileSource reads a temperature in degrees Celsius from a file. It backs the
// override file, whose contents an operator writes with a shell redirect.
type fileSource struct {
	path string
}

func (f *fileSource) Name() string { return f.path }

func (f *fileSource) Read() (hwmon.MilliCelsius, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", f.path, err)
	}
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// An empty read is the expected shape of a torn write: a shell
		// redirect truncates before it writes. It is reported like any other
		// bad reading and covered by the same retry budget.
		return 0, fmt.Errorf("read %s: %q is not a temperature in degrees Celsius", f.path, s)
	}
	// ParseFloat accepts "nan" and "inf", and converting a float outside
	// int64's range to it is implementation-defined -- on amd64 it yields the
	// most negative int64, about -9.2e15 degrees, which compares as safely
	// below every limit. Unchecked, that is a way for the override file to
	// report a safe temperature on a node that is over its limit, which is
	// the one direction this program must never fail in. Anything that is not
	// a temperature is rejected here and spends the retry budget like any
	// other bad reading, so the node stops rather than keeps running.
	if math.IsNaN(v) || math.IsInf(v, 0) || v < minReadingCelsius || v > maxReadingCelsius {
		return 0, fmt.Errorf("read %s: %q is not a temperature in degrees Celsius", f.path, s)
	}
	return hwmon.MilliCelsius(math.Round(v * 1000)), nil
}

// Sensors adapts a set of hwmon sensors to Source, reporting the hottest of
// them.
//
// A single failed sensor fails the whole reading. With two sockets watched,
// a node whose second socket cannot be read is a node whose temperature is
// half unknown, and the guard treats unknown as unsafe.
type Sensors []hwmon.Sensor

// NewSensors adapts sensors to Source.
func NewSensors(sensors []hwmon.Sensor) Sensors { return Sensors(sensors) }

// Name lists the sensors being watched, joined with "+".
func (s Sensors) Name() string {
	names := make([]string, 0, len(s))
	for _, sensor := range s {
		names = append(names, sensor.Name())
	}
	return strings.Join(names, "+")
}

// Read returns the hottest of the sensors, or an error if any one of them
// cannot be read.
func (s Sensors) Read() (hwmon.MilliCelsius, error) {
	if len(s) == 0 {
		return 0, errors.New("no sensors selected")
	}
	var hottest hwmon.MilliCelsius
	for i, sensor := range s {
		v, err := sensor.Read()
		if err != nil {
			return 0, err
		}
		if i == 0 || v > hottest {
			hottest = v
		}
	}
	return hottest, nil
}
