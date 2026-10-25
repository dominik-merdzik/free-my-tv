package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Hwmon reads temperatures from sysfs. /sys/class/hwmon isn't namespaced in an LXC container, so a container in a
// guest on pve (the aggregator, in Docker on LXC 100) sees pve's own sensors: that's the only reason these numbers
// belong to pve. Moved to a guest on pve2, they would be pve2's, so HWMON_NODE has to follow it.
//
// The hwmonN numbers change between reboots, so each chip is found by its name every time.
type Hwmon struct {
	Root string // /sys/class/hwmon; a temp dir in tests
}

func (h *Hwmon) Read() Temps {
	var t Temps
	entries, err := os.ReadDir(h.Root)
	if err != nil {
		return t // not on pve (or no sysfs): unknown, shown as "—"
	}
	for _, e := range entries {
		dir := filepath.Join(h.Root, e.Name())
		name := readTrim(filepath.Join(dir, "name"))
		switch {
		case name == "coretemp" && t.CPU == nil:
			t.CPU = labelled(dir, "Package id 0")
		case name == "nvme" && t.NVMe == nil:
			t.NVMe = labelled(dir, "Composite") // the drive's overall temperature
		case strings.HasPrefix(name, "r8169") && t.NIC == nil:
			t.NIC = firstTemp(dir)
			// nct6795 (the board's Super I/O) is skipped on purpose: its AUXTIN inputs float and read −128 / +127 °C.
		}
	}
	return t
}

// labelled finds the temp*_input whose temp*_label is label.
func labelled(dir, label string) *int {
	labels, _ := filepath.Glob(filepath.Join(dir, "temp*_label"))
	for _, l := range labels {
		if readTrim(l) == label {
			return milli(strings.TrimSuffix(l, "_label") + "_input")
		}
	}
	return nil
}

// firstTemp is for chips with one unlabelled sensor (the NIC).
func firstTemp(dir string) *int {
	inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
	if len(inputs) == 0 {
		return nil
	}
	return milli(inputs[0])
}

// milli reads a millidegree value and returns whole °C; nil for anything implausible.
func milli(path string) *int {
	v, err := strconv.Atoi(readTrim(path))
	if err != nil {
		return nil
	}
	c := (v + 500) / 1000
	if c <= -40 || c >= 125 {
		return nil
	}
	return intPtr(c)
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
