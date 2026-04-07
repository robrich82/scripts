package main

import "testing"

func TestApplyVersionRegex(t *testing.T) {
	tests := []struct {
		ver     string
		pattern string
		want    string
		wantErr bool
	}{
		// Full match (no capture group)
		{"version 1.2.3", `\d+\.\d+\.\d+`, "1.2.3", false},
		// Capture group returns group 1
		{"app v2.0.1 (build 5)", `v(\d+\.\d+\.\d+)`, "2.0.1", false},
		// No match
		{"no version here", `\d+\.\d+`, "", true},
		// Invalid regex
		{"anything", `[invalid`, "", true},
		// Trims whitespace
		{" 3.1.4 ", `\s*(\d+\.\d+\.\d+)\s*`, "3.1.4", false},
	}
	for _, tt := range tests {
		got, err := applyVersionRegex(tt.ver, tt.pattern)
		if (err != nil) != tt.wantErr {
			t.Errorf("applyVersionRegex(%q, %q) error = %v, wantErr %v", tt.ver, tt.pattern, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("applyVersionRegex(%q, %q) = %q, want %q", tt.ver, tt.pattern, got, tt.want)
		}
	}
}

func TestApplyVersionRewrite(t *testing.T) {
	tests := []struct {
		ver     string
		rewrite []string
		want    string
		wantErr bool
	}{
		// Wrong length — no-op
		{"1.2.3", nil, "1.2.3", false},
		{"1.2.3", []string{"only-one"}, "1.2.3", false},
		{"1.2.3", []string{"a", "b", "c"}, "1.2.3", false},
		// Simple substitution
		{"release-1.2.3", []string{`^release-`, ""}, "1.2.3", false},
		// Capture group in replacement
		{"v1.2.3-stable", []string{`^v(\d+\.\d+\.\d+).*$`, "$1"}, "1.2.3", false},
		// Invalid regex
		{"1.2.3", []string{`[invalid`, "x"}, "", true},
		// No match — ReplaceAllString returns original string unchanged
		{"1.2.3", []string{`^abc`, ""}, "1.2.3", false},
	}
	for _, tt := range tests {
		got, err := applyVersionRewrite(tt.ver, tt.rewrite)
		if (err != nil) != tt.wantErr {
			t.Errorf("applyVersionRewrite(%q, %v) error = %v, wantErr %v", tt.ver, tt.rewrite, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("applyVersionRewrite(%q, %v) = %q, want %q", tt.ver, tt.rewrite, got, tt.want)
		}
	}
}
