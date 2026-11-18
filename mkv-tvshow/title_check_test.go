package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStripPartSuffix(t *testing.T) {
	cases := []struct {
		input      string
		wantBase   string
		wantSuffix string
	}{
		{"Some Title-part1", "Some Title", "-part1"},
		{"Some Title-part2", "Some Title", "-part2"},
		{"Some Title-Part1", "Some Title", "-Part1"},
		{"Some Title", "Some Title", ""},
		{"Title-partial", "Title-partial", ""}, // word "partial" must not match
		{"Title-part10", "Title", "-part10"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			base, suffix := stripPartSuffix(tc.input)
			if base != tc.wantBase || suffix != tc.wantSuffix {
				t.Errorf("stripPartSuffix(%q) = (%q, %q), want (%q, %q)",
					tc.input, base, suffix, tc.wantBase, tc.wantSuffix)
			}
		})
	}
}

func TestExtractFileTitles_PartSuffix(t *testing.T) {
	cases := []struct {
		stem string
		want []string
	}{
		// Single episode, part suffix stripped before comparison.
		{"Show - S01E05 - Some Episode-part1", []string{"Some Episode"}},
		{"Show - S01E05 - Some Episode-part2", []string{"Some Episode"}},
		{"Show - S01E05 - Some Episode-Part1", []string{"Some Episode"}},
		// Multi-episode: suffix on the last title only.
		{"Show - S01E05E06 - Title A﹢Title B-part1", []string{"Title A", "Title B"}},
		// No suffix: unchanged.
		{"Show - S01E05 - Some Episode", []string{"Some Episode"}},
	}
	for _, tc := range cases {
		t.Run(tc.stem, func(t *testing.T) {
			got := extractFileTitles(tc.stem)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractFileTitles(%q) = %v, want %v", tc.stem, got, tc.want)
			}
		})
	}
}

func TestBuildNewStemFromFS_PartSuffix(t *testing.T) {
	cases := []struct {
		oldStem     string
		fsTitlePart string
		wantStem    string
	}{
		{
			"Show - S01E05 - Old Title-part1",
			"New Title",
			"Show - S01E05 - New Title-part1",
		},
		{
			"Show - S01E05 - Old Title-part2",
			"New Title",
			"Show - S01E05 - New Title-part2",
		},
		{
			"Show - S01E05 - Old Title",
			"New Title",
			"Show - S01E05 - New Title",
		},
	}
	for _, tc := range cases {
		t.Run(tc.oldStem, func(t *testing.T) {
			got, _ := buildNewStemFromFS(tc.oldStem, tc.fsTitlePart, `C:\Media\Show`)
			if got != tc.wantStem {
				t.Errorf("buildNewStemFromFS(%q, %q) = %q, want %q",
					tc.oldStem, tc.fsTitlePart, got, tc.wantStem)
			}
		})
	}
}

func TestAllPlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		titles []string
		want   bool
	}{
		{"empty", []string{}, false},
		{"single placeholder", []string{"Episode 2"}, true},
		{"placeholder case insensitive", []string{"episode 12"}, true},
		{"real title", []string{"Ami Has Arrived"}, false},
		{"mixed", []string{"Episode 1", "Ami Has Arrived"}, false},
		{"multi all placeholders", []string{"Episode 1", "Episode 2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allPlaceholders(tc.titles)
			if got != tc.want {
				t.Errorf("allPlaceholders(%v) = %v, want %v", tc.titles, got, tc.want)
			}
		})
	}
}

// noopTimer satisfies FileTimer without touching the filesystem.
type noopTimer struct{}

func (noopTimer) SetTimes(_ string, _ time.Time) error        { return nil }
func (noopTimer) SetCreationTime(_ string, _ time.Time) error { return nil }

// newNullTitleCache returns a TitleCache backed by a temp file.
func newNullTitleCache(t *testing.T) *TitleCache {
	t.Helper()
	tc, _ := LoadTitleCache(filepath.Join(t.TempDir(), "title_cache.json"))
	return tc
}

// assertNoAskPrompter fails the test if Ask is ever called.
type assertNoAskPrompter struct{ t *testing.T }

func (p *assertNoAskPrompter) Ask(q string) (string, error) {
	p.t.Errorf("unexpected rename prompt when TMDB has placeholder title: %q", q)
	return "n", nil
}

