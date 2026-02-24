package main

import "testing"

func TestParseMediaInfoOutput_DateTagWins(t *testing.T) {
	best, header, err := parseMediaInfoOutput("2022-03-15|2022|UTC 2021-01-01 00:00:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "2022-03-15" {
		t.Errorf("bestDate = %q, want \"2022-03-15\"", best)
	}
	if header != "2021-01-01" {
		t.Errorf("headerDate = %q, want \"2021-01-01\"", header)
	}
}

func TestParseMediaInfoOutput_YearTagFallback(t *testing.T) {
	best, _, err := parseMediaInfoOutput("|2019|UTC 2021-01-01 00:00:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "2019" {
		t.Errorf("bestDate = %q, want \"2019\"", best)
	}
}

func TestParseMediaInfoOutput_HeaderFallback(t *testing.T) {
	best, header, err := parseMediaInfoOutput("||UTC 2016-09-12 04:35:11")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "2016-09-12" {
		t.Errorf("bestDate = %q, want \"2016-09-12\"", best)
	}
	if header != "2016-09-12" {
		t.Errorf("headerDate = %q, want \"2016-09-12\"", header)
	}
}

func TestParseMediaInfoOutput_AllEmpty(t *testing.T) {
	best, header, err := parseMediaInfoOutput("||")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "" || header != "" {
		t.Errorf("expected both empty, got best=%q header=%q", best, header)
	}
}

func TestParseMediaInfoOutput_NoSeparators(t *testing.T) {
	// Some mediainfo versions may output nothing at all.
	best, header, err := parseMediaInfoOutput("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "" || header != "" {
		t.Errorf("expected both empty, got best=%q header=%q", best, header)
	}
}

func TestParseMediaInfoOutput_YearOnly(t *testing.T) {
	// DATE tag is a bare year (4 chars).
	best, _, _ := parseMediaInfoOutput("2020||")
	if best != "2020" {
		t.Errorf("got %q, want \"2020\"", best)
	}
}
