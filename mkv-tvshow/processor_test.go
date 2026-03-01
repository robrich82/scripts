package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureFileTimer records SetTimes and SetCreationTime calls without touching
// real files.
type captureFileTimer struct {
	setTimesCalls        []timerCall
	setCreationTimeCalls []timerCall
}

type timerCall struct {
	path string
	t    time.Time
}

func (c *captureFileTimer) SetTimes(path string, t time.Time) error {
	c.setTimesCalls = append(c.setTimesCalls, timerCall{path, t})
	return nil
}

func (c *captureFileTimer) SetCreationTime(path string, t time.Time) error {
	c.setCreationTimeCalls = append(c.setCreationTimeCalls, timerCall{path, t})
	return nil
}

// fakeTMDB is a test double for TMDBClient with fixed return values.
type fakeTMDB struct {
	searchResults    []ShowResult
	searchErr        error
	showDetails      ShowDetails
	showErr          error
	seasonResult     SeasonResult
	seasonErr        error
	contentRating    NZRating
	contentRatingErr error
}

func (f *fakeTMDB) SearchTV(_ string) ([]ShowResult, error) {
	return f.searchResults, f.searchErr
}
func (f *fakeTMDB) GetShow(_ string) (ShowDetails, error) {
	return f.showDetails, f.showErr
}
func (f *fakeTMDB) GetSeason(_ string, _ int) (SeasonResult, error) {
	return f.seasonResult, f.seasonErr
}
func (f *fakeTMDB) GetContentRatings(_ string) (NZRating, error) {
	return f.contentRating, f.contentRatingErr
}

// fakeTMDB also implements RatingSource so it can be used in ratingSources slices.
func (f *fakeTMDB) Name() string { return "fakeTMDB" }
func (f *fakeTMDB) FetchShowRating(_ string, _ string) (NZRating, error) {
	return f.contentRating, f.contentRatingErr
}
func (f *fakeTMDB) FetchEpisodeRating(_, _ string, _, _ int) (NZRating, error) {
	return RatingNone, nil
}

// helpers

func newTestProcessor(t *testing.T, dir string, cfg Config, cache *ShowCache, tmdb TMDBClient, mkv MKVRunner, prompter Prompter) (*Processor, *captureFileTimer) {
	t.Helper()
	if cfg.MediaDir == "" {
		cfg.MediaDir = dir
	}
	timer := &captureFileTimer{}
	rc, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))
	return NewProcessor(cfg, cache, map[string]ManualEntry{}, tmdb, mkv, prompter, timer, emptyRetryList(t), rc, nil, nil), timer
}

func emptyRetryList(t *testing.T) *RetryList {
	t.Helper()
	return &RetryList{path: filepath.Join(t.TempDir(), "pending_dates.json"), data: make(map[string]bool)}
}

func emptyRatingCache(t *testing.T) *RatingCache {
	t.Helper()
	rc, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))
	return rc
}

func emptyCache(t *testing.T) *ShowCache {
	t.Helper()
	return &ShowCache{path: filepath.Join(t.TempDir(), "cache.json"), data: make(map[string]string)}
}

func cacheWith(t *testing.T, entries map[string]string) *ShowCache {
	t.Helper()
	return &ShowCache{path: filepath.Join(t.TempDir(), "cache.json"), data: entries}
}

// --- resolveShowID ---

func TestResolveShowID_AlreadyInCache_ProcessAll(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show A": "111"})
	p, _ := newTestProcessor(t, "", Config{ProcessAll: true}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{})

	id := p.resolveShowID("Show A")
	if id != "111" {
		t.Errorf("got %q, want \"111\"", id)
	}
}

func TestResolveShowID_AlreadyInCache_PromptYes(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show B": "222"})
	p, _ := newTestProcessor(t, "", Config{}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"y"}})

	id := p.resolveShowID("Show B")
	if id != "222" {
		t.Errorf("got %q, want \"222\"", id)
	}
}

