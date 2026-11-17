package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// titleCacheWith returns a title cache saved to a temp file holding titles,
// loaded back from disk.
func titleCacheWith(t *testing.T, titles map[string]string) *TitleCache {
	t.Helper()
	path := filepath.Join(t.TempDir(), "title_cache.json")
	tc, err := LoadTitleCache(path)
	if err != nil {
		t.Fatal(err)
	}
	for p, title := range titles {
		tc.Set(p, title)
	}
	mustSave(t, tc.Save())
	if tc, err = LoadTitleCache(path); err != nil {
		t.Fatal(err)
	}
	return tc
}

// savedTitles reads tc's file back as the path → title map it holds.
func savedTitles(t *testing.T, tc *TitleCache) map[string]string {
	t.Helper()
	got, err := LoadTitleCache(tc.path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range got.titles {
		out[e.path] = e.title
	}
	return out
}

// With case folding (Windows), a lookup in any case finds the title, and the
// path is saved as last spelled; without it, case matters.
func TestTitleCache_PathCase(t *testing.T) {
	for _, fold := range []bool{true, false} {
		setFoldPathCase(t, fold)
		tc := titleCacheWith(t, map[string]string{`D:\TV\Show\a.mkv`: "Full"})

		if _, ok := tc.Get(`d:\tv\show\a.mkv`); ok != fold {
			t.Errorf("fold=%v: lowercase lookup found = %v", fold, ok)
		}
		if got, _ := tc.Get(`D:\TV\Show\a.mkv`); got != "Full" {
			t.Errorf("fold=%v: exact lookup = %q; want Full", fold, got)
		}
		tc.Set(`d:\tv\show\a.mkv`, "Newer")
		mustSave(t, tc.Save())
		want := map[string]string{`d:\tv\show\a.mkv`: "Newer"}
		if !fold {
			want[`D:\TV\Show\a.mkv`] = "Full"
		}
		if got := savedTitles(t, tc); len(got) != len(want) || got[`d:\tv\show\a.mkv`] != "Newer" {
			t.Errorf("fold=%v: saved %v; want %v", fold, got, want)
		}
	}
}

// Two spellings of one path in a file load as one entry, the same one each time.
func TestTitleCache_LoadMergesCaseCollisions(t *testing.T) {
	setFoldPathCase(t, true)
	path := filepath.Join(t.TempDir(), "title_cache.json")
	if err := os.WriteFile(path, []byte(`{"d:\\tv\\a.mkv": "Lower", "D:\\TV\\a.mkv": "Upper"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tc, err := LoadTitleCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := tc.Get(`d:\tv\a.mkv`); got != "Upper" || len(tc.titles) != 1 {
		t.Errorf("Get = %q with %d entries; want the first-sorted spelling's title, Upper, alone", got, len(tc.titles))
	}
}

func TestTitleCache_Move(t *testing.T) {
	tc := titleCacheWith(t, map[string]string{"/tv/old.mkv": "Full"})

	if tc.Move("/tv/missing.mkv", "/tv/x.mkv") {
		t.Error("Move of an unlisted path reported a move")
	}
	if !tc.Move("/tv/old.mkv", "/tv/new.mkv") {
		t.Fatal("Move reported nothing moved")
	}
	if _, ok := tc.Get("/tv/old.mkv"); ok {
		t.Error("the old path still has a title")
	}
	if got, _ := tc.Get("/tv/new.mkv"); got != "Full" {
		t.Errorf("new path's title = %q; want Full", got)
	}
	var nilCache *TitleCache
	if nilCache.Move("/a", "/b") {
		t.Error("a nil cache reported a move")
	}
}

// A normal run finds a title cached under the media path in another case.
func TestRun_TitleCacheMatchesPathInAnotherCase(t *testing.T) {
	setFoldPathCase(t, true)
	dir := showLibrary(t, "Show A", "Show A - S01E01.mkv")
	file := filepath.Join(dir, "Show A", "Show A - S01E01.mkv")
	titles := titleCacheWith(t, map[string]string{strings.ToUpper(file): "Show A - The Full Title"})

	runner := runTitledShow(t, dir, titles, nil)

	if len(runner.calls) == 0 || !containsAll(runner.calls[0].args, "title=Show A - The Full Title") {
		t.Errorf("tag call = %v; want the cached full title", runner.calls)
	}
}

// A normal run that renames a multi-episode file keeps its cached title with
// the new episode code, and saves the cache with the entry under the new name.
func TestRun_RenameMovesTitleCacheEntry(t *testing.T) {
	dir := showLibrary(t, "Show A", "Show A - S01E01E02E03.mkv")
	oldPath := filepath.Join(dir, "Show A", "Show A - S01E01E02E03.mkv")
	newPath := filepath.Join(dir, "Show A", "Show A - S01E01-E03.mkv")
	titles := titleCacheWith(t, map[string]string{oldPath: "Show A - S01E01E02E03 - One + Two + Three"})
	const want = "Show A - S01E01-E03 - One + Two + Three"

	runner := runTitledShow(t, dir, titles, nil)

	if len(runner.calls) == 0 || !containsAll(runner.calls[0].args, "title="+want) {
		t.Errorf("tag call = %v; want the cached title with the new episode code", runner.calls)
	}
	if got := savedTitles(t, titles); len(got) != 1 || got[newPath] != want {
		t.Errorf("title cache on disk = %v; want %q under %s", got, want, newPath)
	}
}

// A run that renames nothing doesn't write the title cache.
func TestRun_NoRenameLeavesTitleCacheAlone(t *testing.T) {
	dir := showLibrary(t, "Show A", "Show A - S01E01.mkv")
	titles, err := LoadTitleCache(filepath.Join(t.TempDir(), "title_cache.json"))
	if err != nil {
		t.Fatal(err)
	}

	runTitledShow(t, dir, titles, nil)

	if _, err := os.Stat(titles.path); !os.IsNotExist(err) {
		t.Errorf("title cache written (%v); want it left alone", err)
	}
}

// A title cache that can't be saved after a move is reported and counted.
func TestRun_TitleCacheSaveFailureAfterMove(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir := showLibrary(t, "Show A", "Show A - S01E01E02E03.mkv")
	titles := titleCacheWith(t, map[string]string{filepath.Join(dir, "Show A", "Show A - S01E01E02E03.mkv"): "Full"})
	unwritable(t, titles.path)

	var stats Stats
	out := captureOutput(t, func() { runTitledShow(t, dir, titles, &stats) })

	if stats.LostSaves != 1 || !strings.Contains(out, "Failed to save title cache") {
		t.Errorf("LostSaves = %d; want 1 with the lost-save message:\n%s", stats.LostSaves, out)
	}
}

// runTitledShow runs a full pass over the one show in dir ("Show A", TMDB ID
// 999) with titles as its title cache, storing the run's stats in stats when
// set, and returns the MKV runner.
func runTitledShow(t *testing.T, dir string, titles *TitleCache, stats *Stats) *fakeMKVRunner {
	t.Helper()
	tmdb := &fakeTMDB{
		showDetails:   ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult:  SeasonResult{AirDate: "2022-02-01", Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-03-01"}}},
		contentRating: RatingPG,
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cacheWith(t, map[string]string{"Show A": "999"}),
		map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, emptyRetryList(t),
		nil, nil, nil, titles)
	var s Stats
	captureStdout(t, func() { s = p.Run() })
	if stats != nil {
		*stats = s
	}
	return runner
}

// --deband-only's multi-episode rename moves the title-cache entry and saves
// it. The title keeps its old episode code, as the MKV does: this mode doesn't
// write titles, so the next normal run or --title-check sees the difference.
func TestRunDebandOnly_RenameMovesTitleCacheEntry(t *testing.T) {
	dir := showLibrary(t, "Banded", "Banded - S01E01E02E03.mkv")
	oldPath := filepath.Join(dir, "Banded", "Banded - S01E01E02E03.mkv")
	newPath := filepath.Join(dir, "Banded", "Banded - S01E01-E03.mkv")
	const want = "Banded - S01E01E02E03 - One + Two + Three"
	titles := titleCacheWith(t, map[string]string{oldPath: want})
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", extractTagsXML: "<Tags></Tags>"}
	manual := map[string]ManualEntry{"Banded": {DebandEpisodes: map[string]bool{"S01E01": true}}}
	p := NewProcessor(Config{MediaDir: dir}, nil, manual, nil, runner, nil, noopTimer{}, emptyRetryList(t), nil, nil, nil, titles)

	captureStdout(t, func() { p.RunDebandOnly() })

	if got := savedTitles(t, titles); len(got) != 1 || got[newPath] != want {
		t.Errorf("title cache on disk = %v; want %q under %s", got, want, newPath)
	}
}

// A --title-check rename drops the old title and caches the new one it sets.
func TestTitleCheck_RenameCachesNewTitle(t *testing.T) {
	got, newPath, out := runCachedRenameCheck(t, &fakeMKVRunner{avail: true, applyResult: "Done."})

	if len(got) != 1 || got[newPath] != "Show - S01E02 - New Title" {
		t.Errorf("title cache on disk = %v; want only the new title under %s:\n%s", got, newPath, out)
	}
}

// A --title-check rename that doesn't set the title leaves no cached title for
// the new name, so a normal run titles the file from its name rather than
// writing the old title back.
func TestTitleCheck_RenameWithoutTitleWriteDropsOldTitle(t *testing.T) {
	got, _, out := runCachedRenameCheck(t, &fakeMKVRunner{avail: false})

	if len(got) != 0 {
		t.Errorf("title cache on disk = %v; want it empty:\n%s", got, out)
	}
}

// runCachedRenameCheck runs --title-check over a file whose cached title is
// out of date, answering yes to the rename, and returns the saved title cache,
// the file's new path and the output.
func runCachedRenameCheck(t *testing.T, mkv MKVRunner) (saved map[string]string, newPath, out string) {
	t.Helper()
	dir, oldPath, newPath := renameShow(t)
	titles := titleCacheWith(t, map[string]string{oldPath: "Show - S01E02 - Old Title"})
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 2, Name: "New Title"}}}}
	tc := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&scriptedPrompter{answers: []string{"y"}}, mkv, titles, noopTimer{}, nil)

	out = captureOutput(t, tc.Run)

	return savedTitles(t, titles), newPath, out
}

func TestWithEpisodeCode(t *testing.T) {
	for _, tc := range []struct{ title, stem, want string }{
		{"Show - S01E01E02E03 - A + B + C", "Show - S01E01-E03", "Show - S01E01-E03 - A + B + C"},
		{"Show - S01E01E02E03 - A + B + C", "Show - s01E01-E03", "Show - s01E01-E03 - A + B + C"},
		{"Show - S01E01-E03 - A + B + C", "Show - S01E01-E03", "Show - S01E01-E03 - A + B + C"},
		{"Custom title", "Show - S01E01-E03", "Custom title"},
		{"Show - S01E02 - A", "Show - Special", "Show - S01E02 - A"},
	} {
		if got := withEpisodeCode(tc.title, tc.stem); got != tc.want {
			t.Errorf("withEpisodeCode(%q, %q) = %q; want %q", tc.title, tc.stem, got, tc.want)
		}
	}
}
