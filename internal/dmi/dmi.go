// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package dmi reads the board identity the kernel exports from the SMBIOS
// tables. The board name is what selects a node's threshold and sensor, so
// it is read once at startup and a node whose name is unknown is refused
// rather than guessed at.
package dmi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPath is where the kernel exports the SMBIOS "base board product
// name" (type 2, offset 05h). It is mode 0444, so an unprivileged reader
// needs no capability; board_serial and product_uuid next to it are 0400,
// and this package deliberately touches neither.
const DefaultPath = "/sys/devices/virtual/dmi/id/board_name"

// BoardName returns the trimmed contents of path.
//
// An empty or whitespace-only file is an error: some firmware leaves the
// field blank, and silently treating that as a board named "" would let it
// match a stray empty section in the configuration.
func BoardName(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read board name: %w", err)
	}
	name := strings.TrimSpace(string(b))
	if name == "" {
		return "", fmt.Errorf("read board name: %s is empty", path)
	}
	return name, nil
}

// BoardNameIn is BoardName rooted at an alternative sysfs tree, for tests.
func BoardNameIn(root string) (string, error) {
	return BoardName(filepath.Join(root, "devices", "virtual", "dmi", "id", "board_name"))
}
