package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeChip makes a fake /sys/class/hwmon/<dir> with a name and files.
func writeChip(t *testing.T, root, dir, name string, files map[string]string) {
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	files["name"] = name
	for f, v := range files {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// LXC 100's layout, with the numbers shuffled to prove nothing depends on hwmonN.
func TestHwmonFindsChipsByName(t *testing.T) {
	root := t.TempDir()
	writeChip(t, root, "hwmon0", "acpitz", map[string]string{"temp1_input": "27800"})
	writeChip(t, root, "hwmon1", "nct6795", map[string]string{"temp1_input": "-128000", "temp1_label": "AUXTIN0", "temp2_input": "127000", "temp2_label": "AUXTIN1"})
	writeChip(t, root, "hwmon2", "coretemp", map[string]string{
		"temp1_input": "46600", "temp1_label": "Package id 0",
		"temp2_input": "44000", "temp2_label": "Core 0",
	})
	writeChip(t, root, "hwmon3", "r8169_0_500:00", map[string]string{"temp1_input": "52300"})
	writeChip(t, root, "hwmon4", "nvme", map[string]string{"temp1_input": "38850", "temp1_label": "Composite", "temp2_input": "60000", "temp2_label": "Sensor 1"})

	got := (&Hwmon{Root: root}).Read()
	if got.CPU == nil || *got.CPU != 47 {
		t.Errorf("CPU = %v, want 47 (the Package id 0 input, not Core 0)", got.CPU)
	}
	if got.NVMe == nil || *got.NVMe != 39 {
		t.Errorf("NVMe = %v, want 39 (Composite)", got.NVMe)
	}
	if got.NIC == nil || *got.NIC != 52 {
		t.Errorf("NIC = %v, want 52", got.NIC)
	}
}

// On 104 (or anywhere without these chips) every temperature is unknown, not zero.
func TestHwmonWithoutSensorsIsNull(t *testing.T) {
	for _, root := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if got := (&Hwmon{Root: root}).Read(); got.CPU != nil || got.NVMe != nil || got.NIC != nil {
			t.Errorf("%s: %+v, want all nil", root, got)
		}
	}
}

func TestHwmonIgnoresImplausibleReadings(t *testing.T) {
	root := t.TempDir()
	writeChip(t, root, "hwmon0", "coretemp", map[string]string{"temp1_input": "127000", "temp1_label": "Package id 0"})
	if got := (&Hwmon{Root: root}).Read(); got.CPU != nil {
		t.Errorf("CPU = %d, want nil for a 127 °C reading", *got.CPU)
	}
}
