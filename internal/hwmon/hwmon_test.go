// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package hwmon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// chip describes one hwmon device to build into a fake sysfs tree.
type chip struct {
	// dev is the sysfs path of the parent device below <root>/devices,
	// spelled exactly as the kernel spells it, because the chip name is
	// derived from its last component.
	dev string
	// subsystem is the bus the parent device sits on, or "" for a device
	// registered without one.
	subsystem string
	// name is the driver's hwmon "name" attribute.
	name string
	// temps maps an attribute index to its label and millidegree reading. A
	// reading of "" writes no input file at all, which is how a chip that
	// exports no temperature is expressed.
	temps map[int]temp
}

type temp struct {
	label   string
	reading string
}

// build writes the tree and returns its hwmon class directory.
//
// The symlinks are absolute, which sysfs' own are not; nothing in this
// package depends on them being relative, and it keeps the fixtures legible.
func build(t *testing.T, chips ...chip) string {
	t.Helper()
	root := t.TempDir()
	classDir := filepath.Join(root, "class", "hwmon")
	mkdir(t, classDir)

	for i, c := range chips {
		hwmonName := "hwmon" + strconv.Itoa(i)
		devDir := filepath.Join(root, "devices", filepath.FromSlash(c.dev))
		hwmonDir := filepath.Join(devDir, "hwmon", hwmonName)
		mkdir(t, hwmonDir)

		write(t, filepath.Join(hwmonDir, "name"), c.name)
		for idx, tp := range c.temps {
			base := "temp" + strconv.Itoa(idx)
			if tp.reading != "" {
				write(t, filepath.Join(hwmonDir, base+"_input"), tp.reading)
			}
			if tp.label != "" {
				write(t, filepath.Join(hwmonDir, base+"_label"), tp.label)
			}
		}

		if c.dev != "" {
			symlink(t, devDir, filepath.Join(hwmonDir, "device"))
		}
		if c.subsystem != "" {
			busDir := filepath.Join(root, "bus", c.subsystem)
			mkdir(t, busDir)
			symlink(t, busDir, filepath.Join(devDir, "subsystem"))
		}
		symlink(t, hwmonDir, filepath.Join(classDir, hwmonName))
	}
	return classDir
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestChipNames pins the composed chip names to what lm_sensors prints for
// the same hardware. The expectations for k10temp, coretemp and spd5118 are
// the names the deployed board table already uses, so a regression here would
// silently stop matching a node's configured sensor.
func TestChipNames(t *testing.T) {
	tests := []struct {
		desc      string
		dev       string
		subsystem string
		name      string
		want      string
	}{
		{
			desc:      "AMD Zen CPU temperature, PCI function 00:18.3",
			dev:       "pci0000:00/0000:00:18.3",
			subsystem: "pci",
			name:      "k10temp",
			want:      "k10temp-pci-00c3",
		},
		{
			desc:      "AMD Zen CPU temperature, PCI function 00:19.3",
			dev:       "pci0000:00/0000:00:19.3",
			subsystem: "pci",
			name:      "k10temp",
			want:      "k10temp-pci-00cb",
		},
		{
			desc:      "Intel package temperature, first socket",
			dev:       "platform/coretemp.0",
			subsystem: "platform",
			name:      "coretemp",
			want:      "coretemp-isa-0000",
		},
		{
			desc:      "Intel package temperature, second socket",
			dev:       "platform/coretemp.1",
			subsystem: "platform",
			name:      "coretemp",
			want:      "coretemp-isa-0001",
		},
		{
			desc:      "DDR5 SPD hub temperature on I2C bus 20, address 0x50",
			dev:       "pci0000:00/0000:00:14.0/i2c-20/20-0050",
			subsystem: "i2c",
			name:      "spd5118",
			want:      "spd5118-i2c-20-50",
		},
		{
			desc:      "NIC temperature, PCI function 83:00.1",
			dev:       "pci0000:80/0000:83:00.1",
			subsystem: "pci",
			name:      "i350bb",
			want:      "i350bb-pci-0001",
		},
		{
			desc:      "ACPI thermal zone counts as ISA",
			dev:       "LNXSYSTM:00/LNXTHERM:00",
			subsystem: "acpi",
			name:      "acpitz",
			want:      "acpitz-isa-0000",
		},
		{
			desc:      "device without a subsystem is virtual",
			dev:       "virtual/thermal/thermal_zone0",
			subsystem: "",
			name:      "soc_dts",
			want:      "soc_dts-virtual-0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := build(t, chip{
				dev:       tc.dev,
				subsystem: tc.subsystem,
				name:      tc.name,
				temps:     map[int]temp{1: {reading: "45000"}},
			})
			sensors, err := Discover(dir)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sensors) != 1 {
				t.Fatalf("got %d sensors, want 1: %+v", len(sensors), sensors)
			}
			if sensors[0].Chip != tc.want {
				t.Errorf("chip name = %q, want %q", sensors[0].Chip, tc.want)
			}
		})
	}
}

// TestDiscoverSkipsChipsWithoutTemperatures keeps fan-only and voltage-only
// hwmon devices out of the listing; the hwmon class carries those too.
func TestDiscoverSkipsChipsWithoutTemperatures(t *testing.T) {
	dir := build(t,
		chip{dev: "platform/coretemp.0", subsystem: "platform", name: "coretemp",
			temps: map[int]temp{1: {label: "Package id 0", reading: "42000"}}},
		chip{dev: "platform/it87.552", subsystem: "platform", name: "it8728",
			temps: map[int]temp{}},
	)
	sensors, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sensors) != 1 || sensors[0].Chip != "coretemp-isa-0000" {
		t.Fatalf("got %+v, want only the coretemp sensor", sensors)
	}
	if sensors[0].Label != "Package id 0" {
		t.Errorf("label = %q, want %q", sensors[0].Label, "Package id 0")
	}
}