func TestResolveShowID_AlreadyInCache_PromptNo(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show C": "333"})
	p, _ := newTestProcessor(t, "", Config{}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"n"}})

	id := p.resolveShowID("Show C")
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP", id)
	}
	if !p.sessionSkipped["Show C"] {
		t.Error("expected Show C to be in sessionSkipped")
	}
}

func TestResolveShowID_NewShow_AcceptFirstResult(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 456, Name: "New Show", FirstAirDate: "2021-01-01"}}}
	p, _ := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"y"}})

	id := p.resolveShowID("New Show")
	if id != "456" {
		t.Errorf("got %q, want \"456\"", id)
	}
	if cached, _ := cache.Get("New Show"); cached != "456" {
		t.Errorf("cache not updated: got %q", cached)
	}
}

func TestResolveShowID_NewShow_ManualID(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 1, Name: "Wrong Show"}}}
	p, _ := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"99999"}})

	id := p.resolveShowID("My Show")
	if id != "99999" {
		t.Errorf("got %q, want \"99999\"", id)
	}
}

func TestResolveShowID_NewShow_SkipAnswer(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 1, Name: "A Show"}}}
	p, _ := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"s"}})

	id := p.resolveShowID("My Show")
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP", id)
	}
}

func TestResolveShowID_NewShow_NoResults_ManualID(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: nil}
	p, _ := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"77777"}})

	id := p.resolveShowID("Obscure Show")
	if id != "77777" {
		t.Errorf("got %q, want \"77777\"", id)
	}
}

func TestResolveShowID_SessionSkipped_NoRePrompt(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 1}}}
	prompter := &scriptedPrompter{answers: []string{"s"}} // only one answer queued
	p, _ := newTestProcessor(t, "", Config{}, cache, tmdb, &fakeMKVRunner{}, prompter)

	p.resolveShowID("Skipped Show") // consumes the one answer
	id := p.resolveShowID("Skipped Show") // should not prompt again
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP", id)
	}
}

// --- resolveEpisode ---

func TestResolveEpisode_NoManualData(t *testing.T) {
	p := &Processor{}
	id, s, e := p.resolveEpisode("Unknown Show", "100", 2, 5)
	if id != "100" || s != 2 || e != 5 {
		t.Errorf("got id=%q s=%d e=%d, want id=100 s=2 e=5", id, s, e)
	}
}

func TestResolveEpisode_FullMetalPanic_Mapping(t *testing.T) {
	p := &Processor{manualData: map[string]ManualEntry{
		"Full Metal Panic!": {
			Mapping: map[string]EpisodePair{"S02E01": {0, 1}},
		},
	}}
	_, s, e := p.resolveEpisode("Full Metal Panic!", "originalID", 2, 1)
	if s != 0 || e != 1 {
		t.Errorf("S02E01 should map to (0,1), got (%d,%d)", s, e)
	}
}

func TestResolveEpisode_PantyStocking_SeasonOverride(t *testing.T) {
	p := &Processor{manualData: map[string]ManualEntry{
		"Panty & Stocking with Garterbelt": {
			Overrides: map[string]SeasonOverride{
				"S02": {ID: "246862", MapSeason: 1},
			},
		},
	}}
	id, s, _ := p.resolveEpisode("Panty & Stocking with Garterbelt", "original", 2, 1)
	if id != "246862" {
		t.Errorf("got id %q, want \"246862\"", id)
	}
	if s != 1 {
		t.Errorf("got tmdbSeason %d, want 1", s)
	}
}

// --- normalizeFilename ---

func TestNormalizeFilename_TwoEpisodes_NoRename(t *testing.T) {
	dir := t.TempDir()
	name := "Show - S01E01E02.mkv"
	os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)

	p := &Processor{}
	got, _ := p.normalizeFilename(dir, name)
	if got != name {
		t.Errorf("got %q, want %q (should not rename 2-episode file)", got, name)
	}
}

func TestNormalizeFilename_ThreeEpisodes_Renamed(t *testing.T) {
	dir := t.TempDir()
	name := "Show - S01E01E02E03.mkv"
	os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)

	p := &Processor{}
	got, _ := p.normalizeFilename(dir, name)
	want := "Show - S01E01-E03.mkv"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- applyMKV ---

