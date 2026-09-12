// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package hwmon reads temperatures straight from the kernel's hwmon class in
// sysfs, which is the same place the lm_sensors userspace reads them from.
//
// Chip names are composed the way libsensors composes them, so the names in
// this package's configuration are the names `sensors` prints. Reproducing
// that naming is a convenience, not a correctness requirement: the program's
// --list mode prints the names it computed on the node itself, which is what
// an operator should paste into the configuration.
package hwmon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultClassDir holds one symlink per hwmon device. Every temperature
// attribute below it is mode 0444, so nothing here needs privilege.
const DefaultClassDir = "/sys/class/hwmon"

// MilliCelsius is the unit of the hwmon tempN_input attributes: thousandths
// of a degree Celsius. Readings are kept in it end to end so that a
// threshold comparison never rounds.
type MilliCelsius int64

func (m MilliCelsius) String() string {
	return strconv.FormatFloat(float64(m)/1000, 'f', 3, 64) + "C"
}

// Sensor is one tempN_input attribute of a chip.
type Sensor struct {
	// Chip is the libsensors-style name of the chip this sensor belongs to,
	// e.g. "k10temp-pci-00c3".
	Chip string
	// Attr is the attribute base name, e.g. "temp1".
	Attr string
	// Label is the contents of tempN_label, e.g. "Tctl". Empty when the
	// driver exports no label for this attribute.
	Label string
	// Index is the N in tempN, used to order sensors within a chip.
	Index int

	path string
}

// Name is how a sensor is identified in a diagnostic: "chip/label" when the
// driver labels it, "chip/attr" otherwise.
func (s Sensor) Name() string {
	if s.Label != "" {
		return s.Chip + "/" + s.Label
	}
	return s.Chip + "/" + s.Attr
}

// Read returns the current reading.
//
// A read of a live hwmon attribute is a single kernel-side snapshot, so it
// cannot be torn the way a file written by another process can. It can still
// fail: drivers return EIO or ENODATA when the underlying bus transaction
// fails, and the attribute disappears with its device on a module unload.
// Both surface here as an error, and the caller treats an error as "the
// temperature is unknown".
func (s Sensor) Read() (MilliCelsius, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", s.Name(), err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read %s: %q is not an integer: %w", s.Name(), strings.TrimSpace(string(b)), err)
	}
	return MilliCelsius(v), nil
}

var tempAttr = regexp.MustCompile(`^temp([0-9]+)_input$`)

// Discover returns every temperature sensor below classDir, ordered by chip
// name and then by attribute index.
//
// Chips that export no temperature at all are skipped rather than reported:
// the hwmon class also carries fan, voltage and power-only devices.
func Discover(classDir string) ([]Sensor, error) {
	entries, err := os.ReadDir(classDir)
	if err != nil {
		return nil, fmt.Errorf("list hwmon devices: %w", err)
	}

	var out []Sensor
	for _, e := range entries {
		dir := filepath.Join(classDir, e.Name())
		chip, err := chipName(dir)
		if err != nil {
			// A device that vanished between the listing and the read, or
			// one without a name attribute, is not a reason to refuse every
			// other chip on the node.
			continue
		}
		sensors, err := chipSensors(dir, chip)
		if err != nil {
			continue
		}
		out = append(out, sensors...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Chip != out[j].Chip {
			return out[i].Chip < out[j].Chip
		}
		return out[i].Index < out[j].Index
	})
	return out, nil
}

func chipSensors(dir, chip string) ([]Sensor, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Sensor
	for _, f := range files {
		m := tempAttr.FindStringSubmatch(f.Name())
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		attr := "temp" + m[1]
		label := ""
		if b, err := os.ReadFile(filepath.Join(dir, attr+"_label")); err == nil {
			label = strings.TrimSpace(string(b))
		}
		out = append(out, Sensor{
			Chip:  chip,
			Attr:  attr,
			Label: label,
			Index: idx,
			path:  filepath.Join(dir, f.Name()),
		})
	}
	return out, nil
}

// chipName composes the libsensors-style name of the hwmon device in dir.
//
// libsensors builds it from the driver's "name" attribute plus the bus type
// and address of the parent device (lib/sysfs.c, lib/access.c). The bus type
// comes from the parent's subsystem, the address from its sysfs name.
func chipName(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "name"))
	if err != nil {
		return "", err
	}
	prefix := strings.TrimSpace(string(b))
	if prefix == "" {
		return "", errors.New("empty hwmon name")
	}

	// The "device" symlink points at the parent that owns the sensors; its
	// own "subsystem" symlink names the bus. Devices registered without a
	// parent (software monitors such as acpitz on some platforms) have no
	// device link and are "virtual" to libsensors.
	devPath, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
	if err != nil {
		return prefix + "-virtual-0", nil
	}
	devName := filepath.Base(devPath)

	subsys := ""
	if p, err := filepath.EvalSymlinks(filepath.Join(devPath, "subsystem")); err == nil {
		subsys = filepath.Base(p)
	}

	switch subsys {
	case "i2c":
		// sysfs name is "<bus>-<addr>" with a decimal bus and a hex address,
		// e.g. "20-0050"; libsensors prints the address in two hex digits.
		bus, addr, ok := splitPair(devName, 10, 16)
		if !ok {
			break
		}
		return fmt.Sprintf("%s-i2c-%d-%02x", prefix, bus, addr), nil
	case "spi":
		// sysfs name is "spi<bus>.<chipselect>", both decimal.
		bus, cs, ok := splitPair(strings.TrimPrefix(devName, "spi"), 10, 10)
		if !ok {
			break
		}
		return fmt.Sprintf("%s-spi-%d-%x", prefix, bus, cs), nil
	case "pci":
		// sysfs name is "domain:bus:slot.func"; libsensors folds slot and
		// function into one address, (slot << 3) | func, and prints neither
		// the domain nor the bus. k10temp at 0000:00:18.3 is therefore
		// "k10temp-pci-00c3".
		slot, fn, ok := pciSlotFunc(devName)
		if !ok {
			break
		}
		return fmt.Sprintf("%s-pci-%04x", prefix, (slot<<3)|fn), nil
	case "platform", "acpi", "of_node":
		// Platform devices are named "<driver>.<id>" and libsensors treats
		// them as ISA with the id as the address, so the two coretemp
		// devices of a dual-socket node are "coretemp-isa-0000" and
		// "coretemp-isa-0001".
		addr := 0
		if i := strings.LastIndex(devName, "."); i >= 0 {
			if v, err := strconv.ParseInt(devName[i+1:], 0, 32); err == nil {
				addr = int(v)
			}
		}
		return fmt.Sprintf("%s-isa-%04x", prefix, addr), nil
	}
	return prefix + "-virtual-0", nil
}

// splitPair parses "<a><sep><b>" where sep is "-" or "." in the given bases.
func splitPair(s string, baseA, baseB int) (int64, int64, bool) {
	i := strings.IndexAny(s, "-.")
	if i < 0 {
		return 0, 0, false
	}
	a, err := strconv.ParseInt(s[:i], baseA, 32)
	if err != nil {
		return 0, 0, false
	}
	b, err := strconv.ParseInt(s[i+1:], baseB, 32)
	if err != nil {
		return 0, 0, false
	}
	return a, b, true
}

// pciSlotFunc pulls slot and function out of a "domain:bus:slot.func" name.
func pciSlotFunc(name string) (int64, int64, bool) {
	i := strings.LastIndex(name, ":")
	if i < 0 {
		return 0, 0, false
	}
	return splitPair(name[i+1:], 16, 16)
}
