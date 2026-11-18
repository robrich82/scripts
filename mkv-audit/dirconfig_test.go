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

func TestDirTitleYear_FSSubstituteChars(t *testing.T) {
	// Filesystem-safe substitute characters must be converted back for TMDB.
	title, year := dirTitleYear("The Testament of Sister New Devil꞉ Departures (2018).mkv")
	wantTitle := "The Testament of Sister New Devil: Departures"
	if title != wantTitle || year != "2018" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, wantTitle, "2018")
	}
}

func TestDirTitleYear_FullwidthQuestionMark(t *testing.T) {
	title, year := dirTitleYear("Is It Wrong to Try to Pick Up Girls in a Dungeon？꞉ Arrow of the Orion (2019).mkv")
	wantTitle := "Is It Wrong to Try to Pick Up Girls in a Dungeon?: Arrow of the Orion"
	if title != wantTitle || year != "2019" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, wantTitle, "2019")
	}
}

func TestDirTitleYear_PeriodInTitleSubdir(t *testing.T) {
	// Subdirectory name: no extension, but title contains a period.
	// Must not truncate at the period mid-title.
	title, year := dirTitleYear("Fate╱stay night꞉ Heaven's Feel I. Presage Flower (2017)")
	wantTitle := "Fate/stay night: Heaven's Feel I. Presage Flower"
	if title != wantTitle || year != "2017" {
		t.Errorf("got title=%q year=%q, want %q %q", title, year, wantTitle, "2017")
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

// TestResolveIETFMap_FileSpecificOverride verifies that a DirOverride whose
// path is a full relative file path (not a directory) matches only that file.
func TestResolveIETFMap_FileSpecificOverride(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "Music",
		Path: root,
		Overrides: []DirOverride{
			// Directory-level: all YOASOBI files are Japanese.
			{Path: "YOASOBI", IETF: map[string]string{"jpn": "ja-JP"}},
			// File-level: one specific track is English.
			{Path: "YOASOBI/アイドル (2023)/02 - Idol - Idol-video.mkv", IETF: map[string]string{"eng": "en-JP"}},
		},
	}

	// The specific file should get both overrides merged.
	target := filepath.Join(root, "YOASOBI", "アイドル (2023)", "02 - Idol - Idol-video.mkv")
	m := resolveIETFMap(target, root, lib, OriginInfo{})
	if m["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn on target file, got %q", m["jpn"])
	}
	if m["eng"] != "en-JP" {
		t.Errorf("expected en-JP for eng on target file, got %q", m["eng"])
	}

	// A sibling file should only get the directory override.
	sibling := filepath.Join(root, "YOASOBI", "アイドル (2023)", "01 - アイドル - アイドル-video.mkv")
	m2 := resolveIETFMap(sibling, root, lib, OriginInfo{})
	if m2["jpn"] != "ja-JP" {
		t.Errorf("expected ja-JP for jpn on sibling, got %q", m2["jpn"])
	}
	if m2["eng"] != "en-US" {
		t.Errorf("expected en-US (default) for eng on sibling, got %q", m2["eng"])
	}

	// matchedOverrideIETF should also return both keys for the target file.
	ov := matchedOverrideIETF(target, root, lib)
	if ov["eng"] != "en-JP" {
		t.Errorf("matchedOverrideIETF: expected en-JP for eng, got %q", ov["eng"])
	}
	if ov["jpn"] != "ja-JP" {
		t.Errorf("matchedOverrideIETF: expected ja-JP for jpn, got %q", ov["jpn"])
	}
}

func TestResolveOriginLang_NilLib_UsesOrigin(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep01.mkv")
	origin := OriginInfo{OrigLang: "en", OriginCountry: "US"}
	got := resolveOriginLang(file, root, nil, origin)
	if got != "eng" {
		t.Errorf("expected eng, got %q", got)
	}
}

func TestResolveOriginLang_Override_WinsOverTMDB(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "Star Wars Visions (2021)", OriginLang: "jpn"},
		},
	}
	// TMDB says English, but the override corrects it to Japanese.
	file := filepath.Join(root, "Star Wars Visions (2021)", "S01E01.mkv")
	origin := OriginInfo{OrigLang: "en", OriginCountry: "US"}
	got := resolveOriginLang(file, root, lib, origin)
	if got != "jpn" {
		t.Errorf("expected jpn from override, got %q", got)
	}
}

