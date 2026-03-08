package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeMediaInfoReader returns pre-set dates and title.
type fakeMediaInfoReader struct {
	best   string
	header string
	title  string
	err    error
}

func (f *fakeMediaInfoReader) Query(_ string) (string, string, string, error) {
	return f.best, f.header, f.title, f.err
}

// fakeMKVSyncer records SyncHeader calls.
type fakeMKVSyncer struct {
	calls []fakeSyncCall
	err   error
}

type fakeSyncCall struct {
	dateStr  string
	tagsPath string
}

func (f *fakeMKVSyncer) SyncHeader(_ string, dateStr, tagsPath string) error {
	f.calls = append(f.calls, fakeSyncCall{dateStr, tagsPath})
	return f.err
}

// fakeFileTimer records timestamp operations and returns a controlled ctime check result.
type fakeFileTimer struct {
	ctimeNeedsUpdate     bool
	setTimesCalls        []time.Time
	setCreationTimeCalls []time.Time
}

func (f *fakeFileTimer) CreationTimeNeedsUpdate(_ string, _ int64) bool {
	return f.ctimeNeedsUpdate
}

func (f *fakeFileTimer) SetTimes(_ string, t time.Time) error {
	f.setTimesCalls = append(f.setTimesCalls, t)
	return nil
}

func (f *fakeFileTimer) SetCreationTime(_ string, t time.Time) error {
	f.setCreationTimeCalls = append(f.setCreationTimeCalls, t)
	return nil
}

func makeTempMKV(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.mkv")
	if err != nil {
		t.Fatalf("creating temp mkv: %v", err)
	}
	f.Close()
	return f.Name()
}

// --- parseDate tests ---

func TestParseDate_FullDate(t *testing.T) {
	dt, err := parseDate("2021-06-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2021 || dt.Month() != 6 || dt.Day() != 15 {
		t.Errorf("got %v, want 2021-06-15", dt)
	}
}

func TestParseDate_YearOnly(t *testing.T) {
	dt, err := parseDate("2019")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2019 || dt.Month() != 1 || dt.Day() != 1 {
		t.Errorf("got %v, want 2019-01-01", dt)
	}
}

func TestParseDate_LongString_TruncatesToTen(t *testing.T) {
	dt, err := parseDate("2020-03-25T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2020 || dt.Month() != 3 || dt.Day() != 25 {
		t.Errorf("got %v, want 2020-03-25", dt)
	}
}

func TestParseDate_InvalidYear(t *testing.T) {
	_, err := parseDate("abcd")
	if err == nil {
		t.Error("expected error for invalid year-only string")
	}
}

// --- processFile tests ---

func TestProcessFile_NoDate_Skipped(t *testing.T) {
	path := makeTempMKV(t)
	mi := &fakeMediaInfoReader{best: "", header: ""}
	result := processFile(path, mi, nil, &fakeFileTimer{}, nil)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped", result)
	}
}

func TestProcessFile_MediaInfoError_Error(t *testing.T) {
	path := makeTempMKV(t)
	mi := &fakeMediaInfoReader{err: fmt.Errorf("mediainfo not found")}
	result := processFile(path, mi, nil, &fakeFileTimer{}, nil)
	if result != ResultError {
		t.Errorf("got %v, want ResultError", result)
	}
}

func TestProcessFile_HeaderDiffers_SyncsAndUpdates(t *testing.T) {
	path := makeTempMKV(t)
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	mi := &fakeMediaInfoReader{best: "2021-04-10", header: ""}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer, nil)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(syncer.calls) != 1 || syncer.calls[0].dateStr != "2021-04-10" {
		t.Errorf("SyncHeader calls: %v", syncer.calls)
	}
}

func TestProcessFile_AlreadyCurrent_OK(t *testing.T) {
	path := makeTempMKV(t)
	target := time.Date(2021, 4, 10, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	mi := &fakeMediaInfoReader{best: "2021-04-10", header: "2021-04-10"}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer, nil)
	if result != ResultOK {
		t.Errorf("got %v, want ResultOK", result)
	}
	if len(syncer.calls) != 0 {
		t.Error("SyncHeader should not be called when header is current")
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("SetTimes should not be called when mtime is current")
	}
}

func TestProcessFile_YearOnly_ParsedAndUpdated(t *testing.T) {
	path := makeTempMKV(t)
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	mi := &fakeMediaInfoReader{best: "2018", header: ""}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer, nil)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(syncer.calls) == 0 {
		t.Error("expected SyncHeader to be called")
	}
}

func TestProcessFile_MkvpropeditRuns_AlwaysRestoresMtime(t *testing.T) {
	// mtime is already at the target date, but header differs → mkvpropedit runs.
	// After mkvpropedit the mtime would silently be "now" without the fix.
	// Verify SetTimes is still called.
	path := makeTempMKV(t)
	target := time.Date(2021, 4, 10, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target) // mtime already correct

	mi := &fakeMediaInfoReader{best: "2021-04-10", header: ""} // header wrong
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	processFile(path, mi, syncer, timer, nil)

	if len(timer.setTimesCalls) == 0 {
		t.Error("SetTimes must be called after mkvpropedit even when mtime was already correct")
	}
}

