package main

import "testing"

func TestNormalizeLanguage(t *testing.T) {
	cases := []struct{ input, want string }{
		{"eng", "eng"},
		{"en", "eng"},
		{"en-US", "eng"},
		{"EN", "eng"},
		{"ja", "jpn"},
		{"ja_JP", "jpn"},
		{"jpn", "jpn"},
		{"jap", "jpn"},
		{"ko", "kor"},
		{"ko-KR", "kor"},
		{"kor", "kor"},
		{"fr", "fre"},
		{"fra", "fre"},
		{"fre", "fre"},
		{"ru", "rus"},
		{"rus", "rus"},
		{"und", "und"},
		{"", "und"},
	}
	for _, tc := range cases {
		got := normalizeLanguage(tc.input)
		if got != tc.want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestGetIETFLanguage(t *testing.T) {
	if got := getIETFLanguage("eng"); got != "en" {
		t.Errorf("got %q, want \"en\"", got)
	}
	if got := getIETFLanguage("jpn"); got != "ja" {
		t.Errorf("got %q, want \"ja\"", got)
	}
	if got := getIETFLanguage("zzz"); got != "zzz" {
		t.Errorf("got %q, want passthrough \"zzz\"", got)
	}
}

func TestContainsKeyword(t *testing.T) {
	if !containsAny("Commentary Track", forbiddenKeywords) {
		t.Error("expected commentary to be forbidden")
	}
	if !containsAny("Signs & Songs", forcedKeywords) {
		t.Error("expected songs to be forced")
	}
	if containsAny("Japanese Dialogue", forbiddenKeywords) {
		t.Error("dialogue track should not be forbidden")
	}
}