func TestResolveOriginLang_NoMatch_UsesOrigin(t *testing.T) {
	root := t.TempDir()
	lib := &Library{
		Name: "TV",
		Path: root,
		Overrides: []DirOverride{
			{Path: "Other Show", OriginLang: "jpn"},
		},
	}
	file := filepath.Join(root, "Different Show", "ep.mkv")
	origin := OriginInfo{OrigLang: "ko", OriginCountry: "KR"}
	got := resolveOriginLang(file, root, lib, origin)
	if got != "kor" {
		t.Errorf("expected kor from TMDB, got %q", got)
	}
}

func TestResolveOriginLang_EmptyOrigin(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "show", "ep.mkv")
	got := resolveOriginLang(file, root, nil, OriginInfo{})
	if got != "und" {
		t.Errorf("expected und for empty origin, got %q", got)
	}
}

// TestResolveIETFMap_NFDPath verifies that an override path in NFC matches a
// filesystem path in NFD (and vice-versa) — both normalized to NFC before compare.
func TestResolveIETFMap_NFDPath(t *testing.T) {
	// NFC form of "ド" is U+30C9; NFD form is U+30C8 + U+309B.
	nfc := "YOASOBI/\u30A2\u30A4\u30C9\u30EB (2023)/02 - Idol - Idol-video.mkv"       // ア+イ+ド+ル (NFC)
	nfd := "YOASOBI/\u30A2\u30A4\u30C8\u3099\u30EB (2023)/02 - Idol - Idol-video.mkv" // ア+イ+ト+combining-dakuten+ル (NFD)

	root := t.TempDir()
	lib := &Library{
		Name: "Music",
		Path: root,
		Overrides: []DirOverride{
			// Config stores the path in NFC.
			{Path: nfc, IETF: map[string]string{"eng": "en-JP"}},
		},
	}

	// Filesystem path arrives as NFD (e.g. copied from macOS).
	fileNFD := filepath.Join(root, filepath.FromSlash(nfd))
	m := resolveIETFMap(fileNFD, root, lib, OriginInfo{})
	if m["eng"] != "en-JP" {
		t.Errorf("NFC config path should match NFD filesystem path; got eng=%q", m["eng"])
	}
}

// A folder whose name starts with ".." is inside the library, not above it.
func TestContentRootDir_NameStartingWithDots(t *testing.T) {
	got := contentRootDir(
		filepath.FromSlash("/media/Movies/...And Justice for All (1979)/movie.mkv"),
		filepath.FromSlash("/media/Movies"),
	)
	if want := "...And Justice for All (1979)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDetectLibrary_FolderStartingWithDots(t *testing.T) {
	cfg := &GlobalConfig{Libraries: []Library{{Name: "Movies", Path: "/media/Movies"}}}
	if lib := detectLibrary("/media/Movies/..tick..tick..BOOM! (2021)", cfg); lib == nil || lib.Name != "Movies" {
		t.Errorf("got %+v, want the Movies library", lib)
	}
	if lib := detectLibrary("/media/Other", cfg); lib != nil {
		t.Errorf("a sibling of the library matched: %+v", lib)
	}
}

func TestRelOutside(t *testing.T) {
	sep := string(filepath.Separator)
	for rel, want := range map[string]bool{
		"..":                   true,
		".." + sep + "x":       true,
		".":                    false,
		"...And Justice":       false,
		"..x" + sep + "file":   false,
		"a" + sep + ".." + "b": false,
	} {
		if got := relOutside(rel); got != want {
			t.Errorf("relOutside(%q) = %v, want %v", rel, got, want)
		}
	}
}

// A movie folder whose name starts with ".." gets its TMDB origin, and its ID
// is saved against its file.
func TestFetchOrigins_FolderNameStartingWithDots(t *testing.T) {
	const name = "...And Justice for All (1979)"
	root, paths := siblingLibrary(t, name, name+".mkv")
	tmdb := titleTMDB{
		ids:   map[string]string{"...And Justice for All": "11"},
		langs: map[string]string{"11": "en"},
	}
	captureStdout(t, func() {
		origins := fetchOrigins(paths, root, "movie", tmdb, &fakePrompter{answer: "y"})
		if origins[name].OrigLang != "en" {
			t.Errorf("origins = %v; want en for %s", origins, name)
		}
	})
	if _, files := readMovieSections(t); files[paths[0]] != "11" {
		t.Errorf("files = %v; want %s → 11", files, paths[0])
	}
}
