// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package hwmon

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Select returns the sensors a board entry's chip and sensor references
// resolve to, in discovery order.
//
// chipRef matches a chip either in full ("k10temp-pci-00c3", exactly one
// chip) or by its driver prefix ("k10temp", every chip of that driver). The
// prefix form is what makes a dual-socket node expressible: both sockets'
// sensors are selected and the guard watches the hottest of them, instead of
// the configuration having to name one socket and quietly ignore the other.
//
// sensorRef matches either a driver-exported label ("Tctl", case-insensitive)
// or an attribute base name ("temp1"). Left empty it selects the
// lowest-numbered temperature attribute of each matched chip, which is the
// first reading `sensors` prints for that chip.
//
// Selecting nothing is an error. A board whose sensor cannot be found must
// not silently degrade into a node with no thermal guard.
func Select(sensors []Sensor, chipRef, sensorRef string) ([]Sensor, error) {
	if chipRef == "" {
		return nil, fmt.Errorf("no chip configured")
	}

	var onChip []Sensor
	for _, s := range sensors {
		if s.Chip == chipRef || strings.HasPrefix(s.Chip, chipRef+"-") {
			onChip = append(onChip, s)
		}
	}
	if len(onChip) == 0 {
		return nil, fmt.Errorf("no hwmon chip matches %q", chipRef)
	}

	if sensorRef == "" {
		// Lowest-numbered attribute per hwmon device. Discover() has already
		// sorted by chip and then by index, so the first sensor seen for a
		// device is its lowest.
		//
		// The set is keyed by the device directory rather than by the
		// composed chip name, because two devices can compose to the same
		// name: the name is derived from the parent's bus address, and a
		// driver whose parent sits on a bus this package does not recognise
		// falls back to "<driver>-virtual-0" for every instance. Keying on
		// the name would keep one of them and drop the rest, leaving a node
		// whose second device is the hot one with no thermal guard on it and
		// nothing in the log to say so.
		var out []Sensor
		seen := map[string]bool{}
		for _, s := range onChip {
			dev := filepath.Dir(s.path)
			if !seen[dev] {
				seen[dev] = true
				out = append(out, s)
			}
		}
		return out, nil
	}

	var out []Sensor
	for _, s := range onChip {
		if strings.EqualFold(s.Label, sensorRef) || s.Attr == sensorRef {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("chip %q has no sensor %q (available: %s)",
			chipRef, sensorRef, describe(onChip))
	}
	return out, nil
}

func describe(sensors []Sensor) string {
	parts := make([]string, 0, len(sensors))
	for _, s := range sensors {
		if s.Label != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", s.Attr, s.Label))
			continue
		}
		parts = append(parts, s.Attr)
	}
	return strings.Join(parts, ", ")
}