// TestDiscoverOrder fixes the ordering Select relies on when it picks the
// lowest-numbered attribute of a chip: chip name, then attribute index. The
// fixture lists temp10 before temp2 to prove the index is compared as a
// number rather than as a string.
func TestDiscoverOrder(t *testing.T) {
	dir := build(t,
		chip{dev: "pci0000:00/0000:00:19.3", subsystem: "pci", name: "k10temp",
			temps: map[int]temp{1: {label: "Tctl", reading: "60000"}}},
		chip{dev: "pci0000:00/0000:00:18.3", subsystem: "pci", name: "k10temp",
			temps: map[int]temp{
				10: {label: "Tccd8", reading: "55000"},
				2:  {label: "Tccd1", reading: "52000"},
				1:  {label: "Tctl", reading: "50000"},
			}},
	)
	sensors, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var got []string
	for _, s := range sensors {
		got = append(got, s.Name())
	}
	want := []string{
		"k10temp-pci-00c3/Tctl",
		"k10temp-pci-00c3/Tccd1",
		"k10temp-pci-00c3/Tccd8",
		"k10temp-pci-00cb/Tctl",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSensorRead(t *testing.T) {
	dir := build(t, chip{dev: "platform/coretemp.0", subsystem: "platform", name: "coretemp",
		temps: map[int]temp{
			1: {label: "Package id 0", reading: "84999"},
			2: {label: "Core 0", reading: "not a number"},
		}})
	sensors, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got, err := sensors[0].Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The reading is kept in millidegrees so that a comparison against an
	// 85 C limit sees 84.999 and not a rounded 85.
	if got != 84999 {
		t.Errorf("reading = %d, want 84999", got)
	}
	if got.String() != "84.999C" {
		t.Errorf("String() = %q, want %q", got.String(), "84.999C")
	}

	if _, err := sensors[1].Read(); err == nil {
		t.Error("reading a non-numeric attribute succeeded, want an error")
	}

	// A sensor whose attribute disappears with its device must report an
	// error rather than a zero reading.
	if err := os.Remove(sensors[0].path); err != nil {
		t.Fatal(err)
	}
	if _, err := sensors[0].Read(); err == nil {
		t.Error("reading a removed attribute succeeded, want an error")
	}
}

func TestSelect(t *testing.T) {
	dir := build(t,
		chip{dev: "pci0000:00/0000:00:18.3", subsystem: "pci", name: "k10temp",
			temps: map[int]temp{
				1: {label: "Tctl", reading: "50000"},
				2: {label: "Tccd1", reading: "48000"},
			}},
		chip{dev: "pci0000:00/0000:00:19.3", subsystem: "pci", name: "k10temp",
			temps: map[int]temp{1: {label: "Tctl", reading: "61000"}}},
		chip{dev: "pci0000:00/0000:00:14.0/i2c-20/20-0050", subsystem: "i2c", name: "spd5118",
			temps: map[int]temp{1: {reading: "39000"}}},
	)
	sensors, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	tests := []struct {
		desc      string
		chipRef   string
		sensorRef string
		want      []string
		wantErr   bool
	}{
		{
			desc:      "a full chip name selects one socket",
			chipRef:   "k10temp-pci-00c3",
			sensorRef: "Tctl",
			want:      []string{"k10temp-pci-00c3/Tctl"},
		},
		{
			desc:      "a driver prefix selects both sockets",
			chipRef:   "k10temp",
			sensorRef: "Tctl",
			want:      []string{"k10temp-pci-00c3/Tctl", "k10temp-pci-00cb/Tctl"},
		},
		{
			desc:      "a label matches regardless of case",
			chipRef:   "k10temp-pci-00c3",
			sensorRef: "tCTL",
			want:      []string{"k10temp-pci-00c3/Tctl"},
		},
		{
			desc:      "an attribute name matches too",
			chipRef:   "k10temp-pci-00c3",
			sensorRef: "temp2",
			want:      []string{"k10temp-pci-00c3/Tccd1"},
		},
		{
			desc:    "no sensor means the lowest attribute of each matched chip",
			chipRef: "k10temp",
			want:    []string{"k10temp-pci-00c3/Tctl", "k10temp-pci-00cb/Tctl"},
		},
		{
			desc:    "an unlabelled chip is named by its attribute",
			chipRef: "spd5118",
			want:    []string{"spd5118-i2c-20-50/temp1"},
		},
		{
			desc:    "an unknown chip is an error, never an empty selection",
			chipRef: "nct6798",
			wantErr: true,
		},
		{
			desc:      "an unknown sensor on a known chip is an error",
			chipRef:   "k10temp-pci-00c3",
			sensorRef: "Tdie",
			wantErr:   true,
		},
		{
			desc:    "an empty chip reference is an error",
			chipRef: "",
			wantErr: true,
		},
		{
			desc:      "a prefix must not match a longer driver name",
			chipRef:   "k10",
			sensorRef: "Tctl",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := Select(sensors, tc.chipRef, tc.sensorRef)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Select(%q, %q) succeeded with %+v, want an error",
						tc.chipRef, tc.sensorRef, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Select(%q, %q): %v", tc.chipRef, tc.sensorRef, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("selected %d sensors, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i].Name() != tc.want[i] {
					t.Errorf("sensor %d = %q, want %q", i, got[i].Name(), tc.want[i])
				}
			}
		})
	}
}
