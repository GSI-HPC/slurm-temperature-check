// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
# A comment, and a blank line above.
[H11DSi-NT]
chip = k10temp-pci-00c3
sensor = Tctl
max_celsius = 85

; Semicolons start a comment too.
[BC11SPSCA0]
chip        =   coretemp-isa-0000
sensor      =   Package id 0
max_celsius =   90

[TESTBOARD]
chip = k10temp
max_celsius = 60.5
`

func TestParse(t *testing.T) {
	cfg, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got, want := len(cfg.Boards), 3; got != want {
		t.Fatalf("parsed %d boards, want %d", got, want)
	}

	b, err := cfg.Lookup("H11DSi-NT")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if b.Chip != "k10temp-pci-00c3" || b.Sensor != "Tctl" || b.Max != 85000 {
		t.Errorf("got %+v, want chip k10temp-pci-00c3, sensor Tctl, max 85000", b)
	}

	// Whitespace around the separator is trimmed, but a value's own internal
	// spaces are not: "Package id 0" is one label.
	b, _ = cfg.Lookup("BC11SPSCA0")
	if b.Sensor != "Package id 0" {
		t.Errorf("sensor = %q, want %q", b.Sensor, "Package id 0")
	}

	// An omitted sensor is the documented "lowest attribute of the chip".
	b, _ = cfg.Lookup("TESTBOARD")
	if b.Sensor != "" {
		t.Errorf("sensor = %q, want it empty", b.Sensor)
	}
	if b.Max != 60500 {
		t.Errorf("max = %d, want 60500", b.Max)
	}

	if _, err := cfg.Lookup("NOSUCHBOARD"); err == nil {
		t.Error("looking up an unconfigured board succeeded, want an error")
	} else if !strings.Contains(err.Error(), "H11DSi-NT") {
		// The error has to list what is configured; an operator reading it in
		// the journal is trying to find out what to add.
		t.Errorf("error %q does not name the configured boards", err)
	}

	want := []string{"BC11SPSCA0", "H11DSi-NT", "TESTBOARD"}
	got := cfg.BoardNames()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BoardNames() = %v, want %v", got, want)
		}
	}
}

// TestParseRejects covers every way the table can be wrong. Each of these
// would, if accepted, produce a node that looks guarded and is not, so the
// parser refuses rather than defaults.
func TestParseRejects(t *testing.T) {
	tests := []struct {
		desc  string
		input string
		// want is a fragment the message must contain, so a reader of the
		// journal is pointed at the line rather than at the file.
		want string
	}{
		{
			desc:  "a key before any section",
			input: "chip = k10temp\n[B]\nchip = k10temp\nmax_celsius = 80\n",
			want:  "before the first",
		},
		{
			desc:  "a board defined twice",
			input: "[B]\nchip = k10temp\nmax_celsius = 80\n[B]\nchip = coretemp\nmax_celsius = 70\n",
			want:  "already defined",
		},
		{
			desc:  "a key set twice in one board",
			input: "[B]\nchip = k10temp\nchip = coretemp\nmax_celsius = 80\n",
			want:  "set twice",
		},
		{
			desc:  "a misspelled key, which would otherwise be ignored",
			input: "[B]\nchip = k10temp\nsensr = Tctl\nmax_celsius = 80\n",
			want:  "unknown key",
		},
		{
			desc:  "a missing threshold",
			input: "[B]\nchip = k10temp\n",
			want:  "no max_celsius",
		},
		{
			desc:  "a missing chip",
			input: "[B]\nmax_celsius = 80\n",
			want:  "no chip",
		},
		{
			desc:  "an empty chip",
			input: "[B]\nchip =\nmax_celsius = 80\n",
			want:  "empty chip",
		},
		{
			desc:  "a non-numeric threshold",
			input: "[B]\nchip = k10temp\nmax_celsius = eighty\n",
			want:  "is not a number",
		},
		{
			desc:  "a threshold so high the guard would never trip",
			input: "[B]\nchip = k10temp\nmax_celsius = 850\n",
			want:  "plausible range",
		},
		{
			desc:  "a threshold so low the guard would trip at idle",
			input: "[B]\nchip = k10temp\nmax_celsius = 8\n",
			want:  "plausible range",
		},
		{
			desc:  "a line that is neither a section nor a pair",
			input: "[B]\nchip k10temp\nmax_celsius = 80\n",
			want:  "neither a section header",
		},
		{
			desc:  "an unterminated section header",
			input: "[B\nchip = k10temp\nmax_celsius = 80\n",
			want:  "unterminated",
		},
		{
			desc:  "an empty section header",
			input: "[]\nchip = k10temp\nmax_celsius = 80\n",
			want:  "empty section",
		},
		{
			desc:  "an empty table",
			input: "# nothing here\n",
			want:  "no boards configured",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestParsedThresholdBounds fixes the ends of the accepted range, so a change
// to them is a deliberate one.
func TestParsedThresholdBounds(t *testing.T) {
	for _, v := range []string{"20", "150"} {
		if _, err := Parse(strings.NewReader("[B]\nchip = c\nmax_celsius = " + v + "\n")); err != nil {
			t.Errorf("max_celsius = %s rejected: %v", v, err)
		}
	}
	// nan is in this list because every comparison against it is false: a
	// range test written as "below the minimum or above the maximum" accepts
	// it, and it then converts to a limit of about -9.2e15 C, which every
	// real reading exceeds. A table with one such entry drains the node on
	// its first pass.
	for _, v := range []string{"19.9", "150.1", "-40", "0", "nan", "NaN", "inf", "-inf"} {
		if _, err := Parse(strings.NewReader("[B]\nchip = c\nmax_celsius = " + v + "\n")); err == nil {
			t.Errorf("max_celsius = %s accepted, want it rejected", v)
		}
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boards.conf")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Boards) != 3 {
		t.Errorf("parsed %d boards, want 3", len(cfg.Boards))
	}

	if _, err := Load(filepath.Join(t.TempDir(), "missing.conf")); err == nil {
		t.Error("loading a missing table succeeded, want an error")
	}

	// A parse error has to name the file; Parse alone only knows line numbers.
	bad := filepath.Join(t.TempDir(), "bad.conf")
	if err := os.WriteFile(bad, []byte("[B]\nchip = c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("error %v does not name %s", err, bad)
	}
}

// repoRoot walks up from the test's working directory to the directory holding
// go.mod. Walking rather than assuming "../.." keeps the test working under
// the RPM %gocheck macro, which runs it from a tree it laid out itself.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no go.mod above the working directory; not running from a source tree")
		}
		dir = parent
	}
}

// TestShippedTableParses keeps the packaged default honest: it is installed as
// %config(noreplace) and a node that cannot parse it has no guard at all.
func TestShippedTableParses(t *testing.T) {
	path := filepath.Join(repoRoot(t), "packaging", "config", "boards.conf")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped board table does not parse: %v", err)
	}
	if _, err := cfg.Lookup("TESTBOARD"); err != nil {
		t.Errorf("the shipped table has no TESTBOARD entry, which the install test needs: %v", err)
	}
}
