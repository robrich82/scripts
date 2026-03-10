package main

import (
	"path/filepath"
	"testing"
)

func TestDetectLibrary_NoMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/other/path", cfg)
	if lib != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", lib)
	}
}

func TestDetectLibrary_ExactMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV", cfg)
	if lib == nil {
		t.Fatal("expected match, got nil")
	}
	if lib.Name != "TV" {
		t.Errorf("expected TV, got %q", lib.Name)
	}
}

func TestDetectLibrary_SubdirMatch(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV/ShowName/Season 1", cfg)
	if lib == nil || lib.Name != "TV" {
		t.Errorf("expected TV library for subdir, got %v", lib)
	}
}

func TestDetectLibrary_MostSpecificWins(t *testing.T) {
	cfg := &GlobalConfig{
		Libraries: []Library{
			{Name: "Media", Path: "/media"},
			{Name: "TV", Path: "/media/TV"},
		},
	}
	lib := detectLibrary("/media/TV/ShowName", cfg)
	if lib == nil || lib.Name != "TV" {
		t.Errorf("expected most-specific library TV, got %v", lib)
	}
}

func TestDetectLibrary_EmptyConfig(t *testing.T) {
	cfg := &GlobalConfig{}
	lib := detectLibrary("/any/path", cfg)
	if lib != nil {
		t.Errorf("expected nil for empty config, got %+v", lib)
	}
}

func TestResolveIETFMap_NilLib_UsesDefaults(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep01.mkv")
	m := resolveIETFMap(file, root, nil, OriginInfo{})
	if m["eng"] != "en-US" {
		t.Errorf("expected en-US for eng, got %q", m["eng"])
	}
}

func TestResolveIETFMap_Applies_Override(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "Agatha Christie's Poirot (1989)", IETF: map[string]string{"eng": "en-GB"}},
		},
	}

	// File inside the override directory → should get en-GB.
	inside := filepath.Join(root, "Agatha Christie's Poirot (1989)", "S01E01.mkv")
	m := resolveIETFMap(inside, root, lib, OriginInfo{})
	if m["eng"] != "en-GB" {
		t.Errorf("expected en-GB inside override dir, got %q", m["eng"])
	}
	// Other languages should retain their defaults.
	if m["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn, got %q", m["jpn"])
	}

	// File outside the override directory → should get en-US.
	outside := filepath.Join(root, "Another Show", "S01E01.mkv")
	m2 := resolveIETFMap(outside, root, lib, OriginInfo{})
	if m2["eng"] != "en-US" {
		t.Errorf("expected en-US outside override dir, got %q", m2["eng"])
	}
}

func TestResolveIETFMap_TMDBOrigin_SetsIETF(t *testing.T) {
	// TMDB says the show is Canadian English → eng should map to en-CA.
	root := t.TempDir()
	file := filepath.Join(root, "Paw Patrol (2013)", "S01E01.mkv")
	origin := OriginInfo{OrigLang: "en", OriginCountry: "CA"}
	m := resolveIETFMap(file, root, nil, origin)
	if m["eng"] != "en-CA" {
		t.Errorf("expected en-CA from TMDB origin, got %q", m["eng"])
	}
	// Other languages should keep their defaults.
	if m["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn, got %q", m["jpn"])
	}
}

func TestResolveIETFMap_ManualOverrideWinsOverTMDB(t *testing.T) {
	// TMDB says US English, but the manual override says GB → override wins.
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "My Show", IETF: map[string]string{"eng": "en-GB"}},
		},
	}
	file := filepath.Join(root, "My Show", "S01E01.mkv")
	origin := OriginInfo{OrigLang: "en", OriginCountry: "US"}
	m := resolveIETFMap(file, root, lib, origin)
	if m["eng"] != "en-GB" {
		t.Errorf("manual override should win over TMDB; got %q", m["eng"])
	}
}

func TestResolveIETFMap_TMDBOrigin_UnknownLang_NoEffect(t *testing.T) {
	// TMDB returns a language we don't have in ietfMap → should be ignored.
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep.mkv")
	origin := OriginInfo{OrigLang: "tl", OriginCountry: "PH"} // Tagalog
	m := resolveIETFMap(file, root, nil, origin)
	// eng should still be en-US (not affected by the unknown-language origin).
	if m["eng"] != "en-US" {
		t.Errorf("expected en-US (unaffected by unknown origin lang), got %q", m["eng"])
	}
}

func TestContentRootDir_TV(t *testing.T) {
	got := contentRootDir(
		filepath.FromSlash("/media/TV/Paw Patrol (2013)/Season 1/ep.mkv"),
		filepath.FromSlash("/media/TV"),
	)
	if got != "Paw Patrol (2013)" {
		t.Errorf("got %q, want %q", got, "Paw Patrol (2013)")
	}
}

func TestContentRootDir_NotUnder(t *testing.T) {
	got := contentRootDir(
		filepath.FromSlash("/other/path/file.mkv"),
		filepath.FromSlash("/media/TV"),
	)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestDirTitleYear_WithYear(t *testing.T) {
	title, year := dirTitleYear("Paw Patrol (2013)")
	if title != "Paw Patrol" || year != "2013" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, "Paw Patrol", "2013")
	}
}

func TestDirTitleYear_WithoutYear(t *testing.T) {
	title, year := dirTitleYear("Some Show")
	if title != "Some Show" || year != "" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, "Some Show", "")
	}
}

func TestDirTitleYear_FlatMovieFilename(t *testing.T) {
	// Flat movie library: directory name is the filename stem including .mkv.
	title, year := dirTitleYear("The Matrix (1999).mkv")
	if title != "The Matrix" || year != "1999" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, "The Matrix", "1999")
	}
}

func TestResolveIETFMap_CaseInsensitive(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "MyShow", IETF: map[string]string{"eng": "en-AU"}},
		},
	}
	// Path in different case.
	file := filepath.Join(root, "myshow", "ep01.mkv")
	m := resolveIETFMap(file, root, lib, OriginInfo{})
	if m["eng"] != "en-AU" {
		t.Errorf("expected en-AU for case-insensitive match, got %q", m["eng"])
	}
}
