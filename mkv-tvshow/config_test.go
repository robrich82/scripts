package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanMetadataTitle_StripExtension(t *testing.T) {
	got := cleanMetadataTitle("Show Name - S01E01.mkv")
	want := "Show Name - S01E01"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCleanMetadataTitle_NoExtension(t *testing.T) {
	got := cleanMetadataTitle("Show Name")
	want := "Show Name"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCleanMetadataTitle_ReplaceColon(t *testing.T) {
	got := cleanMetadataTitle("Title꞉ Subtitle.mkv")
	want := "Title: Subtitle"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCleanMetadataTitle_AllReplacements(t *testing.T) {
	// Each lookalike char mapped to its ASCII equivalent
	cases := []struct{ input, want string }{
		{"A꞉B", "A:B"},
		{"A╱B", "A/B"},
		{"A╲B", `A\B`},
		{"A│B", "A|B"},
		{"A＊B", "A*B"},
		{"A？B", "A?B"},
		{"A''B", `A"B`},
		{"A＜B", "A<B"},
		{"A＞B", "A>B"},
	}
	for _, tc := range cases {
		got := cleanMetadataTitle(tc.input)
		if got != tc.want {
			t.Errorf("cleanMetadataTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCleanSearchQuery_StripYearSuffix(t *testing.T) {
	got := cleanSearchQuery("Attack on Titan (2013)")
	want := "Attack on Titan"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCleanSearchQuery_StripBrackets(t *testing.T) {
	got := cleanSearchQuery("【Some Show】")
	want := "Some Show"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCleanSearchQuery_CombinedTransforms(t *testing.T) {
	got := cleanSearchQuery("【Show꞉ Name】 (2020)")
	want := "Show: Name"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadManualData_MissingFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	data, err := LoadManualData(filepath.Join(dir, "manual_data.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty map, got %v", data)
	}
}

func TestLoadManualData_ParsesOverridesAndMappings(t *testing.T) {
	input := map[string]ManualEntry{
		"Test Show": {
			Overrides: map[string]SeasonOverride{
				"S02": {ID: "999", MapSeason: 1},
			},
			Mapping: map[string]EpisodePair{
				"S01E01": {1, 2},
			},
		},
	}
	raw, _ := json.Marshal(input)
	dir := t.TempDir()
	path := filepath.Join(dir, "manual_data.json")
	os.WriteFile(path, raw, 0644)

	data, err := LoadManualData(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := data["Test Show"]
	if !ok {
		t.Fatal("expected entry for 'Test Show'")
	}
	ovr, ok := entry.Overrides["S02"]
	if !ok || ovr.ID != "999" || ovr.MapSeason != 1 {
		t.Errorf("bad override: %+v", ovr)
	}
	pair, ok := entry.Mapping["S01E01"]
	if !ok || pair[0] != 1 || pair[1] != 2 {
		t.Errorf("bad mapping: %v", pair)
	}
}

func TestLoadManualData_ParsesExtras(t *testing.T) {
	input := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S02 - Drama CD": {Date: "2022-04-01", Season: 2},
			},
		},
	}
	raw, _ := json.Marshal(input)
	dir := t.TempDir()
	path := filepath.Join(dir, "manual_data.json")
	os.WriteFile(path, raw, 0644)

	data, err := LoadManualData(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := data["My Show"]
	if !ok {
		t.Fatal("expected entry for 'My Show'")
	}
	extra, ok := entry.Extras["My Show - S02 - Drama CD"]
	if !ok {
		t.Fatal("expected extras entry for 'My Show - S02 - Drama CD'")
	}
	if extra.Date != "2022-04-01" {
		t.Errorf("got date %q, want %q", extra.Date, "2022-04-01")
	}
	if extra.Season != 2 {
		t.Errorf("got season %d, want 2", extra.Season)
	}
}

func TestLoadManualData_InvalidJSON_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manual_data.json")
	os.WriteFile(path, []byte("not json"), 0644)
	_, err := LoadManualData(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadManualData_ParsesExtrasWithEpisodeRef(t *testing.T) {
	input := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S01 NCOP": {Season: 1, Episode: 1},
			},
		},
	}
	raw, _ := json.Marshal(input)
	dir := t.TempDir()
	path := filepath.Join(dir, "manual_data.json")
	os.WriteFile(path, raw, 0644)

	data, err := LoadManualData(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	extra, ok := data["My Show"].Extras["My Show - S01 NCOP"]
	if !ok {
		t.Fatal("expected extras entry")
	}
	if extra.Season != 1 || extra.Episode != 1 || extra.Date != "" {
		t.Errorf("unexpected extra: %+v", extra)
	}
}

func TestExtractOpEdSuffix(t *testing.T) {
	cases := []struct {
		stem       string
		wantBase   string
		wantSuffix string
	}{
		{"Show S01 OP", "Show S01", "OP"},
		{"Show S01 OP1", "Show S01", "OP1"},
		{"Show S02 ED", "Show S02", "ED"},
		{"Show S02 ED2", "Show S02", "ED2"},
		{"Show S01 NCOP", "Show S01", "NCOP"},
		{"Show S02 NCED1", "Show S02", "NCED1"},
		{"Show S01 ncop2", "Show S01", "NCOP2"}, // case-insensitive, uppercased
		{"Show S01 - Drama CD", "Show S01 - Drama CD", ""},
		{"Show Name", "Show Name", ""},
		{"OP", "OP", ""},    // no leading space
		{"S01E01", "S01E01", ""},
		// Hyphen separator (actual filename convention)
		{"Show - S01E03 - A Friend-NCOP", "Show - S01E03 - A Friend", "NCOP"},
		{"Show - S01 - Ending 1 (Episodes 01-11)-NCED", "Show - S01 - Ending 1 (Episodes 01-11)", "NCED"},
		{"Show - S02 - Opening 2-NCOP2", "Show - S02 - Opening 2", "NCOP2"},
		{"Show - S01 - Ending 2-NCED2", "Show - S01 - Ending 2", "NCED2"},
	}
	for _, tc := range cases {
		base, suffix := extractOpEdSuffix(tc.stem)
		if base != tc.wantBase || suffix != tc.wantSuffix {
			t.Errorf("extractOpEdSuffix(%q) = (%q, %q), want (%q, %q)",
				tc.stem, base, suffix, tc.wantBase, tc.wantSuffix)
		}
	}
}
