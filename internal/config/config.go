// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package config parses the board table: which sensor to watch on a given
// mainboard, and how hot that sensor may get.
//
// The format is the sectioned key/value syntax systemd and sysconfig files
// already use on these nodes, parsed here with the standard library only. The
// parser is deliberately strict — an unknown key, a repeated section, a
// missing threshold or a threshold outside a plausible range is an error
// rather than a default — because every one of those mistakes would otherwise
// leave a node running jobs with a guard that never trips.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/GSI-HPC/slurm-temperature-check/internal/hwmon"
)

// DefaultPath is where the RPM installs the board table.
const DefaultPath = "/etc/slurm-temperature-check/boards.conf"

// Plausible bounds for a threshold, in degrees Celsius. A value outside them
// is a typo, and the two directions fail differently: too low trips
// constantly, too high is a guard that never fires at all. Both are refused.
const (
	minThresholdCelsius = 20
	maxThresholdCelsius = 150
)

// Board is one entry of the table.
type Board struct {
	// Name is the section header, matched against the DMI board name.
	Name string
	// Chip selects hwmon chips, in full or by driver prefix.
	Chip string
	// Sensor selects a label or attribute within those chips; empty means
	// the lowest-numbered temperature attribute of each.
	Sensor string
	// Max is the highest reading the board may report.
	Max hwmon.MilliCelsius
	// Line is where the section starts, for diagnostics.
	Line int
}

// Config is the parsed table, keyed by board name.
type Config struct {
	Boards map[string]Board
}

// BoardNames returns the configured board names in sorted order.
func (c *Config) BoardNames() []string {
	names := make([]string, 0, len(c.Boards))
	for n := range c.Boards {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Lookup returns the entry for a board name.
//
// An unknown board is an error, not an implied default: the whole point of
// the table is that a threshold is a property of the hardware, and a node
// whose hardware is not described cannot be guarded.
func (c *Config) Lookup(board string) (Board, error) {
	b, ok := c.Boards[board]
	if !ok {
		return Board{}, fmt.Errorf("board %q is not in the table (configured: %s)",
			board, strings.Join(c.BoardNames(), ", "))
	}
	return b, nil
}

// Load reads and parses the table at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open board table: %w", err)
	}
	// The file is only read, so a close error says nothing useful.
	defer func() { _ = f.Close() }()

	cfg, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads the table from r.
func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{Boards: map[string]Board{}}

	var (
		current string
		seen    = map[string]map[string]bool{}
		sc      = bufio.NewScanner(r)
		lineNo  int
	)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: unterminated section header %q", lineNo, line)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return nil, fmt.Errorf("line %d: empty section header", lineNo)
			}
			if _, dup := cfg.Boards[name]; dup {
				return nil, fmt.Errorf("line %d: board %q is already defined on line %d",
					lineNo, name, cfg.Boards[name].Line)
			}
			cfg.Boards[name] = Board{Name: name, Line: lineNo}
			seen[name] = map[string]bool{}
			current = name
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: %q is neither a section header nor a key = value pair", lineNo, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if current == "" {
			return nil, fmt.Errorf("line %d: %q appears before the first [board] section", lineNo, key)
		}
		if seen[current][key] {
			return nil, fmt.Errorf("line %d: %q is set twice in board %q", lineNo, key, current)
		}
		seen[current][key] = true

		b := cfg.Boards[current]
		switch key {
		case "chip":
			if value == "" {
				return nil, fmt.Errorf("line %d: board %q has an empty chip", lineNo, current)
			}
			b.Chip = value
		case "sensor":
			b.Sensor = value
		case "max_celsius":
			m, err := parseThreshold(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: board %q: %w", lineNo, current, err)
			}
			b.Max = m
		default:
			return nil, fmt.Errorf("line %d: board %q has unknown key %q (known: chip, sensor, max_celsius)",
				lineNo, current, key)
		}
		cfg.Boards[current] = b
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	for _, name := range cfg.BoardNames() {
		b := cfg.Boards[name]
		if b.Chip == "" {
			return nil, fmt.Errorf("board %q (line %d) has no chip", name, b.Line)
		}
		if !seen[name]["max_celsius"] {
			return nil, fmt.Errorf("board %q (line %d) has no max_celsius", name, b.Line)
		}
	}
	if len(cfg.Boards) == 0 {
		return nil, fmt.Errorf("no boards configured")
	}
	return cfg, nil
}

func parseThreshold(s string) (hwmon.MilliCelsius, error) {
	if s == "" {
		return 0, fmt.Errorf("max_celsius is empty")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("max_celsius %q is not a number", s)
	}
	if v < minThresholdCelsius || v > maxThresholdCelsius {
		return 0, fmt.Errorf("max_celsius %s is outside the plausible range %d..%d",
			s, minThresholdCelsius, maxThresholdCelsius)
	}
	return hwmon.MilliCelsius(v * 1000), nil
}
