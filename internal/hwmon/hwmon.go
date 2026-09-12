// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package hwmon reads temperatures straight from the kernel's hwmon class in
// sysfs, which is the same place the lm_sensors userspace reads them from.
//
// Chip names are composed the way libsensors composes them, so that the names
// in this package's configuration are the names `sensors` prints for the
// buses a server mainboard exposes its temperatures on: PCI, I2C, SPI, ACPI
// and the platform devices libsensors calls ISA. Reproducing that naming is a
// convenience, not a correctness requirement, and it is not guaranteed for
// every bus the kernel has: the program's --list mode prints the names it
// computed on the node itself, and that is what an operator should paste into
// the configuration.
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

// maxDeviceLinks bounds the walk up the "device" links. Real chains are two
// or three links long; the bound exists only so that a malformed tree cannot
// spin here.
const maxDeviceLinks = 8

// chipName composes the libsensors-style name of the hwmon device in dir.
//
// libsensors builds it from the driver's "name" attribute plus the bus type
// and address of the device that owns the sensors (lib/sysfs.c, lib/access.c).
// The bus type comes from that device's subsystem, the address from its sysfs
// name.
func chipName(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "name"))
	if err != nil {
		return "", err
	}
	prefix := strings.TrimSpace(string(b))
	if prefix == "" {
		return "", errors.New("empty hwmon name")
	}

	// The "device" symlink points at the parent that owns the sensors.
	// Devices registered without a parent (software monitors such as some
	// platforms' thermal zones) have no device link and are "virtual" to
	// libsensors.
	devPath, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
	if err != nil {
		return prefix + "-virtual-0", nil
	}

	// That parent does not always name a bus. A driver may register its
	// hwmon device below a class device — nvme below /sys/class/nvme, a
	// wireless PHY below ieee80211, an ACPI thermal zone below
	// /sys/class/thermal — and the class device's own subsystem is the class,
	// not a bus. Each of them carries a "device" link pointing further up, so
	// the chain is followed until it reaches a bus. Stopping at the first
	// link would name every such device "<driver>-virtual-0", and since those
	// names collide, a node with two of them would have all but one dropped
	// from a prefix selection.
	for i := 0; i < maxDeviceLinks; i++ {
		if suffix, ok := busSuffix(devPath); ok {
			return prefix + suffix, nil
		}
		next, err := filepath.EvalSymlinks(filepath.Join(devPath, "device"))
		if err != nil || next == devPath {
			break
		}
		devPath = next
	}
	return prefix + "-virtual-0", nil
}

// busSuffix returns the "-<bus>-<address>" half of a libsensors chip name for
// the device at devPath, or false when its subsystem is not a bus this
// package knows how to address.
func busSuffix(devPath string) (string, bool) {
	link, err := filepath.EvalSymlinks(filepath.Join(devPath, "subsystem"))
	if err != nil {
		return "", false
	}
	devName := filepath.Base(devPath)

	switch filepath.Base(link) {
	case "i2c":
		// sysfs name is "<bus>-<addr>" with a decimal bus and a hex address,
		// e.g. "20-0050"; libsensors prints the address in two hex digits.
		bus, addr, ok := splitPair(devName, 10, 16)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("-i2c-%d-%02x", bus, addr), true
	case "spi":
		// sysfs name is "spi<bus>.<chipselect>", both decimal.
		bus, cs, ok := splitPair(strings.TrimPrefix(devName, "spi"), 10, 10)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("-spi-%d-%x", bus, cs), true
	case "pci":
		addr, ok := pciAddress(devName)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("-pci-%04x", addr), true
	case "acpi":
		// ACPI is its own bus type to libsensors, with a single address: an
		// ACPI namespace path is not something it can fold into a number.
		return "-acpi-0", true
	case "platform", "of_node":
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
		return fmt.Sprintf("-isa-%04x", addr), true
	}
	return "", false
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

// pciAddress folds a "domain:bus:slot.func" device name into the address
// libsensors prints: (bus << 8) | (slot << 3) | func, all hex. The domain is
// not part of it, so k10temp at 0000:00:18.3 is 0x00c3 and a NIC at
// 0000:83:00.1 is 0x8301.
func pciAddress(name string) (int64, bool) {
	parts := strings.Split(name, ":")
	if len(parts) < 2 {
		return 0, false
	}
	bus, err := strconv.ParseInt(parts[len(parts)-2], 16, 32)
	if err != nil {
		return 0, false
	}
	slot, fn, ok := splitPair(parts[len(parts)-1], 16, 16)
	if !ok {
		return 0, false
	}
	return (bus << 8) | (slot << 3) | fn, true
}
