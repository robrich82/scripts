package main

import (
	"testing"
)

func TestMaxRating(t *testing.T) {
	tests := []struct {
		a, b NZRating
		want NZRating
	}{
		// RatingNone propagation
		{RatingNone, RatingNone, RatingNone},
		{RatingNone, RatingG, RatingG},
		{RatingG, RatingNone, RatingG},
		// Adjacent pairs — higher should win
		{RatingG, RatingPG, RatingPG},
		{RatingPG, RatingM, RatingM},
		{RatingM, RatingRP13, RatingRP13},
		{RatingRP13, RatingR13, RatingR13},
		{RatingR13, RatingRP15, RatingRP15},
		{RatingRP15, RatingR15, RatingR15},
		{RatingR15, RatingRP16, RatingRP16},
		{RatingRP16, RatingR16, RatingR16},
		{RatingR16, RatingRP18, RatingRP18},
		{RatingRP18, RatingR18, RatingR18},
		// Symmetry
		{RatingPG, RatingG, RatingPG},
		{RatingR18, RatingG, RatingR18},
		// Same
		{RatingM, RatingM, RatingM},
		{RatingR16, RatingR16, RatingR16},
	}
	for _, tc := range tests {
		got := MaxRating(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("MaxRating(%q, %q) = %q; want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseNZRating(t *testing.T) {
	tests := []struct {
		input   string
		want    NZRating
		wantOK  bool
	}{
		// Canonical forms
		{"G", RatingG, true},
		{"PG", RatingPG, true},
		{"M", RatingM, true},
		{"RP13", RatingRP13, true},
		{"R13", RatingR13, true},
		{"RP15", RatingRP15, true},
		{"R15", RatingR15, true},
		{"RP16", RatingRP16, true},
		{"R16", RatingR16, true},
		{"RP18", RatingRP18, true},
		{"R18", RatingR18, true},
		// Lowercase
		{"g", RatingG, true},
		{"pg", RatingPG, true},
		{"m", RatingM, true},
		{"r16", RatingR16, true},
		{"rp13", RatingRP13, true},
		// Mixed case
		{"R16", RatingR16, true},
		{"Pg", RatingPG, true},
		// Spaces stripped
		{"R 16", RatingR16, true},
		{"RP 18", RatingRP18, true},
		{"R 13", RatingR13, true},
		// Unknown / empty
		{"", RatingNone, false},
		{"NR", RatingNone, false},
		{"TV-MA", RatingNone, false},
		{"X", RatingNone, false},
		{"r17", RatingNone, false},
	}
	for _, tc := range tests {
		got, ok := ParseNZRating(tc.input)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ParseNZRating(%q) = (%q, %v); want (%q, %v)",
				tc.input, got, ok, tc.want, tc.wantOK)
		}
	}
}
