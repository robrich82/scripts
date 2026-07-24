package main

import (
	"path/filepath"
	"testing"
)

func TestManualDataTool(t *testing.T) {
	cases := map[string]string{
		"tv":    "mkv-tvshow",
		"TV":    "mkv-tvshow",
		"movie": "mkv-movie",
		"":      "",
		"music": "",
	}
	for libType, want := range cases {
		if got := manualDataTool(libType); got != want {
			t.Errorf("manualDataTool(%q) = %q; want %q", libType, got, want)
		}
	}
}

func TestParseAccountLangs_NormalisesAndDedupes(t *testing.T) {
	// Entries carry unrelated fields too — they must be ignored, not rejected.
	data := []byte(`{
		"Subbed Show": {"deband": true, "overrides": {"S02": {"id": "1"}}},
		"Dubbed Show": {"language": "ja", "alt-language": "en"},
		"All Same":    {"language": "ja", "alt-language": "ja", "alt2-language": "jpn"},
		"Alt Only":    {"alt2-language": "fr"}
	}`)
	got := parseAccountLangs(data)

	if _, ok := got["Subbed Show"]; ok {
		t.Error("entry with no language keys should be omitted")
	}
	if want := []string{"jpn", "eng"}; !stringsEqual(got["Dubbed Show"], want) {
		t.Errorf("Dubbed Show = %v; want %v", got["Dubbed Show"], want)
	}
	if want := []string{"jpn"}; !stringsEqual(got["All Same"], want) {
		t.Errorf("All Same = %v; want %v (deduped after normalisation)", got["All Same"], want)
	}
	if want := []string{"fre"}; !stringsEqual(got["Alt Only"], want) {
		t.Errorf("Alt Only = %v; want %v", got["Alt Only"], want)
	}
}

func TestParseAccountLangs_MalformedReturnsNil(t *testing.T) {
	if got := parseAccountLangs([]byte("not json")); got != nil {
		t.Errorf("malformed JSON should yield nil, got %v", got)
	}
	if got := parseAccountLangs([]byte(`{}`)); got != nil {
		t.Errorf("empty manual data should yield nil, got %v", got)
	}
}

func TestAccountLangs_ForFile(t *testing.T) {
	root := filepath.FromSlash("G:/Media")
	langs := AccountLangs{
		"Dubbed Show":   {"jpn", "eng"},
		"Coco (2017)":   {"spa"},
		"Nested (2019)": {"fre"},
	}

	cases := []struct {
		name string
		path string
		want []string
	}{
		{
			"tv episode matches the show folder",
			filepath.FromSlash("G:/Media/Dubbed Show/Season 01/Dubbed Show - S01E01.mkv"),
			[]string{"jpn", "eng"},
		},
		{
			"flat movie matches the filename stem",
			filepath.FromSlash("G:/Media/Coco (2017).mkv"),
			[]string{"spa"},
		},
		{
			"movie in its own folder matches the folder name",
			filepath.FromSlash("G:/Media/Nested (2019)/Nested (2019) - Bluray.mkv"),
			[]string{"fre"},
		},
		{
			"unconfigured title has no languages",
			filepath.FromSlash("G:/Media/Other Show/Other Show - S01E01.mkv"),
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := langs.forFile(tc.path, root); !stringsEqual(got, tc.want) {
				t.Errorf("forFile = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestAccountLangs_ForFile_NilMap(t *testing.T) {
	var langs AccountLangs
	if got := langs.forFile(filepath.FromSlash("G:/Media/Show/ep.mkv"), filepath.FromSlash("G:/Media")); got != nil {
		t.Errorf("nil AccountLangs should yield nil, got %v", got)
	}
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
