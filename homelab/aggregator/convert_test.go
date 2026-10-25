package main

import "testing"

func TestPct(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want int
	}{{0, 0}, {0.054, 5}, {0.125, 13}, {0.995, 100}, {1.7, 100}, {-0.1, 0}} {
		if got := pct(c.in); got != c.want {
			t.Errorf("pct(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestRatioPct(t *testing.T) {
	if got := ratioPct(50, 200); got != 25 {
		t.Errorf("ratioPct(50, 200) = %d, want 25", got)
	}
	if got := ratioPct(5, 0); got != 0 {
		t.Errorf("ratioPct with no total = %d, want 0", got)
	}
}

func TestGB(t *testing.T) {
	for _, c := range []struct {
		bytes float64
		want  float64
	}{{0, 0}, {gib, 1}, {16.75 * gib, 16.8}, {33_604_000_000, 31.3}} {
		if got := gb(c.bytes); got != c.want {
			t.Errorf("gb(%v) = %v, want %v", c.bytes, got, c.want)
		}
	}
}

func TestWeightedCPU(t *testing.T) {
	// 4 cores flat out and 16 idle: 4 of 20 cores busy, 20%, where a plain average would say 50%.
	if got := weightedCPU([]float64{1, 0}, []int{4, 16}); got != 20 {
		t.Errorf("weightedCPU = %d, want 20", got)
	}
	if got := weightedCPU(nil, nil); got != 0 {
		t.Errorf("weightedCPU with no nodes = %d, want 0", got)
	}
}
