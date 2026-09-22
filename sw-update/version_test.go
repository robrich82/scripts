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

func TestHashPrefixMatch(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"e76a35ec9", "e76a35ec95", true},  // local shorter than remote
		{"e76a35ec95", "e76a35ec9", true},  // remote shorter than local
		{"E76A35EC9", "e76a35ec95", true},  // case-insensitive
		{"e76a35ec9", "e76a35ec9", true},   // identical
		{"e76a35ec9", "f00d35ec95", false}, // different commit
		{"", "e76a35ec95", false},          // empty never matches
		{"e76a35ec95", "", false},
	}
	for _, tt := range tests {
		got := HashPrefixMatch(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("HashPrefixMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