func TestApplyMKV_MkvpropeditNotFound(t *testing.T) {
	runner := &fakeMKVRunner{avail: false}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := p.applyMKV("file.mkv", "Title", "", "id", "", "", "", RatingNone)
	if ok {
		t.Error("expected false when mkvpropedit not available")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called when unavailable")
	}
}

func TestApplyMKV_Success_ArgsIncludeEditAndTitle(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: mkvpropeditSuccessMsg,
		tagsPath:    "tags.xml",
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "My Title", "", "", "", "", "", RatingNone)

	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	args := runner.calls[0].args
	if !containsAll(args, "--edit", "info", "--set", "title=My Title") {
		t.Errorf("missing expected args in: %v", args)
	}
}

func TestApplyMKV_WithDate_SetsDateArg(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "2021-05-10T00:00:00Z", "", "", RatingNone)

	args := runner.calls[0].args
	if !containsAll(args, "--set", "date=2021-05-10T00:00:00Z") {
		t.Errorf("missing date arg in: %v", args)
	}
}

func TestApplyMKV_WithPoster_AddsAttachment(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: mkvpropeditSuccessMsg,
		tagsPath:    "t.xml",
		posterPath:  "poster.jpg",
		posterOK:    true,
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "", "", "http://example.com/poster.jpg", RatingNone)

	args := runner.calls[0].args
	if !containsAll(args, "--add-attachment", "poster.jpg") {
		t.Errorf("missing attachment args in: %v", args)
	}
}

func TestApplyMKV_NoPosterURL_NoAttachment(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "", "", "", RatingNone)

	args := runner.calls[0].args
	for _, a := range args {
		if strings.Contains(a, "attachment") {
			t.Errorf("unexpected attachment arg %q when no poster URL given", a)
		}
	}
}

func TestApplyMKV_ExistingJPEG_DeleteArgAdded(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:       true,
		attachments: AttachmentInfo{HasJPEG: true},
		applyResult: mkvpropeditSuccessMsg,
		tagsPath:    "t.xml",
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "", "", "", RatingNone)

	args := runner.calls[0].args
	if !containsAll(args, "--delete-attachment", "mime-type:image/jpeg") {
		t.Errorf("expected JPEG delete arg in: %v", args)
	}
}

func TestApplyMKV_Success_SetsTimestamps(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 0, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	p.applyMKV("show.mkv", "T", "", "", "2020-06-15T00:00:00Z", "2020-06-15T00:00:00Z", "", RatingNone)

	want := time.Date(2020, 6, 15, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called")
	}
	if !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("SetTimes got %v, want %v", timer.setTimesCalls[0].t, want)
	}
	if len(timer.setCreationTimeCalls) == 0 {
		t.Fatal("expected SetCreationTime to be called")
	}
	if !timer.setCreationTimeCalls[0].t.Equal(want) {
		t.Errorf("SetCreationTime got %v, want %v", timer.setCreationTimeCalls[0].t, want)
	}
}

func TestApplyMKV_Failure_NoTimestamps(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 1, applyResult: "Error"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	p.applyMKV("show.mkv", "T", "", "", "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", "", RatingNone)

	if len(timer.setTimesCalls) != 0 || len(timer.setCreationTimeCalls) != 0 {
		t.Error("timestamps should not be set on failure")
	}
}

// --- applyRatingOnlyMKV ---

