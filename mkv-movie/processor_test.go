package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeTMDBMovieClient returns pre-set TMDB ID and details.
type fakeTMDBMovieClient struct {
	tmdbID  string
	details MovieDetails
	err     error
}

func (f *fakeTMDBMovieClient) SearchAndFetch(_, _ string) (string, MovieDetails, error) {
	return f.tmdbID, f.details, f.err
}

func (f *fakeTMDBMovieClient) GetMovieDetails(_ string) (MovieDetails, error) {
	return f.details, f.err
}

// fakeApplier records Apply calls.
type fakeApplier struct {
	calls []fakeApplyCall
	err   error
}

type fakeApplyCall struct {
	title      string
	dateStr    string
	tagsPath   string
	posterPath string
}

func (f *fakeApplier) Apply(_ string, title, dateStr, tagsPath, posterPath string) error {
	f.calls = append(f.calls, fakeApplyCall{title, dateStr, tagsPath, posterPath})
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

// makeTempMKVNamed creates a temp .mkv file with a specific name.
func makeTempMKVNamed(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("creating temp mkv %s: %v", name, err)
	}
	return path
}

func makeCache(t *testing.T) *MovieRatingCache {
	t.Helper()
	c, err := LoadMovieRatingCache(filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatalf("LoadMovieRatingCache: %v", err)
	}
	return c
}

func testDetails(releaseDate string) MovieDetails {
	return MovieDetails{
		TMDBID:      "12345",
		Title:       "Avengers Endgame",
		Overview:    "Heroes assemble.",
		ReleaseDate: releaseDate,
		PosterPath:  "",
	}
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

func TestProcessFile_TMDBSearchError_Error(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	tmdb := &fakeTMDBMovieClient{err: fmt.Errorf("network error")}
	result := processFile(path, tmdb, nil, &fakeFileTimer{}, nil, makeCache(t), false)
	if result != ResultError {
		t.Errorf("got %v, want ResultError", result)
	}
}

func TestProcessFile_TMDBNotFound_Skipped(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	tmdb := &fakeTMDBMovieClient{tmdbID: "", details: MovieDetails{}}
	result := processFile(path, tmdb, nil, &fakeFileTimer{}, nil, makeCache(t), false)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped", result)
	}
}

func TestProcessFile_NoReleaseDate_Skipped(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: MovieDetails{TMDBID: "12345", Title: "Avengers Endgame"}}
	result := processFile(path, tmdb, nil, &fakeFileTimer{}, nil, makeCache(t), false)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped", result)
	}
}

func TestProcessFile_AlreadyApplied_MtimeCurrent_OK(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	target := time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	// Simulate file already fully processed.
	cache.SetTMDBID("Avengers Endgame:2019", "12345")
	cache.MarkMetaApplied(path, "12345")

	result := processFile(path, tmdb, applier, timer, nil, cache, false)
	if result != ResultOK {
		t.Errorf("got %v, want ResultOK", result)
	}
	if len(applier.calls) != 0 {
		t.Error("Apply should not be called when already applied and mtime current")
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("SetTimes should not be called when mtime is already correct")
	}
}

func TestProcessFile_NotApplied_AppliesMetadata(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)

	result := processFile(path, tmdb, applier, timer, nil, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(applier.calls) != 1 {
		t.Errorf("expected 1 Apply call, got %d", len(applier.calls))
	}
	if applier.calls[0].title != "Avengers Endgame" {
		t.Errorf("Apply title = %q, want %q", applier.calls[0].title, "Avengers Endgame")
	}
	if applier.calls[0].dateStr != "2019-04-26" {
		t.Errorf("Apply dateStr = %q, want %q", applier.calls[0].dateStr, "2019-04-26")
	}
	if len(timer.setTimesCalls) == 0 {
		t.Error("SetTimes should be called after apply")
	}
	// TMDB ID and meta-applied should be persisted.
	if cache.GetTMDBID("Avengers Endgame:2019") != "12345" {
		t.Error("TMDB ID should be cached after search")
	}
	if !cache.IsMetaApplied(path, "12345") {
		t.Error("meta should be marked applied after successful apply")
	}
}

