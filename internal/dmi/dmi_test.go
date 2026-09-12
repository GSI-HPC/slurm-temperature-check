// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package dmi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoardName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board_name")

	// The kernel writes the SMBIOS string with a trailing newline.
	if err := os.WriteFile(path, []byte("H12DSG-O-CPU\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := BoardName(path)
	if err != nil {
		t.Fatalf("BoardName: %v", err)
	}
	if got != "H12DSG-O-CPU" {
		t.Errorf("board name = %q, want %q", got, "H12DSG-O-CPU")
	}

	// Firmware that leaves the field blank must not produce a board named "",
	// which could match a stray empty section in the table.
	if err := os.WriteFile(path, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BoardName(path); err == nil {
		t.Error("an empty board_name was accepted, want an error")
	}

	if _, err := BoardName(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing board_name was accepted, want an error")
	}
}

func TestBoardNameIn(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "devices", "virtual", "dmi", "id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "board_name"), []byte("TESTBOARD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := BoardNameIn(root)
	if err != nil {
		t.Fatalf("BoardNameIn: %v", err)
	}
	if got != "TESTBOARD" {
		t.Errorf("board name = %q, want %q", got, "TESTBOARD")
	}
}