func TestApplyRatingOnlyMKV_RestoresTimestamps(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(filePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set a specific mtime so we can verify it gets restored.
	want := time.Date(2021, 3, 15, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(filePath, want, want); err != nil {
		t.Fatal(err)
	}

	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	ok := p.applyRatingOnlyMKV(filePath, RatingR16)
	if !ok {
		t.Fatal("expected success")
	}

	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called to restore timestamps")
	}
	got := timer.setTimesCalls[0].t
	if !got.Equal(want) {
		t.Errorf("restored mtime = %v; want %v", got, want)
	}
}

func TestApplyRatingOnlyMKV_RatingNone_NoMkvpropedit(t *testing.T) {
	runner := &fakeMKVRunner{avail: true}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := p.applyRatingOnlyMKV("file.mkv", RatingNone)
	if !ok {
		t.Error("expected true (nothing to do)")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called when rating is RatingNone")
	}
}

func TestApplyRatingOnlyMKV_Failure_NoTimestampRestore(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 1, applyResult: "Error"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	ok := p.applyRatingOnlyMKV("file.mkv", RatingPG)
	if ok {
		t.Error("expected false on mkvpropedit failure")
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("SetTimes should not be called on failure")
	}
}

// --- resolveEpisodeRating ---

func TestResolveEpisodeRating_InvalidInput_RePrompts(t *testing.T) {
	rc := emptyRatingCache(t)
	// Prompter returns an invalid rating first, then a valid one.
	prompter := &scriptedPrompter{answers: []string{"PG-13", "R16"}}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingR16 {
		t.Errorf("got %q, want R16", got)
	}
	if prompter.idx != 2 {
		t.Errorf("prompter called %d times, want 2", prompter.idx)
	}
	// Rating must be cached in the show cache.
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != RatingR16 {
		t.Errorf("show cache has %q ok=%v, want R16", cached, ok)
	}
}

func TestResolveEpisodeRating_BlankAcceptsPlexDefault(t *testing.T) {
	rc := emptyRatingCache(t)
	// Prompter immediately hits Enter (blank).
	prompter := &scriptedPrompter{answers: []string{""}}
	plex := &fakePlexClient{showRatingKey: "42", contentRating: RatingM}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		plex:                  plex,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingM {
		t.Errorf("got %q, want M (Plex default)", got)
	}
	// Rating must be cached for future episodes.
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != RatingM {
		t.Errorf("show cache has %q ok=%v, want M", cached, ok)
	}
}

func TestResolveEpisodeRating_BlankWithNoPlexDefault_Skips(t *testing.T) {
	rc := emptyRatingCache(t)
	prompter := &scriptedPrompter{answers: []string{""}}
	// Plex has no rating for this show.
	plex := &fakePlexClient{showRatingKey: "42", contentRating: RatingNone}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		plex:                  plex,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingNone {
		t.Errorf("got %q, want RatingNone when Plex has no rating and user skips", got)
	}
}

func TestResolveEpisodeRating_BlankUsesPlexTitleFallback(t *testing.T) {
	// TMDB-ID lookup fails (e.g. HAMA agent); title lookup succeeds.
	rc := emptyRatingCache(t)
	prompter := &scriptedPrompter{answers: []string{""}}
	plex := &fakePlexClient{
		findErr:        ErrPlexNotFound, // TMDB ID not in Plex GUID
		titleRatingKey: "88",
		contentRating:  Rating16,
	}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		plex:                  plex,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "AIKa", "", 1, 1)
	if got != Rating16 {
		t.Errorf("got %q, want 16 (Plex title-fallback default)", got)
	}
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != Rating16 {
		t.Errorf("show cache has %q ok=%v, want 16", cached, ok)
	}
}

func TestResolveEpisodeRating_BlankUsesPlexFilePathFallback(t *testing.T) {
	// Both TMDB-ID and title lookups fail; file-path lookup succeeds.
	rc := emptyRatingCache(t)
	prompter := &scriptedPrompter{answers: []string{""}}
	plex := &fakePlexClient{
		findErr:           ErrPlexNotFound, // TMDB ID not in Plex GUID
		findTitleErr:      ErrPlexNotFound, // title mismatch (e.g. "AIKa" vs "Agent Aika")
		filePathRatingKey: "23067",
		contentRating:     Rating16,
	}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		plex:                  plex,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "AIKa", `G:\TV\AIKa (1997)\Season 01\ep1.mkv`, 1, 1)
	if got != Rating16 {
		t.Errorf("got %q, want 16 (Plex file-path fallback default)", got)
	}
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != Rating16 {
		t.Errorf("show cache has %q ok=%v, want 16", cached, ok)
	}
}

