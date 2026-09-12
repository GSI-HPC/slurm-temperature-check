// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// node builds a fabricated sysfs tree plus a board table, and returns the
// flags that point the program at it. It is the same shape the package's
// install test builds in a container, where no real sensor exists.
type node struct {
	board   string
	chip    string
	label   string
	reading string
	table   string
}

func (n node) flags(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()

	hwmonDir := filepath.Join(root, "devices", "pci0000:00", "0000:00:18.3", "hwmon", "hwmon0")
	classDir := filepath.Join(root, "class", "hwmon")
	busDir := filepath.Join(root, "bus", "pci")
	for _, d := range []string{hwmonDir, classDir, busDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"name": n.chip}
	if n.reading != "" {
		files["temp1_input"] = n.reading
	}
	if n.label != "" {
		files["temp1_label"] = n.label
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(hwmonDir, name), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "devices", "pci0000:00", "0000:00:18.3"),
		filepath.Join(hwmonDir, "device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(busDir,
		filepath.Join(root, "devices", "pci0000:00", "0000:00:18.3", "subsystem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hwmonDir, filepath.Join(classDir, "hwmon0")); err != nil {
		t.Fatal(err)
	}

	boardPath := filepath.Join(root, "board_name")
	if err := os.WriteFile(boardPath, []byte(n.board+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tablePath := filepath.Join(root, "boards.conf")
	if err := os.WriteFile(tablePath, []byte(n.table), 0o644); err != nil {
		t.Fatal(err)
	}

	return []string{
		"--hwmon.path=" + classDir,
		"--board-name-path=" + boardPath,
		"--config=" + tablePath,
		"--disable-file=" + filepath.Join(root, "disable"),
		"--override-file=" + filepath.Join(root, "override"),
	}
}

const testTable = "[TESTBOARD]\nchip = k10temp\nsensor = Tctl\nmax_celsius = 60\n"

func healthy() node {
	return node{board: "TESTBOARD", chip: "k10temp", label: "Tctl", reading: "42000", table: testTable}
}

func exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersion(t *testing.T) {
	code, stdout, _ := exec(t, "--version")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "slurm-temperature-check") {
		t.Errorf("stdout = %q, want it to name the program", stdout)
	}
}

func TestList(t *testing.T) {
	n := healthy()
	code, stdout, _ := exec(t, append(n.flags(t), "--list")...)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	// --list is what an operator pastes into the board table, so it has to
	// print the composed chip name, the label and a live reading.
	for _, want := range []string{"k10temp-pci-00c3", "Tctl", "42.000C"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--list output does not contain %q:\n%s", want, stdout)
		}
	}
}

// TestListWorksWithoutAConfig matters because --list is the first thing run
// on a node whose board is not in the table yet.
func TestListWorksWithoutAConfig(t *testing.T) {
	n := healthy()
	args := append(n.flags(t), "--list", "--config=/nonexistent/boards.conf")
	if code, _, stderr := exec(t, args...); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		desc string
		node node
		want int
		// contains is a fragment the output must carry.
		contains string
	}{
		{
			desc:     "a healthy node reports ok",
			node:     healthy(),
			want:     exitOK,
			contains: "ok",
		},
		{
			desc: "a node over its limit reports a trip",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tctl",
				reading: "61000", table: testTable},
			want:     exitTripped,
			contains: "trip",
		},
		{
			desc: "a limit is inclusive",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tctl",
				reading: "60000", table: testTable},
			want:     exitOK,
			contains: "ok",
		},
		{
			desc: "a thousandth over the limit still trips",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tctl",
				reading: "60001", table: testTable},
			want:     exitTripped,
			contains: "trip",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			code, stdout, stderr := exec(t, append(tc.node.flags(t), "--check")...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stdout: %s stderr: %s)", code, tc.want, stdout, stderr)
			}
			if !strings.Contains(stdout, tc.contains) {
				t.Errorf("stdout %q does not contain %q", stdout, tc.contains)
			}
		})
	}
}

