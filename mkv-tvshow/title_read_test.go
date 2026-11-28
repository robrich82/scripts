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
// skips the write, a missing or different one sets it.
func TestTitleCheck_UnreadableTitle_TrustsCache(t *testing.T) {
	const readErrOut = "Can't read the MKV title of Show - S01E02 - New Title.mkv"
	for _, tt := range []struct {
		name      string
		err       error
		cached    string
		wantCalls int
		wantOut   string
	}{
		{"no mkvmerge, cached", errNoMKVMerge, wantFullTitle, 0, "mkvmerge not in PATH"},
		{"no mkvmerge, not cached", errNoMKVMerge, "", 1, "mkvmerge not in PATH"},
		{"read error, cached", errors.New("boom"), wantFullTitle, 0, readErrOut},
		{"read error, not cached", errors.New("boom"), "", 1, readErrOut},
		{"read error, stale cache", errors.New("boom"), "Show - S01E02 - Old", 1, readErrOut},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeMKVRunner{avail: true, applyResult: "Done.", readTitleErr: tt.err}
			titles := newNullTitleCache(t)
			tc, path := titleCheckLibrary(t, runner, titles)
			if tt.cached != "" {
				titles.Set(path, tt.cached)
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

// The JSON shapes are mkvmerge v103's: an unopenable file has no container
// key, and one it doesn't recognise exits 0.
func TestTitleFromIdentify(t *testing.T) {
	const titled = `{"container":{"recognized":true,"properties":{"title":"Show - S01E01 - A"}},"errors":[]}`
	for _, tt := range []struct {
		name, json string
		rc         int
		want       string
		wantErr    string
	}{
		{"title", titled, 0, "Show - S01E01 - A", ""},
		{"warnings only", titled, 1, "Show - S01E01 - A", ""},
		{"no title", `{"container":{"recognized":true,"properties":{}},"errors":[]}`, 0, "", ""},
		{"error exit", titled, 2, "", "exit code 2"},
		{"killed", titled, -1, "", "exit code -1"},
		{"unopenable", `{"errors":["The file 'x.mkv' could not be opened for reading: open file error.\n","Second.\n"],"warnings":[]}`, 2,
			"", "mkvmerge: The file 'x.mkv' could not be opened for reading: open file error.; Second."},
		{"unrecognised", `{"container":{"recognized":false,"supported":false},"errors":[]}`, 0, "", "didn't recognise"},
		{"not JSON", `mkvmerge v90`, 2, "", "mkvmerge -J (exit code 2) output"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := titleFromIdentify([]byte(tt.json), tt.rc)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "\n") {
					t.Fatalf("err = %q; want one containing %q, on one line", err, tt.wantErr)
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

func TestRealMKVRunner_ReadTitle_NoMKVMerge(t *testing.T) {
	if _, err := (&realMKVRunner{}).ReadTitle("x.mkv"); !errors.Is(err, errNoMKVMerge) {
		t.Errorf("err = %v; want errNoMKVMerge", err)
	}
}

// Against real MKVToolNix: a file with no title reads as "", and a file that
// isn't Matroska or can't be opened is an error.
func TestRealMKVRunner_ReadTitle(t *testing.T) {
	dir := t.TempDir()
	path, runner := muxTestMKV(t, dir, "untitled.mkv")
	if got, err := runner.ReadTitle(path); err != nil || got != "" {
		t.Errorf("ReadTitle(untitled) = %q, %v; want \"\", nil", got, err)
	}
	if _, err := runner.ReadTitle(debandTestFile(t)); err == nil || !strings.Contains(err.Error(), "didn't recognise") {
		t.Errorf("ReadTitle(non-MKV) err = %v; want didn't recognise", err)
	}
	if _, err := runner.ReadTitle(filepath.Join(dir, "missing.mkv")); err == nil || !strings.Contains(err.Error(), "could not be opened") {
		t.Errorf("ReadTitle(missing) err = %v; want could not be opened", err)
	}
}
