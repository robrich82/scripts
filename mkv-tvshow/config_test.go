package main

import "testing"

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

func TestManualData_PantyStocking_SeasonOverride(t *testing.T) {
	entry, ok := manualData["Panty & Stocking with Garterbelt"]
	if !ok {
		t.Fatal("expected entry for Panty & Stocking")
	}
	ovr, ok := entry.Overrides["S02"]
	if !ok {
		t.Fatal("expected S02 override")
	}
	if ovr.ID != "246862" {
		t.Errorf("got ID %q, want %q", ovr.ID, "246862")
	}
	if ovr.MapSeason != 1 {
		t.Errorf("got MapSeason %d, want 1", ovr.MapSeason)
	}
}

func TestManualData_FullMetalPanic_EpisodeMapping(t *testing.T) {
	entry, ok := manualData["Full Metal Panic!"]
	if !ok {
		t.Fatal("expected entry for Full Metal Panic!")
	}
	pair, ok := entry.Mapping["S02E01"]
	if !ok {
		t.Fatal("expected mapping for S02E01")
	}
	if pair[0] != 0 || pair[1] != 1 {
		t.Errorf("got %v, want [0 1]", pair)
	}
}