func TestApplyRatingOnlyMKV_AlreadyApplied_SkipsWrite(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(filePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := emptyRatingCache(t)
	// Pre-mark this file as already having R16 tags applied.
	rc.MarkApplied(filePath, RatingR16)

	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer, ratingCache: rc}

	ok := p.applyRatingOnlyMKV(filePath, RatingR16)
	if !ok {
		t.Error("expected true (already applied counts as success)")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply must not be called when tags are already applied")
	}
}

// --- Run (end-to-end) ---

func TestProcessorRun_SkipsOldFiles(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Test Show")
	os.MkdirAll(showDir, 0755)

	mkvPath := filepath.Join(showDir, "Test Show - S01E01.mkv")
	os.WriteFile(mkvPath, []byte{}, 0644)
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(mkvPath, oldTime, oldTime)

	cache := cacheWith(t, map[string]string{"Test Show": "12345"})
	runner := &fakeMKVRunner{avail: true}
	p, _ := newTestProcessor(t, dir, Config{
		MediaDir:   dir,
		CutoffDate: time.Now(),
		ProcessAll: true,
	}, cache, &fakeTMDB{}, runner, &scriptedPrompter{})

	stats := p.Run()

	if stats.SkippedOld != 1 {
		t.Errorf("got SkippedOld=%d, want 1", stats.SkippedOld)
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called for old files")
	}
}

func TestProcessorRun_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Test Show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "subtitle.srt"), []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Test Show": "12345"})
	runner := &fakeMKVRunner{avail: true}
	p, _ := newTestProcessor(t, dir, Config{
		MediaDir:   dir,
		CutoffDate: time.Time{},
		ProcessAll: true,
	}, cache, &fakeTMDB{}, runner, &scriptedPrompter{})

	p.Run()
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called for non-MKV files")
	}
}

func TestProcessorRun_SkipsShowOnSKIP(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Skip Show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "Skip Show - S01E01.mkv"), []byte{}, 0644)

	cache := emptyCache(t)
	runner := &fakeMKVRunner{avail: true}
	// User skips the new show prompt
	p, _ := newTestProcessor(t, dir, Config{
		MediaDir:   dir,
		CutoffDate: time.Time{},
	}, cache, &fakeTMDB{searchResults: []ShowResult{{ID: 1}}}, runner,
		&scriptedPrompter{answers: []string{"s"}})

	p.Run()
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called for skipped shows")
	}
}

func TestProcessorRun_SuccessPath(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Good Show")
	os.MkdirAll(showDir, 0755)

	mkvPath := filepath.Join(showDir, "Good Show - S01E01.mkv")
	os.WriteFile(mkvPath, []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Good Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{
			PosterPath: "/season.jpg",
			Episodes: []Episode{
				{EpisodeNumber: 1, AirDate: "2022-03-01", Overview: "First episode"},
			},
		},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: mkvpropeditSuccessMsg,
		tagsPath:    "t.xml",
	}
	p, _ := newTestProcessor(t, dir, Config{
		MediaDir:   dir,
		CutoffDate: time.Time{},
		ProcessAll: true,
	}, cache, tmdb, runner, &scriptedPrompter{})

	stats := p.Run()

	if stats.Success != 1 {
		t.Errorf("got Success=%d, want 1", stats.Success)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 Apply call, got %d", len(runner.calls))
	}
	// Verify title was passed correctly.
	args := runner.calls[0].args
	titleFound := false
	for _, a := range args {
		if strings.HasPrefix(a, "title=") {
			titleFound = true
			break
		}
	}
	if !titleFound {
		t.Errorf("title= arg not found in: %v", args)
	}
}

