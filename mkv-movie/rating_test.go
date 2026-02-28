package main

import (
	"testing"
)

func TestMaxRating(t *testing.T) {
	tests := []struct {
		a, b NZRating
		want NZRating
	}{
		{RatingNone, RatingNone, RatingNone},
		{RatingNone, RatingG, RatingG},
		{RatingG, RatingNone, RatingG},
		{RatingG, RatingPG, RatingPG},
		{RatingPG, RatingM, RatingM},
		{RatingM, RatingRP13, RatingRP13},
		{RatingRP13, Rating13, Rating13},
		{Rating13, RatingR13, RatingR13},
		{RatingR13, RatingRP15, RatingRP15},
		{RatingRP15, Rating15, Rating15},
		{Rating15, RatingR15, RatingR15},
		{RatingR15, RatingRP16, RatingRP16},
		{RatingRP16, Rating16, Rating16},
		{Rating16, RatingR16, RatingR16},
		{RatingR16, RatingR, RatingR},
		{RatingR, RatingRP18, RatingRP18},
		{RatingRP18, Rating18, Rating18},
		{Rating18, RatingR18, RatingR18},
		{RatingPG, RatingG, RatingPG},
		{RatingR18, RatingG, RatingR18},
		{RatingM, RatingM, RatingM},
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
		input  string
		want   NZRating
		wantOK bool
	}{
		{"G", RatingG, true},
		{"PG", RatingPG, true},
		{"M", RatingM, true},
		{"16", Rating16, true},
		{"13", Rating13, true},
		{"15", Rating15, true},
		{"18", Rating18, true},
		{"R", RatingR, true},
		{"R16", RatingR16, true},
		{"r16", RatingR16, true},
		{"R 16", RatingR16, true},
		{"RP13", RatingRP13, true},
		{"rp13", RatingRP13, true},
		{"", RatingNone, false},
		{"NR", RatingNone, false},
		{"TV-MA", RatingNone, false},
		{"PG-13", RatingNone, false},
	}
	for _, tc := range tests {
		got, ok := ParseNZRating(tc.input)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ParseNZRating(%q) = (%q, %v); want (%q, %v)",
				tc.input, got, ok, tc.want, tc.wantOK)
		}
	}
}