func TestProcessFile_AppliedButMtimeWrong_FixesTimestamps(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	// mtime is wrong even though metadata was already applied.
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	cache.SetTMDBID("Avengers Endgame:2019", "12345")
	cache.MarkMetaApplied(path, "12345")

	result := processFile(path, tmdb, applier, timer, nil, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated (mtime wrong)", result)
	}
	// No Apply call needed (meta already applied).
	if len(applier.calls) != 0 {
		t.Error("Apply should not be called when only mtime needs fixing")
	}
	if len(timer.setTimesCalls) == 0 {
		t.Error("SetTimes should be called to correct mtime")
	}
}

func TestProcessFile_Overwrite_ReappliesEvenIfApplied(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	target := time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	cache.SetTMDBID("Avengers Endgame:2019", "12345")
	cache.MarkMetaApplied(path, "12345")

	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, true /* overwrite */, nil, prompter)

	result := processFile(path, tmdb, applier, timer, rr, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated (overwrite forces re-apply)", result)
	}
	if len(applier.calls) != 1 {
		t.Errorf("expected 1 Apply call under overwrite, got %d", len(applier.calls))
	}
}

func TestProcessFile_MkvpropeditRuns_AlwaysRestoresMtime(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	target := time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target) // mtime already correct

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	// Not yet applied — mkvpropedit will run.

	processFile(path, tmdb, applier, timer, nil, cache, false)

	if len(timer.setTimesCalls) == 0 {
		t.Error("SetTimes must be called after mkvpropedit even when mtime was already correct")
	}
}

func TestProcessFile_WithRating_TagsPathSet(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	cache.Set("12345", RatingM)

	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, nil, prompter)

	result := processFile(path, tmdb, applier, timer, rr, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(applier.calls) == 0 || applier.calls[0].tagsPath == "" {
		t.Error("Apply should be called with a non-empty tagsPath when rating is set")
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
	cache := makeCache(t)
	plex := &fakePlexMovieClient{titleRatingKey: "77", contentRating: RatingR16}
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, plex, prompter)

	rating := rr.ResolveMovieRating("Some Film:2022", "Some Film")
	if rating != RatingR16 {
		t.Errorf("got %q, want R16 (Plex default)", rating)
	}
	cached, ok := cache.Get("Some Film:2022")
	if !ok || cached != RatingR16 {
		t.Errorf("cache has %q ok=%v, want R16", cached, ok)
	}
}

func TestResolveMovieRating_BlankWithNoPlexDefault_Skips(t *testing.T) {
	cache := makeCache(t)
	plex := &fakePlexMovieClient{titleRatingKey: "77", contentRating: RatingNone}
	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver(nil, cache, false, plex, prompter)

	rating := rr.ResolveMovieRating("Some Film:2022", "Some Film")
	if rating != RatingNone {
		t.Errorf("got %q, want RatingNone when Plex has no rating and user skips", rating)
	}
}

func TestResolveMovieRating_InvalidInput_RePrompts(t *testing.T) {
	cache := makeCache(t)
	calls := 0
	prompter := func(string) (string, error) {
		calls++
		if calls == 1 {
			return "PG-13", nil // US rating, not valid NZ
		}
		return "R16", nil
	}
	rr := newMovieRatingResolver(nil, cache, false, nil, prompter)

	rating := rr.ResolveMovieRating("title:2020", "Some Movie")
	if rating != RatingR16 {
		t.Errorf("got %q, want R16", rating)
	}
	if calls != 2 {
		t.Errorf("prompter called %d times, want 2", calls)
	}
	cached, ok := cache.Get("title:2020")
	if !ok || cached != RatingR16 {
		t.Errorf("cache has %q ok=%v, want R16", cached, ok)
	}
}

// --- walkAndProcess tests ---

func TestWalkAndProcess_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte{}, 0644)

	tmdb := &fakeTMDBMovieClient{}
	cache := makeCache(t)
	stats := walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, false)
	if stats.Skipped != 0 || stats.Updated != 0 || stats.Errors != 0 {
		t.Errorf("unexpected stats for non-mkv file: %+v", stats)
	}
}

func TestWalkAndProcess_CountsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte{}, 0644)

	// TMDB finds nothing → skipped.
	tmdb := &fakeTMDBMovieClient{tmdbID: ""}
	cache := makeCache(t)
	stats := walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, false)
	if stats.Skipped != 1 {
		t.Errorf("got Skipped=%d, want 1", stats.Skipped)
	}
}