// TestCannotArm covers everything that stops the guard from starting at all.
// Each of these exits with exitConfig, which arms the emergency stop, so each
// one has to be a real misconfiguration rather than a transient.
func TestCannotArm(t *testing.T) {
	tests := []struct {
		desc string
		node node
		// extra flags appended after the node's own.
		extra []string
		want  string
	}{
		{
			desc: "the board is not in the table",
			node: node{board: "UNKNOWNBOARD", chip: "k10temp", label: "Tctl",
				reading: "42000", table: testTable},
			want: "is not in the table",
		},
		{
			desc: "the configured chip is not present on the node",
			node: node{board: "TESTBOARD", chip: "coretemp", label: "Package id 0",
				reading: "42000", table: testTable},
			want: "no hwmon chip matches",
		},
		{
			desc: "the configured sensor is not on the chip",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tdie",
				reading: "42000", table: testTable},
			want: "has no sensor",
		},
		{
			desc: "the sensor is present but unreadable",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tctl",
				reading: "no number here", table: testTable},
			want: "is not an integer",
		},
		{
			desc:  "the board table does not exist",
			node:  healthy(),
			extra: []string{"--config=/nonexistent/boards.conf"},
			want:  "open board table",
		},
		{
			desc: "the board table does not parse",
			node: node{board: "TESTBOARD", chip: "k10temp", label: "Tctl",
				reading: "42000", table: "[TESTBOARD]\nchip = k10temp\n"},
			want: "no max_celsius",
		},
		{
			desc:  "the board name cannot be read",
			node:  healthy(),
			extra: []string{"--board-name-path=/nonexistent/board_name"},
			want:  "read board name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			args := append(tc.node.flags(t), "--check")
			args = append(args, tc.extra...)
			code, _, stderr := exec(t, args...)
			if code != exitConfig {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitConfig, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.want)
			}
		})
	}
}

func TestBadFlags(t *testing.T) {
	n := healthy()
	tests := [][]string{
		{"--interval=0"},
		{"--interval=-5s"},
		{"--read-retries=-1"},
		{"--log.level=chatty"},
		{"--log.format=xml"},
		{"stray-argument"},
	}
	for _, extra := range tests {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			args := append(n.flags(t), "--check")
			args = append(args, extra...)
			if code, _, _ := exec(t, args...); code != exitConfig {
				t.Errorf("exit = %d, want %d", code, exitConfig)
			}
		})
	}
}

// TestOverrideStandsInForTheSensor is the documented way to prove on a live
// node that the mechanism works, so it is covered end to end.
func TestOverrideStandsInForTheSensor(t *testing.T) {
	n := healthy()
	args := n.flags(t)
	var overridePath string
	for _, a := range args {
		if strings.HasPrefix(a, "--override-file=") {
			overridePath = strings.TrimPrefix(a, "--override-file=")
		}
	}

	for _, tc := range []struct {
		content string
		want    int
	}{
		{"25", exitOK},
		{"61", exitTripped},
		// A reading that cannot be parsed is a failure for a one-shot check,
		// which has no retry budget to spend on it.
		{"warm", exitTripped},
	} {
		t.Run(tc.content, func(t *testing.T) {
			if err := os.WriteFile(overridePath, []byte(tc.content+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := exec(t, append(args, "--check")...)
			if code != tc.want {
				t.Fatalf("override %q: exit = %d, want %d (stdout %s stderr %s)",
					tc.content, code, tc.want, stdout, stderr)
			}
			// The reading did not come from the sensors, and saying so is
			// the difference between "this node is guarded" and "this node
			// is reporting a number somebody typed".
			if !strings.Contains(stdout, "source   "+overridePath) {
				t.Errorf("override %q: --check output does not name the override "+
					"as the source:\n%s", tc.content, stdout)
			}
		})
	}
}

// TestDisableFileSuspends proves the escape hatch reaches the exit status: a
// node whose sensor is over the limit reports ok while the file is there.
func TestDisableFileSuspends(t *testing.T) {
	n := node{board: "TESTBOARD", chip: "k10temp", label: "Tctl", reading: "99000", table: testTable}
	args := n.flags(t)
	var disablePath string
	for _, a := range args {
		if strings.HasPrefix(a, "--disable-file=") {
			disablePath = strings.TrimPrefix(a, "--disable-file=")
		}
	}

	if code, _, _ := exec(t, append(args, "--check")...); code != exitTripped {
		t.Fatalf("exit = %d before the disable file, want %d", code, exitTripped)
	}
	if err := os.WriteFile(disablePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := exec(t, append(args, "--check")...)
	if code != exitOK {
		t.Fatalf("exit = %d with the disable file, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "disabled") {
		t.Errorf("stdout %q does not say the check is disabled", stdout)
	}
}

func TestLogFormats(t *testing.T) {
	n := healthy()
	for _, format := range []string{"text", "json"} {
		args := append(n.flags(t), "--check", "--log.format="+format,
			"--config=/nonexistent/boards.conf")
		_, _, stderr := exec(t, args...)
		if format == "json" && !strings.HasPrefix(strings.TrimSpace(stderr), "{") {
			t.Errorf("json output is not json: %q", stderr)
		}
		if !strings.Contains(stderr, "cannot arm the guard") {
			t.Errorf("%s: stderr %q does not carry the error", format, stderr)
		}
	}
}