func TestProcessFile_WithRating_TagsWrittenAndTimestampRestored(t *testing.T) {
	path := makeTempMKV(t)
	target := time.Date(2020, 6, 15, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	mi := &fakeMediaInfoReader{best: "2020-06-15", header: "2020-06-15", title: "My Film"}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	dir := t.TempDir()
	cache, _ := LoadMovieRatingCache(filepath.Join(dir, "cache.json"))
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, nil, prompter)
	// Pre-load cache so no prompt is needed.
	cache.Set("My Film:2020", RatingM)

	result := processFile(path, mi, syncer, timer, rr)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated (rating tag written first time)", result)
	}
	// SyncHeader should have been called with a non-empty tagsPath.
	if len(syncer.calls) == 0 || syncer.calls[0].tagsPath == "" {
		t.Error("expected SyncHeader to be called with tagsPath for rating tag")
	}
	// Timestamps must be restored after mkvpropedit.
	if len(timer.setTimesCalls) == 0 {
		t.Error("SetTimes must be called to restore timestamp after rating tag write")
	}
}

func TestProcessFile_WithRating_AlreadyApplied_ShowsOK(t *testing.T) {
	path := makeTempMKV(t)
	target := time.Date(2020, 6, 15, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	mi := &fakeMediaInfoReader{best: "2020-06-15", header: "2020-06-15", title: "My Film"}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	dir := t.TempDir()
	cache, _ := LoadMovieRatingCache(filepath.Join(dir, "cache.json"))
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, nil, prompter)
	cache.Set("My Film:2020", RatingM)
	// Mark as already applied — simulates a second run.
	cache.MarkApplied(path, RatingM)

	result := processFile(path, mi, syncer, timer, rr)
	if result != ResultOK {
		t.Errorf("got %v, want ResultOK (tags already applied, date current)", result)
	}
	if len(syncer.calls) != 0 {
		t.Error("SyncHeader must not be called when tags are already applied and date is current")
	}
}

// fakePlexMovieClient is a test double for PlexMovieClient.
type fakePlexMovieClient struct {
	titleRatingKey   string
	findTitleErr     error
	contentRating    NZRating
	contentRatingErr error
}

func (f *fakePlexMovieClient) FindMovieByTMDBID(_ string) (string, error) {
	return "", ErrPlexMovieNotFound
}

func (f *fakePlexMovieClient) FindMovieByTitle(_, _ string) (string, error) {
	return f.titleRatingKey, f.findTitleErr
}

func (f *fakePlexMovieClient) GetContentRating(_ string) (NZRating, error) {
	return f.contentRating, f.contentRatingErr
}

func (f *fakePlexMovieClient) SetContentRating(_ string, _ NZRating) error { return nil }
func (f *fakePlexMovieClient) RefreshLibraries() error                     { return nil }

func TestResolveMovieRating_BlankAcceptsPlexDefault(t *testing.T) {
	dir := t.TempDir()
	cache, _ := LoadMovieRatingCache(filepath.Join(dir, "cache.json"))

	plex := &fakePlexMovieClient{titleRatingKey: "77", contentRating: RatingR16}
	// Prompter immediately hits Enter (blank).
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, plex, prompter)

	rating := rr.ResolveMovieRating("Some Film:2022", "Some Film")
	if rating != RatingR16 {
		t.Errorf("got %q, want R16 (Plex default)", rating)
	}
	// Rating must be cached.
	cached, ok := cache.Get("Some Film:2022")
	if !ok || cached != RatingR16 {
		t.Errorf("cache has %q ok=%v, want R16", cached, ok)
	}
}

func TestResolveMovieRating_BlankWithNoPlexDefault_Skips(t *testing.T) {
	dir := t.TempDir()
	cache, _ := LoadMovieRatingCache(filepath.Join(dir, "cache.json"))

	plex := &fakePlexMovieClient{titleRatingKey: "77", contentRating: RatingNone}
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, plex, prompter)

	rating := rr.ResolveMovieRating("Some Film:2022", "Some Film")
	if rating != RatingNone {
		t.Errorf("got %q, want RatingNone when Plex has no rating and user skips", rating)
	}
}

func TestResolveMovieRating_InvalidInput_RePrompts(t *testing.T) {
	dir := t.TempDir()
	cache, _ := LoadMovieRatingCache(filepath.Join(dir, "cache.json"))

	calls := 0
	prompter := func(string) (string, error) {
		calls++
		if calls == 1 {
			return "PG-13", nil // US rating, not valid NZ
		}
		return "R16", nil // valid on second try
	}
	rr := newMovieRatingResolver(nil, cache, false, nil, prompter)

	rating := rr.ResolveMovieRating("title:2020", "Some Movie")
	if rating != RatingR16 {
		t.Errorf("got %q, want R16", rating)
	}
	if calls != 2 {
		t.Errorf("prompter called %d times, want 2", calls)
	}
	// Rating must be cached after valid entry.
	cached, ok := cache.Get("title:2020")
	if !ok || cached != RatingR16 {
		t.Errorf("cache has %q ok=%v, want R16", cached, ok)
	}
}

// --- walkAndProcess tests ---

func TestWalkAndProcess_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte{}, 0644)

	mi := &fakeMediaInfoReader{best: ""}
	stats := walkAndProcess(dir, mi, nil, &fakeFileTimer{}, nil)
	if stats.Skipped != 0 || stats.Updated != 0 || stats.Errors != 0 {
		t.Errorf("unexpected stats for non-mkv file: %+v", stats)
	}
}

func TestWalkAndProcess_CountsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte{}, 0644)

	mi := &fakeMediaInfoReader{best: ""}
	stats := walkAndProcess(dir, mi, nil, &fakeFileTimer{}, nil)
	if stats.Skipped != 1 {
		t.Errorf("got Skipped=%d, want 1", stats.Skipped)
	}
}
