package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// setFoldPathCase sets foldPathCase for the test.
func setFoldPathCase(t *testing.T, fold bool) {
	t.Helper()
	old := foldPathCase
	foldPathCase = fold
	t.Cleanup(func() { foldPathCase = old })
}

// savedRetryPaths returns the paths r's file holds.
func savedRetryPaths(t *testing.T, r *RetryList) []string {
	t.Helper()
	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range entries {
		paths = append(paths, p)
	}
	return paths
}

func TestRetryList_FoldsCaseOnlyOnWindows(t *testing.T) {
	if foldPathCase != (runtime.GOOS == "windows") {
		t.Errorf("foldPathCase = %v on %s", foldPathCase, runtime.GOOS)
	}
}

// With case folding, every lookup matches a path in any case, and Save writes
// the spelling last passed in.
func TestRetryList_CaseInsensitiveLookups(t *testing.T) {
	setFoldPathCase(t, true)
	r := emptyRetryList(t)
	lower, upper := `d:\tv\show\a.mkv`, `D:\TV\Show\A.mkv`

	r.Add(lower)
	if !r.Has(upper) {
		t.Fatal("Has misses the path in another case")
	}
	r.Fail(upper)
	r.Add(lower) // keeps the count
	if r.Attempts(upper) != 1 || r.Len() != 1 {
		t.Fatalf("attempts = %d, len = %d; want 1, 1", r.Attempts(upper), r.Len())
	}
	mustSave(t, r.Save())
	if got := savedRetryPaths(t, r); len(got) != 1 || got[0] != lower {
		t.Errorf("saved %v; want [%s]", got, lower)
	}
	r.Keep(upper)
	if r.Attempts(lower) != 0 {
		t.Errorf("Keep in another case left attempts = %d", r.Attempts(lower))
	}
	r.Remove(upper)
	if r.Has(lower) {
		t.Error("Remove in another case left the entry")
	}
}

// Without case folding, paths that differ only in case are separate entries.
func TestRetryList_CaseSensitiveLookups(t *testing.T) {
	setFoldPathCase(t, false)
	r := emptyRetryList(t)
	r.Add("/tv/show/a.mkv")
	if r.Has("/TV/Show/A.mkv") {
		t.Error("Has matched a path in another case")
	}
}

// Two spellings of one path in a saved list load as one entry with the higher
// count, and save as that entry's spelling; without folding they stay apart.
func TestRetryList_LoadMergesCaseCollisions(t *testing.T) {
	for _, tc := range []struct {
		name string
		fold bool
		len  int
	}{{"folding", true, 1}, {"not folding", false, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			setFoldPathCase(t, tc.fold)
			path := filepath.Join(t.TempDir(), "pending_dates.json")
			upper, lower := `D:\TV\a.mkv`, `d:\tv\a.mkv`
			if err := os.WriteFile(path, []byte(`{"D:\\TV\\a.mkv": 1, "d:\\tv\\a.mkv": 3}`), 0o644); err != nil {
				t.Fatal(err)
			}
			r, err := LoadRetryList(path)
			if err != nil {
				t.Fatal(err)
			}
			if r.Len() != tc.len || r.Attempts(lower) != 3 {
				t.Fatalf("len = %d, attempts = %d; want %d, 3", r.Len(), r.Attempts(lower), tc.len)
			}
			if !tc.fold {
				return
			}
			if r.Attempts(upper) != 3 {
				t.Errorf("attempts via %s = %d; want 3", upper, r.Attempts(upper))
			}
			mustSave(t, r.Save())
			if got := savedRetryPaths(t, r); len(got) != 1 || got[0] != lower {
				t.Errorf("saved %v; want [%s]", got, lower)
			}
		})
	}
}

// A file on the retry list under another case is retried although it is
// older than the cutoff, and leaves the list once it succeeds.
func TestRun_RetryListMatchesPathInAnotherCase(t *testing.T) {
	setFoldPathCase(t, true)
	dir, file, retryPath := oldStepShow(t)
	r := newRetryList(retryPath)
	r.Add(strings.ToUpper(file))
	mustSave(t, r.Save())

	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterPath: "p.jpg"}
	stats, _ := stepShowRun(t, dir, retryPath, time.Now(), "2020-01-01", runner)

	if len(runner.calls) != 1 || stats.Success != 1 {
		t.Errorf("Apply calls = %d, stats = %+v; want the old file processed", len(runner.calls), stats)
	}
	if r, _ := LoadRetryList(retryPath); r.Len() != 0 {
		t.Errorf("retry list keeps %v; want it empty", r.data)
	}
}

// Move carries an entry's count to the new path, and does nothing for a path
// not on the list.
func TestRetryList_Move(t *testing.T) {
	r := emptyRetryList(t)
	r.Fail("/tv/a.mkv")
	r.Add("/tv/c.mkv")

	if !r.Move("/tv/a.mkv", "/tv/b.mkv") {
		t.Error("Move of a listed path reported false")
	}
	if r.Has("/tv/a.mkv") || r.Attempts("/tv/b.mkv") != 1 {
		t.Errorf("after Move: %v; want b with 1 failed run", r.data)
	}
	if r.Move("/tv/x.mkv", "/tv/y.mkv") || r.Has("/tv/y.mkv") || r.Len() != 2 {
		t.Errorf("Move of an unlisted path changed the list: %v", r.data)
	}
}
