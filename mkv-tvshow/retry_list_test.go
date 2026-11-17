package main

import (
	"encoding/json"
	"errors"
	"maps"
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

// retryClockStart is the day setRetryClock starts the retry list's clock on.
var retryClockStart = time.Date(2026, 11, 18, 12, 0, 0, 0, time.Local)

// setRetryClock sets the retry list's clock to retryClockStart and returns a
// func that moves it on by days.
func setRetryClock(t *testing.T) (advance func(days int)) {
	t.Helper()
	old, day := retryNow, 0
	retryNow = func() time.Time { return retryClockStart.AddDate(0, 0, day) }
	t.Cleanup(func() { retryNow = old })
	return func(days int) { day += days }
}

// The cap needs both maxRetryAttempts runs in a row and maxRetryDays since
// the first of them.
func TestRetryList_CapNeedsRunsAndDays(t *testing.T) {
	t.Run("runs within the days", func(t *testing.T) {
		advance := setRetryClock(t)
		r := emptyRetryList(t)
		for run := 1; run <= maxRetryAttempts+1; run++ {
			if gaveUp, _, _ := r.Fail("/tv/a.mkv"); gaveUp {
				t.Fatalf("gave up after %d runs within %d days", run, run-1)
			}
			advance(1)
		}
		// Runs 1–6 fell on days 0–5; run 7 is on day 6, still inside the 7 days.
		if gaveUp, _, _ := r.Fail("/tv/a.mkv"); gaveUp {
			t.Fatal("gave up on day 6")
		}
		advance(1)
		gaveUp, runs, days := r.Fail("/tv/a.mkv")
		if !gaveUp || runs != 8 || days != maxRetryDays || r.Has("/tv/a.mkv") {
			t.Errorf("day 7: gaveUp %v, runs %d, days %d, on list %v; want true, 8, 7, false", gaveUp, runs, days, r.Has("/tv/a.mkv"))
		}
	})
	t.Run("days before the runs", func(t *testing.T) {
		advance := setRetryClock(t)
		r := emptyRetryList(t)
		r.Fail("/tv/a.mkv")
		advance(maxRetryDays + 3)
		for run := 2; run < maxRetryAttempts; run++ {
			if gaveUp, _, _ := r.Fail("/tv/a.mkv"); gaveUp {
				t.Fatalf("gave up after %d runs", run)
			}
		}
		if gaveUp, runs, days := r.Fail("/tv/a.mkv"); !gaveUp || runs != maxRetryAttempts || days != maxRetryDays+3 {
			t.Errorf("run 5: gaveUp %v, runs %d, days %d; want true, 5, 10", gaveUp, runs, days)
		}
	})
}

// A run with no failed step, or leaving the list, starts a new streak.
func TestRetryList_StreakResets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset func(r *RetryList, path string)
	}{
		{"Keep", (*RetryList).Keep},
		{"Remove", func(r *RetryList, path string) { r.Remove(path); r.Add(path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			advance := setRetryClock(t)
			r := emptyRetryList(t)
			for range maxRetryAttempts - 1 {
				r.Fail("/tv/a.mkv")
			}
			tc.reset(r, "/tv/a.mkv")
			advance(maxRetryDays)
			for run := 1; run <= maxRetryAttempts; run++ {
				if gaveUp, _, _ := r.Fail("/tv/a.mkv"); gaveUp {
					t.Fatalf("gave up on run %d of the new streak, its first day", run)
				}
			}
		})
	}
}

// The retry list reads the older count form, dating a count from the load,
// and saves the current form; the current form keeps its date.
func TestRetryList_LoadsCountFormAndSavesObjects(t *testing.T) {
	advance := setRetryClock(t)
	path := filepath.Join(t.TempDir(), "pending_dates.json")
	if err := os.WriteFile(path, []byte(`{"/tv/a.mkv": 4, "/tv/b.mkv": 0, "/tv/c.mkv": {"fails": 2}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRetryList(path)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, r.Save())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]retryJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]retryJSON{
		"/tv/a.mkv": {Fails: 4, Since: "2026-11-18"},
		"/tv/b.mkv": {Fails: 0},
		"/tv/c.mkv": {Fails: 2, Since: "2026-11-18"},
	}
	if !maps.Equal(got, want) {
		t.Errorf("saved %v; want %v", got, want)
	}

	// A week later the saved date still counts from the first load.
	advance(maxRetryDays)
	r, err = LoadRetryList(path)
	if err != nil {
		t.Fatal(err)
	}
	if gaveUp, runs, days := r.Fail("/tv/a.mkv"); !gaveUp || runs != 5 || days != maxRetryDays {
		t.Errorf("Fail = %v, %d, %d; want true, 5, 7", gaveUp, runs, days)
	}
}

// A date that doesn't parse fails the load and names the entry.
func TestRetryList_BadSinceFailsLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending_dates.json")
	if err := os.WriteFile(path, []byte(`{"/tv/a.mkv": {"fails": 1, "since": "18/11/2026"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRetryList(path); err == nil || !strings.Contains(err.Error(), "/tv/a.mkv") {
		t.Errorf("err = %v; want one naming the entry", err)
	}
}

// Move keeps the date of the first failed run.
func TestRetryList_MoveKeepsSince(t *testing.T) {
	advance := setRetryClock(t)
	r := emptyRetryList(t)
	r.Fail("/tv/a.mkv")
	advance(maxRetryDays)
	r.Move("/tv/a.mkv", "/tv/b.mkv")
	for range maxRetryAttempts - 2 {
		r.Fail("/tv/b.mkv")
	}
	if gaveUp, _, days := r.Fail("/tv/b.mkv"); !gaveUp || days != maxRetryDays {
		t.Errorf("Fail = %v over %d days; want it to give up over 7", gaveUp, days)
	}
}

// A file that fails a step on 5 runs within a few days stays on the list.
func TestRun_RetryListKeepsFileWithinDays(t *testing.T) {
	dir, file, retryPath := oldStepShow(t)
	advance := setRetryClock(t)
	r := newRetryList(retryPath)
	r.Add(file)
	mustSave(t, r.Save())
	for run := 1; run <= maxRetryAttempts; run++ {
		_, out := stepShowRun(t, dir, retryPath, time.Now(), "2020-01-01",
			&fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml", posterErr: errors.New("HTTP 503")})
		if strings.Contains(out, "Not retrying") {
			t.Fatalf("run %d, on day %d, gave up:\n%s", run, run-1, out)
		}
		advance(1)
	}
	if r, _ := LoadRetryList(retryPath); !r.Has(file) || r.Attempts(file) != maxRetryAttempts {
		t.Errorf("on list = %v, attempts = %d; want true, %d", r.Has(file), r.Attempts(file), maxRetryAttempts)
	}
}
