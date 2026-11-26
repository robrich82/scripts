package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// titleCheckLibrary makes dir/Show/"Show - S01E02 - New Title.mkv", whose
// filename already matches TMDB, and a checker over it using runner and titles.
func titleCheckLibrary(t *testing.T, runner MKVRunner, titles *TitleCache) (tc *TitleChecker, path string) {
	t.Helper()
	dir := showLibrary(t, "Show", "Show - S01E02 - New Title.mkv")
	path = filepath.Join(dir, "Show", "Show - S01E02 - New Title.mkv")
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 2, Name: "New Title"}}}}
	tc = NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&assertNoAskPrompter{t}, runner, titles, noopTimer{}, nil)
	return tc, path
}

const wantFullTitle = "Show - S01E02 - New Title"

// A new release under the same name keeps its own title: the cache still says
// the full title is set, but the file says otherwise, so --title-check sets it.
func TestTitleCheck_FileTitleDiffersFromCache_SetsTitle(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
	titles := newNullTitleCache(t)
	tc, path := titleCheckLibrary(t, runner, titles)
	titles.Set(path, wantFullTitle)
	runner.titles = map[string]string{path: "Release.Group.S01E02.1080p"}

	out := captureOutput(t, tc.Run)

	if len(runner.calls) != 1 || !strings.Contains(strings.Join(runner.calls[0].args, " "), "title="+wantFullTitle) {
		t.Fatalf("Apply calls = %v; want one setting %q:\n%s", runner.calls, wantFullTitle, out)
	}
	if got := savedTitles(t, titles); got[path] != wantFullTitle {
		t.Errorf("title cache on disk = %v; want %q", got, wantFullTitle)
	}
}

// A file that already has the full title isn't written, and the title is
// cached so a normal run keeps it.
func TestTitleCheck_FileHasTitle_CachesWithoutWriting(t *testing.T) {
	for name, cached := range map[string]string{"not cached": "", "stale cache": "Show - S01E02 - Old"} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
			titles := newNullTitleCache(t)
			tc, path := titleCheckLibrary(t, runner, titles)
			if cached != "" {
				titles.Set(path, cached)
			}
			runner.titles = map[string]string{path: wantFullTitle}

			out := captureOutput(t, tc.Run)

			if len(runner.calls) != 0 {
				t.Errorf("Apply called %d times, want none:\n%s", len(runner.calls), out)
			}
			if got := savedTitles(t, titles); got[path] != wantFullTitle {
				t.Errorf("title cache on disk = %v; want %q", got, wantFullTitle)
			}
		})
	}
}

// Without a readable title the cache decides, as before: a cached full title
// skips the write, a missing one sets it.
func TestTitleCheck_UnreadableTitle_TrustsCache(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		cached    bool
		wantCalls int
		wantOut   string
	}{
		{"no mkvmerge, cached", nil, true, 0, "mkvmerge not in PATH"},
		{"no mkvmerge, not cached", nil, false, 1, "mkvmerge not in PATH"},
		{"read error, cached", errors.New("boom"), true, 0, "Can't read the MKV title"},
		{"read error, not cached", errors.New("boom"), false, 1, "Can't read the MKV title"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeMKVRunner{avail: true, applyResult: "Done.", readTitleErr: tt.err}
			titles := newNullTitleCache(t)
			tc, path := titleCheckLibrary(t, runner, titles)
			if tt.cached {
				titles.Set(path, wantFullTitle)
			}

			out := captureOutput(t, tc.Run)

			if len(runner.calls) != tt.wantCalls {
				t.Errorf("Apply called %d times, want %d:\n%s", len(runner.calls), tt.wantCalls, out)
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("output doesn't contain %q:\n%s", tt.wantOut, out)
			}
		})
	}
}

// The missing-mkvmerge warning prints once per run, not once per file.
func TestTitleCheck_NoMKVMerge_WarnsOnce(t *testing.T) {
	dir := showLibrary(t, "Show", "Show - S01E01 - One.mkv", "Show - S01E02 - Two.mkv")
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, Name: "One"}, {EpisodeNumber: 2, Name: "Two"}}}}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done."}
	tc := NewTitleChecker(Config{MediaDir: dir}, cacheWith(t, map[string]string{"Show": "999"}), nil, tmdb,
		&assertNoAskPrompter{t}, runner, newNullTitleCache(t), noopTimer{}, nil)

	out := captureOutput(t, tc.Run)

	if n := strings.Count(out, "mkvmerge not in PATH"); n != 1 {
		t.Errorf("warning printed %d times, want 1:\n%s", n, out)
	}
}

func TestParseContainerTitle(t *testing.T) {
	for _, tt := range []struct {
		name, json, want, wantErr string
	}{
		{"title", `{"container":{"recognized":true,"properties":{"title":"Show - S01E01 - A"}}}`, "Show - S01E01 - A", ""},
		{"no title", `{"container":{"recognized":true,"properties":{}}}`, "", ""},
		{"unrecognised", `{"container":{"recognized":false},"errors":["The file could not be opened"]}`, "", "could not be opened"},
		{"unrecognised, no errors", `{"container":{"recognized":false}}`, "", "didn't recognise"},
		{"not JSON", `mkvmerge v90`, "", "mkvmerge -J output"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContainerTitle([]byte(tt.json))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v; want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("= %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// Against real MKVToolNix: --title-check replaces a release's own title even
// though the title cache already holds the full title.
func TestTitleCheck_RealMKV_ReplacesReleaseTitle(t *testing.T) {
	dir := t.TempDir()
	path, runner := muxTestMKV(t, dir, "Show - S01E02 - New Title.mkv", "--title", "Release.Group.S01E02")
	if got, err := runner.ReadTitle(path); err != nil || got != "Release.Group.S01E02" {
		t.Fatalf("ReadTitle before = %q, %v; want the release title", got, err)
	}
	titles := newNullTitleCache(t)
	titles.Set(path, wantFullTitle)
	tc := NewTitleChecker(Config{}, nil, nil, nil, nil, runner, titles, noopTimer{}, nil)

	out := captureOutput(t, func() { tc.applyFullTitle(path, wantFullTitle) })

	if got, err := runner.ReadTitle(path); err != nil || got != wantFullTitle {
		t.Errorf("ReadTitle after = %q, %v; want %q:\n%s", got, err, wantFullTitle, out)
	}
}

// Against real MKVToolNix: a file with no title reads as "", and a file that
// isn't Matroska is an error.
func TestRealMKVRunner_ReadTitle(t *testing.T) {
	dir := t.TempDir()
	path, runner := muxTestMKV(t, dir, "untitled.mkv")
	if got, err := runner.ReadTitle(path); err != nil || got != "" {
		t.Errorf("ReadTitle(untitled) = %q, %v; want \"\", nil", got, err)
	}
	if _, err := runner.ReadTitle(debandTestFile(t)); err == nil {
		t.Error("ReadTitle(non-MKV) succeeded; want an error")
	}
}