// TestTitleCheck_SkipsPlaceholderRename verifies that when TMDB returns a
// placeholder title ("Episode N") but the file already has a real title, no
// rename is proposed and the file is left untouched.
func TestTitleCheck_SkipsPlaceholderRename(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Show")
	if err := os.Mkdir(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filename := "Show - S01E02 - Ami Has Arrived.mkv"
	mkvPath := filepath.Join(showDir, filename)
	if err := os.WriteFile(mkvPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Show": "tt123"})
	tmdbClient := &fakeTMDB{
		seasonResult: SeasonResult{Episodes: []Episode{
			{EpisodeNumber: 2, Name: "Episode 2"},
		}},
	}

	tc := NewTitleChecker(
		Config{MediaDir: dir},
		cache,
		nil,
		tmdbClient,
		&assertNoAskPrompter{t: t},
		nil, // no mkvpropedit
		newNullTitleCache(t),
		noopTimer{},
		nil,
	)

	tc.Run()

	// File must not have been renamed.
	if _, err := os.Stat(mkvPath); os.IsNotExist(err) {
		t.Errorf("file was renamed or deleted: %s", mkvPath)
	}
}

func TestApplyFullTitle_RestoresTimestamps(t *testing.T) {
	path := debandTestFile(t)
	want := time.Date(2021, 3, 15, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatal(err)
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
	timer := &captureFileTimer{}
	tc := NewTitleChecker(Config{}, nil, nil, nil, nil, runner, newNullTitleCache(t), timer, nil)

	tc.applyFullTitle(path, "Full Title")

	if len(runner.calls) != 1 {
		t.Fatalf("Apply called %d times, want 1", len(runner.calls))
	}
	if len(timer.setTimesCalls) != 1 || !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("SetTimes calls = %v, want one restoring %v", timer.setTimesCalls, want)
	}
	if got, ok := tc.titleCache.Get(path); !ok || got != "Full Title" {
		t.Errorf("title cache = %q ok=%v, want the new title", got, ok)
	}
}

func TestApplyFullTitle_StatError_DoesNotSetTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.mkv")
	runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
	tc := NewTitleChecker(Config{}, nil, nil, nil, nil, runner, newNullTitleCache(t), &captureFileTimer{}, nil)

	out := captureStdout(t, func() { tc.applyFullTitle(path, "Full Title") })

	if len(runner.calls) != 0 {
		t.Errorf("Apply called %d times, want none: timestamps couldn't be restored", len(runner.calls))
	}
	if _, ok := tc.titleCache.Get(path); ok {
		t.Error("title cached although it was never set")
	}
	if !strings.Contains(out, "not setting title") {
		t.Errorf("output doesn't explain the skip:\n%s", out)
	}
}

// renameShow makes dir/Show/"Show - S01E02 - Old Title.mkv", whose title
// TMDB has as "New Title", and returns the file and the path a rename gives it.
func renameShow(t *testing.T) (dir, oldPath, newPath string) {
	t.Helper()
	dir = showLibrary(t, "Show", "Show - S01E02 - Old Title.mkv")
	return dir, filepath.Join(dir, "Show", "Show - S01E02 - Old Title.mkv"),
		filepath.Join(dir, "Show", "Show - S01E02 - New Title.mkv")
}

// runRenameCheck runs --title-check over dir, answering yes to the rename.
func runRenameCheck(t *testing.T, dir string, retry *RetryList) (*TitleChecker, string) {
	t.Helper()
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 2, Name: "New Title"}}}}
	tc := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&scriptedPrompter{answers: []string{"y"}}, nil, newNullTitleCache(t), noopTimer{}, retry)
	out := captureOutput(t, tc.Run)
	return tc, out
}

// A rename moves the file's retry entry, with its failed-run count, to the new name.
func TestTitleCheck_RenameMovesRetryEntry(t *testing.T) {
	dir, oldPath, newPath := renameShow(t)
	retryPath := filepath.Join(t.TempDir(), "pending_dates.json")
	retry := newRetryList(retryPath)
	retry.Fail(oldPath)
	retry.Fail(oldPath)
	mustSave(t, retry.Save())
	retry, _ = LoadRetryList(retryPath)

	tc, out := runRenameCheck(t, dir, retry)

	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("file not renamed: %v\n%s", err, out)
	}
	got, err := LoadRetryList(retryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Has(oldPath) || got.Attempts(newPath) != 2 || got.Len() != 1 {
		t.Errorf("retry list on disk = %v; want only the new path, with 2 failed runs", got.data)
	}
	if tc.LostSaves != 0 {
		t.Errorf("LostSaves = %d; want 0", tc.LostSaves)
	}
}

// A file not on the retry list isn't added by a rename, and the list isn't written.
func TestTitleCheck_RenameOffRetryListLeavesItAlone(t *testing.T) {
	dir, _, _ := renameShow(t)
	retry := emptyRetryList(t)

	runRenameCheck(t, dir, retry)

	if _, err := os.Stat(retry.path); !os.IsNotExist(err) {
		t.Errorf("retry list written (%v); want it left alone", err)
	}
}

// A failed rename leaves the retry entry where it was.
func TestTitleCheck_FailedRenameKeepsRetryEntry(t *testing.T) {
	dir, oldPath, newPath := renameShow(t)
	if err := os.Mkdir(newPath, 0o755); err != nil { // a folder in the way fails the rename
		t.Fatal(err)
	}
	retry := emptyRetryList(t)
	retry.Fail(oldPath)

	_, out := runRenameCheck(t, dir, retry)

	if !strings.Contains(out, "[X] Rename failed") {
		t.Fatalf("rename didn't fail:\n%s", out)
	}
	if !retry.Has(oldPath) || retry.Attempts(oldPath) != 1 || retry.Len() != 1 {
		t.Errorf("retry list = %v; want the old path, with 1 failed run", retry.data)
	}
}

// A retry list --title-check can't save is reported, retried, and counted as lost.
func TestTitleCheck_RetryListSaveFailure(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	dir, oldPath, _ := renameShow(t)
	retry := newRetryList(filepath.Join(t.TempDir(), "missing", "pending_dates.json"))
	retry.Add(oldPath)

	tc, out := runRenameCheck(t, dir, retry)

	for _, want := range []string{
		"[!] Failed to save retry list " + retry.path,
		"[!] The retry list " + retry.path + " was never saved; this run's changes to the episodes to retry are lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if tc.LostSaves != 1 {
		t.Errorf("LostSaves = %d; want 1", tc.LostSaves)
	}
}

// --title-check loads the retry list, so a corrupt one stops it before it renames anything.
func TestMain_TitleCheckLoadsRetryList(t *testing.T) {
	cfgHome := t.TempDir()
	cfg := filepath.Join(cfgHome, "mkv-tvshow")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "pending_dates.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runMainEnv(t, t.TempDir(), cfgHome, []string{"TMDB_API_KEY=unused"}, "--title-check")

	if err == nil || !strings.Contains(out, "Error loading "+filepath.Join(cfg, "pending_dates.json")) {
		t.Errorf("--title-check = %v; want the retry list's load error:\n%s", err, out)
	}
}
