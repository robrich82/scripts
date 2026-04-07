package main

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.10.0", "1.9.0", 1},
		{"1.9.0", "1.10.0", -1},
		{"v1.2.3", "1.2.3", 0},
		{"v2.0.0", "v1.0.0", 1},
		// Unequal segment counts
		{"1.0", "1.0.0", 0},
		{"1.0.1", "1.0", 1},
		{"1.0", "1.0.1", -1},
		// Pre-release: release > pre-release
		{"1.0.0", "1.0.0-beta", 1},
		{"1.0.0-beta", "1.0.0", -1},
		// Pre-release comparison
		{"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-rc1", "1.0.0-rc1", 0},
		// Zero versions
		{"0.0.1", "0.0.2", -1},
		{"0.0.2", "0.0.1", 1},
		{"0.0.0", "0.0.0", 0},
	}
	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