func TestProcessorRun_NoEpisodeDate_FallsBackToPreviousEpisode(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Fallback Show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "Fallback Show - S01E02.mkv"), []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Fallback Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult: SeasonResult{
			AirDate: "2022-02-01",
			Episodes: []Episode{
				{EpisodeNumber: 1, AirDate: "2022-03-01"},
				{EpisodeNumber: 2, AirDate: ""},
			},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil)

	stats := p.Run()
	if stats.Success != 1 {
		t.Fatalf("expected success, got %+v", stats)
	}
	if len(timer.setTimesCalls) != 0 {
		t.Errorf("expected no filesystem timestamp for fallback date, got %d call(s)", len(timer.setTimesCalls))
	}
}

func TestProcessorRun_NoEpisodeDate_NoPrevious_FallsBackToSeasonDate(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Season Date Show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "Season Date Show - S01E01.mkv"), []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Season Date Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult: SeasonResult{
			AirDate: "2022-02-15",
			Episodes: []Episode{
				{EpisodeNumber: 1, AirDate: ""},
			},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil)

	stats := p.Run()
	if stats.Success != 1 {
		t.Fatalf("expected success, got %+v", stats)
	}
	if len(timer.setTimesCalls) != 0 {
		t.Errorf("expected no filesystem timestamp for fallback date, got %d call(s)", len(timer.setTimesCalls))
	}
}

func TestProcessorRun_NoEpisodeDate_NoSeasonDate_FallsBackToShowDate(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Show Date Show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "Show Date Show - S01E01.mkv"), []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Show Date Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{FirstAirDate: "2021-06-10"},
		seasonResult: SeasonResult{
			AirDate: "",
			Episodes: []Episode{
				{EpisodeNumber: 1, AirDate: ""},
			},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil)

	stats := p.Run()
	if stats.Success != 1 {
		t.Fatalf("expected success, got %+v", stats)
	}
	if len(timer.setTimesCalls) != 0 {
		t.Errorf("expected no filesystem timestamp for fallback date, got %d call(s)", len(timer.setTimesCalls))
	}
}

// --- RetryList integration ---

func TestProcessorRun_FallbackDate_AddsToRetryList(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Retry Show")
	os.MkdirAll(showDir, 0755)
	mkvPath := filepath.Join(showDir, "Retry Show - S01E01.mkv")
	os.WriteFile(mkvPath, []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Retry Show": "999"})
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, AirDate: ""}}},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil)

	p.Run()

	if !retryList.Has(mkvPath) {
		t.Errorf("expected %q to be in retry list after fallback date", mkvPath)
	}
}

func TestProcessorRun_RealDate_RemovesFromRetryList(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Real Date Show")
	os.MkdirAll(showDir, 0755)
	mkvPath := filepath.Join(showDir, "Real Date Show - S01E01.mkv")
	os.WriteFile(mkvPath, []byte{}, 0644)

	cache := cacheWith(t, map[string]string{"Real Date Show": "999"})
	tmdb := &fakeTMDB{
		seasonResult: SeasonResult{
			Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2023-05-01"}},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	retryList.Add(mkvPath) // pre-populate as if a previous run used a fallback
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil)

	p.Run()

	if retryList.Has(mkvPath) {
		t.Errorf("expected %q to be removed from retry list after real date found", mkvPath)
	}
}

func TestProcessorRun_RetryList_BypassesCutoff(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Old Show")
	os.MkdirAll(showDir, 0755)
	mkvPath := filepath.Join(showDir, "Old Show - S01E01.mkv")
	os.WriteFile(mkvPath, []byte{}, 0644)

	// Set the file's mod time to well before the cutoff.
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(mkvPath, oldTime, oldTime)

	cache := cacheWith(t, map[string]string{"Old Show": "999"})
	tmdb := &fakeTMDB{
		seasonResult: SeasonResult{
			Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2020-01-01"}},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: mkvpropeditSuccessMsg, tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	retryList.Add(mkvPath) // file is in retry list despite being old
	p := NewProcessor(Config{MediaDir: dir, CutoffDate: time.Now(), ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Errorf("expected file in retry list to be processed despite old mod time, got %+v", stats)
	}
}

// containsAll returns true if all of the given values appear in haystack
// (as individual elements, in any order).
func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
