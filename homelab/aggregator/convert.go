package main

import "math"

// GB here is a GiB (1024³ bytes), the unit the Proxmox web UI shows, so the TV and the UI agree.
const gib = 1024 * 1024 * 1024

// pct turns a 0–1 fraction (Proxmox's cpu) into a whole percent, clamped to 0–100.
func pct(fraction float64) int {
	if math.IsNaN(fraction) || fraction < 0 {
		return 0
	}
	if fraction > 1 {
		return 100
	}
	return int(math.Round(fraction * 100))
}

// ratioPct is used/total as a whole percent; 0 when total is unknown.
func ratioPct(used, total float64) int {
	if total <= 0 {
		return 0
	}
	return pct(used / total)
}

// gb turns bytes into GiB with one decimal.
func gb(bytes float64) float64 {
	return math.Round(bytes/gib*10) / 10
}

// weightedCPU averages the nodes' CPU fractions by core count: a 4-core node at 100% next to a 16-core node at 0%
// is 20% busy overall, not 50%.
func weightedCPU(fractions []float64, cores []int) int {
	var busy, total float64
	for i, f := range fractions {
		busy += f * float64(cores[i])
		total += float64(cores[i])
	}
	if total == 0 {
		return 0
	}
	return pct(busy / total)
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func int64Ptr(v int64) *int64     { return &v }
func stringPtr(v string) *string  { return &v }
