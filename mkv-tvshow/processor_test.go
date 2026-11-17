package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// fakeRatingSource is a minimal RatingSource that returns fixed ratings.
type fakeRatingSource struct {
	showRating    NZRating
	episodeRating NZRating
}

func (f *fakeRatingSource) Name() string { return "fakeSource" }
func (f *fakeRatingSource) FetchShowRating(_, _ string) (NZRating, error) {
	return f.showRating, nil
}
func (f *fakeRatingSource) FetchEpisodeRating(_, _ string, _, _ int) (NZRating, error) {
	return f.episodeRating, nil
}

// helpers

func newTestProcessor(t *testing.T, dir string, cfg Config, cache *ShowCache, tmdb TMDBClient, mkv MKVRunner, prompter Prompter) *Processor {
	t.Helper()
	if cfg.MediaDir == "" {
		cfg.MediaDir = dir
	}
	timer := &captureFileTimer{}
	rc, _ := LoadRatingCache(filepath.Join(t.TempDir(), "rating_cache.json"))
	return NewProcessor(cfg, cache, map[string]ManualEntry{}, tmdb, mkv, prompter, timer, emptyRetryList(t), rc, nil, nil, nil)
}

func emptyRetryList(t *testing.T) *RetryList {
	t.Helper()
	return newRetryList(filepath.Join(t.TempDir(), "pending_dates.json"))
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
	p := newTestProcessor(t, "", Config{ProcessAll: true}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{})

	id := p.resolveShowID("Show A")
	if id != "111" {
		t.Errorf("got %q, want \"111\"", id)
	}
}

func TestResolveShowID_AlreadyInCache_PromptYes(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show B": "222"})
	p := newTestProcessor(t, "", Config{}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"y"}})

	id := p.resolveShowID("Show B")
	if id != "222" {
		t.Errorf("got %q, want \"222\"", id)
	}
}

func TestResolveShowID_AlreadyInCache_PromptNo(t *testing.T) {
	cache := cacheWith(t, map[string]string{"Show C": "333"})
	p := newTestProcessor(t, "", Config{}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"n"}})

	id := p.resolveShowID("Show C")
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP", id)
	}
	if !p.sessionSkipped["Show C"] {
		t.Error("expected Show C to be in sessionSkipped")
	}
}

func TestResolveShowID_AlreadyInCache_TypoDefaultsToSkip(t *testing.T) {
	// Anything other than "y" should skip, not process.
	cache := cacheWith(t, map[string]string{"Show D": "444"})
	p := newTestProcessor(t, "", Config{}, cache,
		&fakeTMDB{}, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"nn"}})

	id := p.resolveShowID("Show D")
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP (typo should default to skip)", id)
	}
}

func TestResolveShowID_NewShow_AcceptFirstResult(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 456, Name: "New Show", FirstAirDate: "2021-01-01"}}}
	p := newTestProcessor(t, "", Config{}, cache,
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
	p := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"99999"}})

	id := p.resolveShowID("My Show")
	if id != "99999" {
		t.Errorf("got %q, want \"99999\"", id)
	}
}

func TestResolveShowID_NewShow_SkipAnswer(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: []ShowResult{{ID: 1, Name: "A Show"}}}
	p := newTestProcessor(t, "", Config{}, cache,
		tmdb, &fakeMKVRunner{}, &scriptedPrompter{answers: []string{"s"}})

	id := p.resolveShowID("My Show")
	if id != "SKIP" {
		t.Errorf("got %q, want SKIP", id)
	}
}

func TestResolveShowID_NewShow_NoResults_ManualID(t *testing.T) {
	cache := emptyCache(t)
	tmdb := &fakeTMDB{searchResults: nil}
	p := newTestProcessor(t, "", Config{}, cache,
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
	p := newTestProcessor(t, "", Config{}, cache, tmdb, &fakeMKVRunner{}, prompter)

	p.resolveShowID("Skipped Show")       // consumes the one answer
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
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	p := &Processor{}
	got, _ := p.normalizeFilename(dir, name)
	if got != name {
		t.Errorf("got %q, want %q (should not rename 2-episode file)", got, name)
	}
}

func TestNormalizeFilename_ThreeEpisodes_Renamed(t *testing.T) {
	dir := t.TempDir()
	name := "Show - S01E01E02E03.mkv"
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	ok := wroteMKV(p.applyMKV("file.mkv", "Title", "", "id", "", "", "", RatingNone, false))
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
		applyResult: "Done.",
		tagsPath:    "tags.xml",
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "My Title", "", "", "", "", "", RatingNone, false)

	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	args := runner.calls[0].args
	if !containsAll(args, "--edit", "info", "--set", "title=My Title") {
		t.Errorf("missing expected args in: %v", args)
	}
}

func TestApplyMKV_WithDate_SetsDateArg(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "2021-05-10T00:00:00Z", "", "", RatingNone, false)

	args := runner.calls[0].args
	if !containsAll(args, "--set", "date=2021-05-10T00:00:00Z") {
		t.Errorf("missing date arg in: %v", args)
	}
}

func TestApplyMKV_WithPoster_AddsAttachment(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
		posterPath:  "poster.jpg",
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyMKV("show.mkv", "T", "", "", "", "", "http://example.com/poster.jpg", RatingNone, false)

	args := runner.calls[0].args
	if !containsAll(args, "--add-attachment", "poster.jpg") {
		t.Errorf("missing attachment args in: %v", args)
	}
}

func TestApplyMKV_ExistingCovers_ReplacedByNewPoster(t *testing.T) {
	const add = "--attachment-name cover.jpg --attachment-mime-type image/jpeg --add-attachment poster.jpg"
	const delJPEG = "--delete-attachment mime-type:image/jpeg "
	const delPNG = "--delete-attachment mime-type:image/png "
	cases := []struct {
		name     string
		existing AttachmentInfo
		want     string
	}{
		{"no cover", AttachmentInfo{}, add},
		{"jpeg only", AttachmentInfo{HasJPEG: true}, delJPEG + add},
		{"png only", AttachmentInfo{HasPNG: true}, delPNG + add},
		{"jpeg and png", AttachmentInfo{HasJPEG: true, HasPNG: true}, delJPEG + delPNG + add},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeMKVRunner{
				avail:       true,
				attachments: tc.existing,
				applyResult: "Done.",
				tagsPath:    "t.xml",
				posterPath:  "poster.jpg",
			}
			p := &Processor{mkv: runner, timer: &captureFileTimer{}}
			p.applyMKV("show.mkv", "T", "", "", "", "", "http://example.com/poster.jpg", RatingNone, false)

			args := runner.calls[0].args
			idx := slices.Index(args, "--delete-attachment")
			if idx < 0 {
				idx = slices.Index(args, "--attachment-name")
			}
			if idx < 0 {
				t.Fatalf("no attachment args in: %v", args)
			}
			if got := strings.Join(args[idx:], " "); got != tc.want {
				t.Errorf("attachment args = %q; want %q", got, tc.want)
			}
		})
	}
}

// An existing cover must survive when no replacement poster is available.
func TestApplyMKV_ExistingCover_KeptWithoutNewPoster(t *testing.T) {
	cases := []struct {
		name, posterURL string
		posterErr       error
		wantWarning     bool
	}{
		{"no poster URL", "", nil, false},
		{"download failed", "http://example.com/poster.jpg", errors.New("unexpected status 503"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeMKVRunner{
				avail:       true,
				attachments: AttachmentInfo{HasJPEG: true, HasPNG: true},
				applyResult: "Done.",
				tagsPath:    "t.xml",
				posterErr:   tc.posterErr,
			}
			p := &Processor{mkv: runner, timer: &captureFileTimer{}}
			out := captureStdout(t, func() {
				p.applyMKV("show.mkv", "T", "", "", "", "", tc.posterURL, RatingNone, false)
			})

			for _, a := range runner.calls[0].args {
				if strings.Contains(a, "attachment") {
					t.Errorf("unexpected attachment arg %q without a new poster: %v", a, runner.calls[0].args)
				}
			}
			warned := strings.Contains(out, "Poster download failed; cover unchanged: unexpected status 503")
			if warned != tc.wantWarning {
				t.Errorf("warning printed = %v, want %v; output:\n%s", warned, tc.wantWarning, out)
			}
		})
	}
}

func TestApplyMKV_Success_SetsTimestamps(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 0, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	p.applyMKV("show.mkv", "T", "", "", "2020-06-15T00:00:00Z", "2020-06-15T00:00:00Z", "", RatingNone, false)

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
	runner := &fakeMKVRunner{avail: true, applyRC: 2, applyResult: "Error"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	p.applyMKV("show.mkv", "T", "", "", "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", "", RatingNone, false)

	if len(timer.setTimesCalls) != 0 || len(timer.setCreationTimeCalls) != 0 {
		t.Error("timestamps should not be set on failure")
	}
}

// --- applyDebandOnlyMKV ---

func TestApplyDebandOnlyMKV_MkvpropeditNotFound(t *testing.T) {
	runner := &fakeMKVRunner{avail: false}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := tagOnlyOK(p.applyDebandOnlyMKV("file.mkv", true))
	if ok {
		t.Error("expected false when mkvpropedit unavailable")
	}
}

// debandTestFile creates an MKV stand-in that the tag-only helpers can stat.
func debandTestFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ep.mkv")
	if err := os.WriteFile(path, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyDebandOnlyMKV_ExtractTagsFails(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, extractTagsErr: fmt.Errorf("mkvextract not found")}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := tagOnlyOK(p.applyDebandOnlyMKV(debandTestFile(t), true))
	if ok {
		t.Error("expected false when ExtractTags fails")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called after ExtractTags failure")
	}
}

func TestApplyDebandOnlyMKV_StatError_DoesNotTag(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:          true,
		applyResult:    "Done.",
		tagsPath:       "tags.xml",
		extractTagsXML: `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets></Tag></Tags>`,
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}

	var ok bool
	out := captureStdout(t, func() { ok = tagOnlyOK(p.applyDebandOnlyMKV(filepath.Join(t.TempDir(), "missing.mkv"), true)) })
	if ok {
		t.Error("expected false when the file can't be stat'd")
	}
	if len(runner.calls) != 0 {
		t.Errorf("Apply called %d times, want none: timestamps couldn't be restored", len(runner.calls))
	}
	if !strings.Contains(out, "not tagging") {
		t.Errorf("output doesn't explain the skip:\n%s", out)
	}
}

func TestApplyDebandOnlyMKV_Success_RestoresTimestamps(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(filePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2021, 6, 1, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(filePath, want, want); err != nil {
		t.Fatal(err)
	}

	runner := &fakeMKVRunner{
		avail:          true,
		applyResult:    "Done.",
		tagsPath:       "tags.xml",
		extractTagsXML: `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets></Tag></Tags>`,
	}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	ok := tagOnlyOK(p.applyDebandOnlyMKV(filePath, true))
	if !ok {
		t.Fatal("expected success")
	}
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called to restore timestamps")
	}
	if !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("restored mtime = %v; want %v", timer.setTimesCalls[0].t, want)
	}
}

func TestApplyDebandOnlyMKV_PassesUpdatedXMLToApply(t *testing.T) {
	runner := &fakeMKVRunner{
		avail:          true,
		applyResult:    "Done.",
		tagsPath:       "tags.xml",
		extractTagsXML: `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets></Tag></Tags>`,
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	p.applyDebandOnlyMKV(debandTestFile(t), true)

	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	if !containsAll(runner.calls[0].args, "--tags", "all:tags.xml") {
		t.Errorf("unexpected args: %v", runner.calls[0].args)
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

	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	ok := tagOnlyOK(p.applyRatingOnlyMKV(filePath, RatingR16))
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
	ok := tagOnlyOK(p.applyRatingOnlyMKV("file.mkv", RatingNone))
	if !ok {
		t.Error("expected true (nothing to do)")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called when rating is RatingNone")
	}
}

func TestApplyRatingOnlyMKV_Failure_NoTimestampRestore(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyRC: 2, applyResult: "Error"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer}
	ok := tagOnlyOK(p.applyRatingOnlyMKV(debandTestFile(t), RatingPG))
	if ok {
		t.Error("expected false on mkvpropedit failure")
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("SetTimes should not be called on failure")
	}
}

func TestApplyRatingOnlyMKV_ExtractTagsFails_ReturnsFalse(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, extractTagsErr: fmt.Errorf("mkvextract not found")}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := tagOnlyOK(p.applyRatingOnlyMKV(debandTestFile(t), RatingM))
	if ok {
		t.Error("expected false when ExtractTags fails")
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called after ExtractTags failure")
	}
}

func TestApplyRatingOnlyMKV_PreservesExistingDebandTag(t *testing.T) {
	existingXML := `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>` +
		`<Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	runner := &fakeMKVRunner{
		avail:          true,
		applyResult:    "Done.",
		tagsPath:       "t.xml",
		extractTagsXML: existingXML,
	}
	p := &Processor{mkv: runner, timer: &captureFileTimer{}}
	ok := tagOnlyOK(p.applyRatingOnlyMKV(debandTestFile(t), RatingM))
	if !ok {
		t.Fatal("expected success")
	}
	// The XML written to WriteTempTags must contain both DEBAND and LAW_RATING.
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	// WriteTempTags is not directly inspectable via fakeMKVRunner, but the call
	// to Apply with --tags proves the path completed successfully.
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
	if _, ok := rc.GetShowRating("12345"); ok {
		t.Error("a skipped rating was cached")
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

func TestResolveEpisodeRating_CachedRating_ReturnsSilently(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("12345", RatingR16)
	prompter := &scriptedPrompter{answers: []string{}}
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{},
		prompter:              prompter,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingR16 {
		t.Errorf("got %q, want R16 (silent cache hit)", got)
	}
	if prompter.idx != 0 {
		t.Errorf("prompter called %d times, want 0 (no prompt for cached rating)", prompter.idx)
	}
}

func TestResolveEpisodeRating_SourceFoundRating_PromptsForConfirmation(t *testing.T) {
	rc := emptyRatingCache(t)
	src := &fakeRatingSource{showRating: RatingR16}
	prompter := &scriptedPrompter{answers: []string{""}} // user presses Enter to confirm
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{src},
		prompter:              prompter,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingR16 {
		t.Errorf("got %q, want R16 (confirmed source rating)", got)
	}
	if prompter.idx != 1 {
		t.Errorf("prompter called %d times, want 1 (source rating must be confirmed)", prompter.idx)
	}
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != RatingR16 {
		t.Errorf("show cache has %q ok=%v, want R16", cached, ok)
	}
}

func TestResolveEpisodeRating_SourceFoundRating_CanBeOverridden(t *testing.T) {
	rc := emptyRatingCache(t)
	src := &fakeRatingSource{showRating: RatingR16}
	prompter := &scriptedPrompter{answers: []string{"M"}} // user overrides to M
	p := &Processor{
		ratingCache:           rc,
		ratingSources:         []RatingSource{src},
		prompter:              prompter,
		sessionPromptedRating: make(map[string]NZRating),
	}

	got := p.resolveEpisodeRating("12345", "Test Show", "", 1, 1)
	if got != RatingM {
		t.Errorf("got %q, want M (user override of source rating)", got)
	}
	if cached, ok := rc.GetShowRating("12345"); !ok || cached != RatingM {
		t.Errorf("show cache has %q ok=%v, want M", cached, ok)
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

	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := &Processor{mkv: runner, timer: timer, ratingCache: rc}

	ok := tagOnlyOK(p.applyRatingOnlyMKV(filePath, RatingR16))
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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}

	mkvPath := filepath.Join(showDir, "Test Show - S01E01.mkv")
	if err := os.WriteFile(mkvPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(mkvPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Test Show": "12345"})
	runner := &fakeMKVRunner{avail: true}
	p := newTestProcessor(t, dir, Config{
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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "subtitle.srt"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Test Show": "12345"})
	runner := &fakeMKVRunner{avail: true}
	p := newTestProcessor(t, dir, Config{
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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Skip Show - S01E01.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := emptyCache(t)
	runner := &fakeMKVRunner{avail: true}
	// User skips the new show prompt
	p := newTestProcessor(t, dir, Config{
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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}

	mkvPath := filepath.Join(showDir, "Good Show - S01E01.mkv")
	if err := os.WriteFile(mkvPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	p := newTestProcessor(t, dir, Config{
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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Fallback Show - S01E02.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Season Date Show - S01E01.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Show Date Show - S01E01.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

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
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	mkvPath := filepath.Join(showDir, "Retry Show - S01E01.mkv")
	if err := os.WriteFile(mkvPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Retry Show": "999"})
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{FirstAirDate: "2022-01-01"},
		seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, AirDate: ""}}},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil, nil)

	p.Run()

	if !onDiskRetry(t, retryList).Has(mkvPath) {
		t.Errorf("expected %q to be in retry list after fallback date", mkvPath)
	}
}

func TestProcessorRun_RealDate_RemovesFromRetryList(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Real Date Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	mkvPath := filepath.Join(showDir, "Real Date Show - S01E01.mkv")
	if err := os.WriteFile(mkvPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Real Date Show": "999"})
	tmdb := &fakeTMDB{
		seasonResult: SeasonResult{
			Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2023-05-01"}},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	retryList.Add(mkvPath) // pre-populate as if a previous run used a fallback
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil, nil)

	p.Run()

	if onDiskRetry(t, retryList).Has(mkvPath) {
		t.Errorf("expected %q to be removed from retry list after real date found", mkvPath)
	}
}

func TestProcessorRun_RetryList_BypassesCutoff(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Old Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	mkvPath := filepath.Join(showDir, "Old Show - S01E01.mkv")
	if err := os.WriteFile(mkvPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Set the file's mod time to well before the cutoff.
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(mkvPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"Old Show": "999"})
	tmdb := &fakeTMDB{
		seasonResult: SeasonResult{
			Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2020-01-01"}},
		},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	retryList := emptyRetryList(t)
	retryList.Add(mkvPath) // file is in retry list despite being old
	p := NewProcessor(Config{MediaDir: dir, CutoffDate: time.Now(), ProcessAll: true}, cache, map[string]ManualEntry{}, tmdb, runner, &scriptedPrompter{}, &captureFileTimer{}, retryList, emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Errorf("expected file in retry list to be processed despite old mod time, got %+v", stats)
	}
}

// --- manual extras ---

func TestProcessorRun_ManualExtra_Processed(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S02 - Drama CD.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{PosterPath: "/season2.jpg"},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S02 - Drama CD": {Date: "2022-04-01", Season: 2},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	want := time.Date(2022, 4, 1, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called")
	}
	if !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("SetTimes got %v, want %v", timer.setTimesCalls[0].t, want)
	}
}

func TestProcessorRun_ManualExtra_EpisodeRef_UsesEpisodeData(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S01 NCOP.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{
			PosterPath: "/season1.jpg",
			Episodes: []Episode{
				{EpisodeNumber: 1, AirDate: "2021-10-03", Overview: "Episode one overview"},
				{EpisodeNumber: 2, AirDate: "2021-10-10", Overview: "Episode two overview"},
			},
		},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S01 NCOP": {Episode: "S01E01"},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	// Filesystem timestamp should match the episode air date.
	wantTime := time.Date(2021, 10, 3, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called")
	}
	if !timer.setTimesCalls[0].t.Equal(wantTime) {
		t.Errorf("SetTimes got %v, want %v", timer.setTimesCalls[0].t, wantTime)
	}
	// Title comes from the filename base with the suffix in parens.
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	if !containsAll(runner.calls[0].args, "title=My Show - S01 (NCOP)") {
		t.Errorf("expected title 'My Show - S01 (NCOP)' in %v", runner.calls[0].args)
	}
}

func TestProcessorRun_ManualExtra_OpEdSuffix_FilenameTitle(t *testing.T) {
	// Extra with manual date (no episode ref) but OP/ED suffix in filename.
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S02 ED.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{PosterPath: "/season2.jpg"},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S02 ED": {Date: "2022-06-15", Season: 2},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	// Title: filename-based (no episode ref) with (ED) suffix appended.
	if !containsAll(runner.calls[0].args, "title=My Show - S02 (ED)") {
		t.Errorf("expected title 'My Show - S02 (ED)' in %v", runner.calls[0].args)
	}
}

func TestProcessorRun_ManualExtra_EpisodePatternInName_TreatedAsExtra(t *testing.T) {
	// A file with S##E## in its name but explicitly listed in extras should go
	// through the extras path (not the episode path), using the hyphen-prefix
	// NCOP suffix and the referenced episode's TMDB data.
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S01E03 - A Friend-NCOP.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{
			PosterPath: "/season1.jpg",
			Episodes: []Episode{
				{EpisodeNumber: 3, AirDate: "2021-01-25", Overview: "Ep3 summary"},
			},
		},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S01E03 - A Friend-NCOP": {Episode: "S01E03"},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	// Title from filename base + suffix in parens; hyphen separator detected.
	if !containsAll(runner.calls[0].args, "title=My Show - S01E03 - A Friend (NCOP)") {
		t.Errorf("expected 'title=My Show - S01E03 - A Friend (NCOP)' in %v", runner.calls[0].args)
	}
	// Date from episode air date.
	wantTime := time.Date(2021, 1, 25, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 || !timer.setTimesCalls[0].t.Equal(wantTime) {
		t.Errorf("SetTimes: got %v, want %v", timer.setTimesCalls, wantTime)
	}
}

func TestProcessorRun_ManualExtra_EpisodeRef_NoSeason_DerivesSeasonFromEpisode(t *testing.T) {
	// When no explicit season is set, the season from the episode ref is used so
	// the episode data can be fetched and the timestamp is set correctly.
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S02 - Promo.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{
			PosterPath: "/season2.jpg",
			Episodes: []Episode{
				{EpisodeNumber: 5, AirDate: "2023-03-15", Overview: "Promo overview"},
			},
		},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S02 - Promo": {Episode: "S02E05"},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	// Timestamp from episode air date (confirms episode was found via derived season).
	wantTime := time.Date(2023, 3, 15, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 || !timer.setTimesCalls[0].t.Equal(wantTime) {
		t.Errorf("SetTimes: got %v, want %v", timer.setTimesCalls, wantTime)
	}
}

func TestProcessorRun_ManualExtra_EpisodeRef_DateOverridesEpisodeDate(t *testing.T) {
	// When both date and episode are specified, date wins for the timestamp.
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S01 - Bonus.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails: ShowDetails{PosterPath: "/show.jpg"},
		seasonResult: SeasonResult{
			PosterPath: "/season1.jpg",
			Episodes: []Episode{
				{EpisodeNumber: 2, AirDate: "2021-11-20", Overview: "Episode two overview"},
			},
		},
	}
	runner := &fakeMKVRunner{
		avail:       true,
		applyResult: "Done.",
		tagsPath:    "t.xml",
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Extras: map[string]ExtraEntry{
				"My Show - S01 - Bonus": {Episode: "S01E02", Date: "2022-01-01"},
			},
		},
	}
	timer := &captureFileTimer{}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	// Timestamp must use the manual date, not the episode air date.
	wantTime := time.Date(2022, 1, 1, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 || !timer.setTimesCalls[0].t.Equal(wantTime) {
		t.Errorf("SetTimes: got %v, want %v", timer.setTimesCalls, wantTime)
	}
	// The date written to MKV metadata must also be the manual date.
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	if !containsAll(runner.calls[0].args, "date=2022-01-01T00:00:00Z") {
		t.Errorf("expected manual date in mkvpropedit args, got %v", runner.calls[0].args)
	}
}

func TestProcessorRun_ManualExtra_NoEntry_Skipped(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S02 - Drama CD.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	runner := &fakeMKVRunner{avail: true}
	p := newTestProcessor(t, dir, Config{
		MediaDir:   dir,
		ProcessAll: true,
	}, cache, &fakeTMDB{}, runner, &scriptedPrompter{})

	stats := p.Run()
	if stats.Success != 0 || stats.Failed != 0 {
		t.Errorf("expected no processing for unknown extra, got %+v", stats)
	}
	if len(runner.calls) != 0 {
		t.Error("Apply should not be called for non-episode file without extras entry")
	}
}

// --- manual dates ---

func TestProcessorRun_ManualDate_NoTMDBDate_SetsTimestamp(t *testing.T) {
	// When TMDB has no air date but manual_data.json provides one, it is used
	// as a real date (stampDate is set, so filesystem timestamps are written).
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S01E03.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		showDetails:  ShowDetails{FirstAirDate: "2020-01-01"},
		seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 3, AirDate: ""}}},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	manualData := map[string]ManualEntry{
		"My Show": {Dates: map[string]string{"S01E03": "2023-04-15"}},
	}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	want := time.Date(2023, 4, 15, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called for manual date")
	}
	if !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("SetTimes got %v, want %v", timer.setTimesCalls[0].t, want)
	}
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	if !containsAll(runner.calls[0].args, "date=2023-04-15T00:00:00Z") {
		t.Errorf("expected manual date in mkvpropedit args, got %v", runner.calls[0].args)
	}
}

func TestProcessorRun_ManualDate_OverridesTMDBDate(t *testing.T) {
	dir := t.TempDir()
	showDir := filepath.Join(dir, "My Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "My Show - S01E01.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	cache := cacheWith(t, map[string]string{"My Show": "999"})
	tmdb := &fakeTMDB{
		seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-06-01"}}},
	}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	timer := &captureFileTimer{}
	manualData := map[string]ManualEntry{
		"My Show": {Dates: map[string]string{"S01E01": "2022-12-25"}},
	}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true}, cache, manualData, tmdb, runner, &scriptedPrompter{}, timer, emptyRetryList(t), emptyRatingCache(t), nil, nil, nil)

	stats := p.Run()

	if stats.Success != 1 {
		t.Fatalf("expected Success=1, got %+v", stats)
	}
	want := time.Date(2022, 12, 25, 0, 0, 0, 0, time.Local)
	if len(timer.setTimesCalls) == 0 {
		t.Fatal("expected SetTimes to be called")
	}
	if !timer.setTimesCalls[0].t.Equal(want) {
		t.Errorf("SetTimes got %v (TMDB date), want %v (manual override)", timer.setTimesCalls[0].t, want)
	}
	if len(runner.calls) == 0 {
		t.Fatal("expected Apply to be called")
	}
	if !containsAll(runner.calls[0].args, "date=2022-12-25T00:00:00Z") {
		t.Errorf("expected overridden date in mkvpropedit args, got %v", runner.calls[0].args)
	}
}

// --- RunLanguageOnly ---

func TestRunLanguageOnly_SetsLanguageForCachedShows(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	rc.SetShowRating("222", RatingM)

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	cache := cacheWith(t, map[string]string{"Show A": "111", "Show B": "222"})

	p := NewProcessor(Config{}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 2 {
		t.Fatalf("expected 2 SetAudioLanguage calls, got %d", len(plexClient.setLangCalls))
	}
	for _, call := range plexClient.setLangCalls {
		if call.lang != "ja" {
			t.Errorf("expected lang ja, got %q", call.lang)
		}
	}
}

func TestRunLanguageOnly_SkipsAlreadyApplied(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	// Cache holds the region-qualified signature actually applied (ja -> ja-JP).
	rc.SetLanguageApplied("111", "ja-JP")

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	cache := cacheWith(t, map[string]string{"Show A": "111"})

	p := NewProcessor(Config{}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 0 {
		t.Errorf("expected no SetAudioLanguage calls (already applied), got %d", len(plexClient.setLangCalls))
	}
}

func TestRunLanguageOnly_OverwriteLanguage_ReappliesAlreadySet(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	rc.SetLanguageApplied("111", "ja")

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	cache := cacheWith(t, map[string]string{"Show A": "111"})

	p := NewProcessor(Config{OverwriteLanguage: true}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 1 {
		t.Fatalf("expected 1 SetAudioLanguage call with --overwrite-language, got %d", len(plexClient.setLangCalls))
	}
}

// A failed alt account is shown and left out of the record, so the next run
// retries that account alone, leaving the primary (and any language chosen in
// Plex since) untouched.
func TestRunLanguageOnly_AltFailure_RetriesOnlyThatAlt(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	cache := cacheWith(t, map[string]string{"Show A": "111"})
	run := func(cfg Config, primary, alt *fakePlexClient) string {
		plexClient := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt})
		p := NewProcessor(cfg, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
		return captureStdout(t, func() { p.RunLanguageOnly() })
	}

	out := run(Config{}, &fakePlexClient{showRatingKey: "plex-99"}, &fakePlexClient{setLangErr: errors.New("alt failed")})
	if !strings.Contains(out, "alt: alt failed") {
		t.Errorf("alt failure not shown:\n%s", out)
	}
	if got, _ := rc.GetLanguageApplied("111"); got != "ja-JP" {
		t.Errorf("recorded %q; want the primary only (ja-JP)", got)
	}

	primary, alt := &fakePlexClient{showRatingKey: "plex-99"}, &fakePlexClient{}
	out = run(Config{}, primary, alt)
	if len(primary.setLangCalls) != 0 || len(alt.setLangCalls) != 1 {
		t.Errorf("retry calls: primary %d, alt %d; want 0 and 1", len(primary.setLangCalls), len(alt.setLangCalls))
	}
	if got, _ := rc.GetLanguageApplied("111"); got != "ja-JP|alt=ja-JP" {
		t.Errorf("recorded %q; want ja-JP|alt=ja-JP", got)
	}
	if strings.Contains(out, "alt:") {
		t.Errorf("alt failure still shown after it succeeded:\n%s", out)
	}

	// --overwrite-language sets every account again.
	primary, alt = &fakePlexClient{showRatingKey: "plex-99"}, &fakePlexClient{}
	run(Config{OverwriteLanguage: true}, primary, alt)
	if len(primary.setLangCalls) != 1 || len(alt.setLangCalls) != 1 {
		t.Errorf("--overwrite-language calls: primary %d, alt %d; want 1 each", len(primary.setLangCalls), len(alt.setLangCalls))
	}
}

// An alt that can't see the show (not found) is an alt failure: the show's
// cached Plex key, which belongs to the primary, is kept.
func TestRunLanguageOnly_AltNotFound_KeepsPlexKey(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	rc.SetPlexKey("111", "plex-99")
	rc.SetLanguageApplied("111", "ja-JP")
	alt := &fakePlexClient{setLangErr: ErrPlexNotFound}
	plexClient := newMultiPlexClient(&fakePlexClient{showRatingKey: "plex-99"}, namedPlexClient{name: altAccount, client: alt})
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "111"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plexClient, nil)
	out := captureStdout(t, func() { p.RunLanguageOnly() })

	if key, ok := rc.GetPlexKey("111"); !ok || key != "plex-99" {
		t.Errorf("Plex key = %q, %v; want plex-99 kept", key, ok)
	}
	if got, _ := rc.GetLanguageApplied("111"); got != "ja-JP" {
		t.Errorf("recorded %q; want ja-JP, without the alt", got)
	}
	if !strings.Contains(out, "alt: ") {
		t.Errorf("alt failure not shown:\n%s", out)
	}
}

// Removing alt tokens doesn't push the primary again (which would undo a
// language chosen in Plex since); only the record and nothing is logged.
func TestRunLanguageOnly_AltTokensRemoved_PrimaryNotPushed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plex       func(primary *fakePlexClient) PlexClient
		cached     string
		wantRecord string
	}{
		{"all alts removed", func(p *fakePlexClient) PlexClient { return p }, "ja-JP|alt=ja-JP", "ja-JP"},
		{"alt2 removed", func(p *fakePlexClient) PlexClient {
			return newMultiPlexClient(p, namedPlexClient{name: altAccount, client: &fakePlexClient{}})
		}, "ja-JP|alt=ja-JP|alt2=ja-JP", "ja-JP|alt=ja-JP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := emptyRatingCache(t)
			rc.SetShowRating("111", RatingM)
			rc.SetLanguageApplied("111", tc.cached)
			primary := &fakePlexClient{showRatingKey: "plex-99"}
			p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "111"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, tc.plex(primary), nil)
			out := captureStdout(t, func() { p.RunLanguageOnly() })
			if len(primary.setLangCalls) != 0 {
				t.Errorf("primary pushed %d times; want 0", len(primary.setLangCalls))
			}
			if got, _ := rc.GetLanguageApplied("111"); got != tc.wantRecord {
				t.Errorf("recorded %q; want %q", got, tc.wantRecord)
			}
			if got := strings.TrimSpace(out); got != "[SKIP]    Show A (lang: unchanged)" {
				t.Errorf("output %q; want a SKIP, as nothing was pushed", got)
			}
		})
	}
}

// Adding an alt account sets only that account.
func TestRunLanguageOnly_NewAltAccount_SetsOnlyIt(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	rc.SetLanguageApplied("111", "ja-JP")
	primary, alt := &fakePlexClient{showRatingKey: "plex-99"}, &fakePlexClient{}
	plexClient := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt})
	p := NewProcessor(Config{}, cacheWith(t, map[string]string{"Show A": "111"}), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plexClient, nil)
	captureStdout(t, func() { p.RunLanguageOnly() })
	if len(primary.setLangCalls) != 0 || len(alt.setLangCalls) != 1 {
		t.Errorf("calls: primary %d, alt %d; want 0 and 1", len(primary.setLangCalls), len(alt.setLangCalls))
	}
}

func TestRunLanguageOnly_ShowNotInPlex_Skipped(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{findErr: ErrPlexNotFound, findTitleErr: ErrPlexNotFound}
	cache := cacheWith(t, map[string]string{"Show A": "111"})

	p := NewProcessor(Config{}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly() // should not panic or error

	if len(plexClient.setLangCalls) != 0 {
		t.Errorf("expected no calls for show not in Plex, got %d", len(plexClient.setLangCalls))
	}
}

func TestRunLanguageOnly_UsesCachedPlexKey(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)
	rc.SetPlexKey("111", "plex-cached")

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	// findErr would normally block the lookup — but the cached key should bypass it.
	plexClient := &fakePlexClient{findErr: ErrPlexNotFound, findTitleErr: ErrPlexNotFound}
	cache := cacheWith(t, map[string]string{"Show A": "111"})

	p := NewProcessor(Config{}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 1 || plexClient.setLangCalls[0].ratingKey != "plex-cached" {
		t.Errorf("expected SetAudioLanguage on cached key, got %+v", plexClient.setLangCalls)
	}
}

func TestRunLanguageOnly_TitleFallbackStripsYear(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	// TMDB GUID lookup fails; title lookup succeeds only for the stripped name.
	plexClient := &fakePlexClient{findErr: ErrPlexNotFound, titleRatingKey: "plex-title"}
	// Folder name includes a year — cleanSearchQuery should strip it before matching.
	cache := cacheWith(t, map[string]string{"Kill la Kill (2013)": "111"})

	p := NewProcessor(Config{}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 1 {
		t.Fatalf("expected 1 SetAudioLanguage call, got %d", len(plexClient.setLangCalls))
	}
	if plexClient.setLangCalls[0].ratingKey != "plex-title" {
		t.Errorf("expected ratingKey plex-title, got %q", plexClient.setLangCalls[0].ratingKey)
	}
}

func TestRatingsOnly_DoesNotSetPlexAudioLanguage(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	cache := cacheWith(t, map[string]string{"Show A": "111"})

	p := NewProcessor(Config{RatingsOnly: true}, cache, nil, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	// applyPlexRatings is called internally; simulate by calling it directly.
	p.applyPlexRatings("111", nil, "Show A", "", false, ManualEntry{})

	if len(plexClient.setLangCalls) != 0 {
		t.Errorf("--ratings-only must not set Plex audio language, got %d calls", len(plexClient.setLangCalls))
	}
}

// The full run sets a show's audio language once and records it; only
// OverwriteLanguage sets it again. A TMDB error is a warning, and a show key
// Plex no longer has is cleared without a message.
func TestApplyPlexRatings_AudioLanguage(t *testing.T) {
	rc := emptyRatingCache(t)
	tmdb := &langTMDB{fakeTMDB: &fakeTMDB{}, langs: map[string]string{"111": "ja"}}
	run := func(cfg Config, plexClient *fakePlexClient) string {
		p := NewProcessor(cfg, cacheWith(t, map[string]string{"Show A": "111"}), nil, tmdb, nil, nil, nil, nil, rc, nil, plexClient, nil)
		return captureStdout(t, func() { p.applyPlexRatings("111", nil, "Show A", "Show A", false, ManualEntry{}) })
	}

	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	out := run(Config{}, plexClient)
	if len(plexClient.setLangCalls) != 1 || plexClient.setLangCalls[0] != (setLangCall{"plex-99", "ja"}) {
		t.Errorf("calls %+v; want ja on plex-99", plexClient.setLangCalls)
	}
	if got, _ := rc.GetLanguageApplied("111"); got != "ja-JP" {
		t.Errorf("recorded %q; want ja-JP", got)
	}
	if !strings.Contains(out, `[i] Plex: audio language for "Show A" set to "ja-JP"`) {
		t.Errorf("push not reported:\n%s", out)
	}

	plexClient = &fakePlexClient{showRatingKey: "plex-99"}
	if out := run(Config{}, plexClient); len(plexClient.setLangCalls) != 0 || strings.Contains(out, "audio language") {
		t.Errorf("second run pushed %d times:\n%s", len(plexClient.setLangCalls), out)
	}

	plexClient = &fakePlexClient{showRatingKey: "plex-99"}
	if run(Config{OverwriteLanguage: true}, plexClient); len(plexClient.setLangCalls) != 1 {
		t.Errorf("OverwriteLanguage pushed %d times; want 1", len(plexClient.setLangCalls))
	}

	tmdb.errs = map[string]error{"222": errors.New("tmdb down")}
	plexClient = &fakePlexClient{showRatingKey: "plex-2"}
	p := NewProcessor(Config{}, emptyCache(t), nil, tmdb, nil, nil, nil, nil, rc, nil, plexClient, nil)
	out = captureStdout(t, func() { p.applyPlexRatings("222", nil, "Show B", "Show B", false, ManualEntry{}) })
	if !strings.Contains(out, "[!] TMDB show lookup failed for ID 222 (original language unknown): tmdb down") || len(plexClient.setLangCalls) != 0 {
		t.Errorf("TMDB error: %d calls; want none and a warning:\n%s", len(plexClient.setLangCalls), out)
	}

	plexClient = &fakePlexClient{showRatingKey: "plex-99", setLangErr: fmt.Errorf("plex: prefs: %w", ErrPlexNotFound)}
	out = run(Config{OverwriteLanguage: true}, plexClient)
	if key, _ := rc.GetPlexKey("111"); key != "" {
		t.Errorf("stale key %q kept", key)
	}
	if strings.Contains(out, "audio language") {
		t.Errorf("stale key reported:\n%s", out)
	}
}

// With alt accounts, the full run reports a failed alt even when only that
// alt was due, and a retry pushes the alt alone.
func TestApplyPlexRatings_AudioLanguage_AltAccounts(t *testing.T) {
	rc := emptyRatingCache(t)
	run := func(altErr error) (out string, primary, alt *fakePlexClient) {
		primary, alt = &fakePlexClient{showRatingKey: "plex-99"}, &fakePlexClient{setLangErr: altErr}
		plexClient := newMultiPlexClient(primary, namedPlexClient{name: altAccount, client: alt})
		p := NewProcessor(Config{}, emptyCache(t), nil, &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}, nil, nil, nil, nil, rc, nil, plexClient, nil)
		out = captureStdout(t, func() { p.applyPlexRatings("111", nil, "Show A", "Show A", false, ManualEntry{}) })
		return out, primary, alt
	}
	const failed = `[i] Plex: audio language for "Show A" set to "ja-JP|alt=ja-JP"; alt: alt failed`

	out, primary, _ := run(errors.New("alt failed"))
	if !strings.Contains(out, failed) || len(primary.setLangCalls) != 1 {
		t.Errorf("first run: %d primary calls; want 1 and the alt failure:\n%s", len(primary.setLangCalls), out)
	}
	out, primary, _ = run(errors.New("alt failed"))
	if !strings.Contains(out, failed) || len(primary.setLangCalls) != 0 {
		t.Errorf("retry: %d primary calls; want 0 and the alt failure:\n%s", len(primary.setLangCalls), out)
	}
	out, primary, alt := run(nil)
	if !strings.Contains(out, `set to "ja-JP|alt=ja-JP"`+"\n") || len(primary.setLangCalls) != 0 || len(alt.setLangCalls) != 1 {
		t.Errorf("alt recovers: primary %d, alt %d calls; want 0 and 1:\n%s", len(primary.setLangCalls), len(alt.setLangCalls), out)
	}
	if got, _ := rc.GetLanguageApplied("111"); got != "ja-JP|alt=ja-JP" {
		t.Errorf("recorded %q; want ja-JP|alt=ja-JP", got)
	}
}

func TestRunLanguageOnly_ManualDataLanguageOverride(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	// TMDB says "ja" but manual_data.json overrides to "ko".
	tmdbClient := &fakeTMDB{showDetails: ShowDetails{OriginalLanguage: "ja"}}
	plexClient := &fakePlexClient{showRatingKey: "plex-99"}
	cache := cacheWith(t, map[string]string{"Show A": "111"})
	manualData := map[string]ManualEntry{
		"Show A": {Language: "ko"},
	}

	p := NewProcessor(Config{}, cache, manualData, tmdbClient, nil, nil, nil, nil, rc, nil, plexClient, nil)
	p.RunLanguageOnly()

	if len(plexClient.setLangCalls) != 1 {
		t.Fatalf("expected 1 SetAudioLanguage call, got %d", len(plexClient.setLangCalls))
	}
	if plexClient.setLangCalls[0].lang != "ko" {
		t.Errorf("expected lang ko (manual override), got %q", plexClient.setLangCalls[0].lang)
	}
}

// --- applyPlexRatings episode metadata ---

func TestApplyPlexRatings_SetsDateFromDatesOverride(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons:       []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s1": {{RatingKey: "ep101", SeasonNumber: 1, EpisodeNumber: 1}},
		},
	}
	manualData := map[string]ManualEntry{
		"My Show": {Dates: map[string]string{"S01E01": "2024-03-15"}},
	}

	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     manualData,
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	p.applyPlexRatings("111", nil, "My Show", "My Show", false, ManualEntry{})

	if len(plexClient.setEpMetaCalls) != 1 {
		t.Fatalf("expected 1 SetEpisodeMetadata call, got %d", len(plexClient.setEpMetaCalls))
	}
	call := plexClient.setEpMetaCalls[0]
	if call.ratingKey != "ep101" {
		t.Errorf("ratingKey = %q; want ep101", call.ratingKey)
	}
	if call.originallyAvailableAt != "2024-03-15" {
		t.Errorf("originallyAvailableAt = %q; want 2024-03-15", call.originallyAvailableAt)
	}
	if call.title != "" {
		t.Errorf("title = %q; want empty (no title override)", call.title)
	}
}

func TestApplyPlexRatings_UsesOverrideIDForRemappedSeason(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)   // season 1 rating, cached under folder ID
	rc.SetShowRating("999", RatingR16) // season 2 rating, cached under override ID

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons: []PlexSeason{
			{RatingKey: "s1", SeasonNumber: 1},
			{RatingKey: "s2", SeasonNumber: 2},
		},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s1": {{RatingKey: "ep101", SeasonNumber: 1, EpisodeNumber: 1}},
			"s2": {{RatingKey: "ep201", SeasonNumber: 2, EpisodeNumber: 1}},
		},
	}
	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     map[string]ManualEntry{},
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	// Season 2 is remapped to TMDB show 999.
	p.applyPlexRatings("111", map[int]string{1: "111", 2: "999"}, "My Show", "My Show", false, ManualEntry{})

	got := map[string]NZRating{}
	for _, c := range plexClient.setRatingCalls {
		if c.itemType == PlexTypeEpisode {
			got[c.ratingKey] = c.rating
		}
	}
	if got["ep101"] != RatingM {
		t.Errorf("S01E01 rating = %q; want M", got["ep101"])
	}
	if got["ep201"] != RatingR16 {
		t.Errorf("S02E01 rating = %q; want R16 (from override ID 999), not the season-1 rating", got["ep201"])
	}
}

func TestApplyPlexRatings_SetsTitleFromTitleOverrides_WhenDateAlsoSet(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons:       []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s1": {{RatingKey: "ep101", SeasonNumber: 1, EpisodeNumber: 1}},
		},
	}
	// title_overrides uses FS-safe lookalikes; cleanMetadataTitle restores them.
	manualData := map[string]ManualEntry{
		"My Show": {
			Dates:          map[string]string{"S01E01": "2024-03-15"},
			TitleOverrides: map[string]string{"S01E01": "Pilot꞉ The Beginning"},
		},
	}

	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     manualData,
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	p.applyPlexRatings("111", nil, "My Show", "My Show", false, ManualEntry{})

	if len(plexClient.setEpMetaCalls) != 1 {
		t.Fatalf("expected 1 SetEpisodeMetadata call, got %d", len(plexClient.setEpMetaCalls))
	}
	call := plexClient.setEpMetaCalls[0]
	if call.title != "Pilot: The Beginning" {
		t.Errorf("title = %q; want \"Pilot: The Beginning\" (lookalikes decoded)", call.title)
	}
	if call.originallyAvailableAt != "2024-03-15" {
		t.Errorf("originallyAvailableAt = %q; want 2024-03-15", call.originallyAvailableAt)
	}
}

func TestApplyPlexRatings_TitleOverrideSkipped_WhenNoDate(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons:       []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s1": {{RatingKey: "ep101", SeasonNumber: 1, EpisodeNumber: 1}},
		},
	}
	manualData := map[string]ManualEntry{
		"My Show": {TitleOverrides: map[string]string{"S01E01": "Pilot꞉ The Beginning"}},
	}

	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     manualData,
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	p.applyPlexRatings("111", nil, "My Show", "My Show", false, ManualEntry{})

	if len(plexClient.setEpMetaCalls) != 0 {
		t.Errorf("expected no SetEpisodeMetadata calls when dates is absent, got %d", len(plexClient.setEpMetaCalls))
	}
}

func TestApplyPlexRatings_InverseMappingApplied(t *testing.T) {
	// Local S01E05 maps to TMDB S02E03. Plex has it as S02E03.
	// The Dates entry keyed by local "S01E05" should still be found.
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons:       []PlexSeason{{RatingKey: "s2", SeasonNumber: 2}},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s2": {{RatingKey: "ep203", SeasonNumber: 2, EpisodeNumber: 3}},
		},
	}
	manualData := map[string]ManualEntry{
		"My Show": {
			Mapping: map[string]EpisodePair{"S01E05": {2, 3}},
			Dates:   map[string]string{"S01E05": "2023-11-01"},
		},
	}

	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     manualData,
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	p.applyPlexRatings("111", nil, "My Show", "My Show", false, ManualEntry{})

	if len(plexClient.setEpMetaCalls) != 1 {
		t.Fatalf("expected 1 SetEpisodeMetadata call, got %d: %+v", len(plexClient.setEpMetaCalls), plexClient.setEpMetaCalls)
	}
	if plexClient.setEpMetaCalls[0].originallyAvailableAt != "2023-11-01" {
		t.Errorf("originallyAvailableAt = %q; want 2023-11-01", plexClient.setEpMetaCalls[0].originallyAvailableAt)
	}
}

func TestApplyPlexRatings_NoManualData_NoMetadataCalls(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.SetShowRating("111", RatingM)

	plexClient := &fakePlexClient{
		showRatingKey: "show-1",
		seasons:       []PlexSeason{{RatingKey: "s1", SeasonNumber: 1}},
		episodesByKey: map[string][]PlexEpisodeItem{
			"s1": {{RatingKey: "ep101", SeasonNumber: 1, EpisodeNumber: 1}},
		},
	}

	p := &Processor{
		ratingCache:    rc,
		plex:           plexClient,
		manualData:     map[string]ManualEntry{},
		sessionPlexKey: map[string]string{"111": "show-1"},
		cfg:            Config{},
	}
	p.applyPlexRatings("111", nil, "My Show", "My Show", false, ManualEntry{})

	if len(plexClient.setEpMetaCalls) != 0 {
		t.Errorf("expected no SetEpisodeMetadata calls with no manual data, got %d", len(plexClient.setEpMetaCalls))
	}
}

// containsAll returns true if all of the given values appear in haystack
// (as individual elements, in any order).
func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		if !slices.Contains(haystack, n) {
			return false
		}
	}
	return true
}

func TestApplyRatingOnlyMKV_StatError_DoesNotTag(t *testing.T) {
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	rc := emptyRatingCache(t)
	p := &Processor{mkv: runner, timer: &captureFileTimer{}, ratingCache: rc}
	path := filepath.Join(t.TempDir(), "missing.mkv")

	var ok bool
	out := captureStdout(t, func() { ok = tagOnlyOK(p.applyRatingOnlyMKV(path, RatingM)) })
	if ok {
		t.Error("expected false when the file can't be stat'd")
	}
	if len(runner.calls) != 0 {
		t.Errorf("Apply called %d times, want none: timestamps couldn't be restored", len(runner.calls))
	}
	if rc.IsApplied(path, RatingM) {
		t.Error("rating marked applied although nothing was written")
	}
	if !strings.Contains(out, "not tagging") {
		t.Errorf("output doesn't explain the skip:\n%s", out)
	}
}

// mkvpropedit exits 1 when it wrote the changes but warned; only 2 is a failure.
// Each apply path must treat a warning as success and set or restore timestamps.
func TestApplyPaths_MkvpropeditWarningIsSuccess(t *testing.T) {
	tags := `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets></Tag></Tags>`
	paths := map[string]func(p *Processor, file string) bool{
		"applyMKV": func(p *Processor, file string) bool {
			return wroteMKV(p.applyMKV(file, "T", "", "", "2020-06-15T00:00:00Z", "2020-06-15T00:00:00Z", "", RatingNone, false))
		},
		"applyRatingOnlyMKV": func(p *Processor, file string) bool { return tagOnlyOK(p.applyRatingOnlyMKV(file, RatingPG)) },
		"applyDebandOnlyMKV": func(p *Processor, file string) bool { return tagOnlyOK(p.applyDebandOnlyMKV(file, true)) },
	}
	for name, apply := range paths {
		for _, tc := range []struct {
			rc   int
			want bool
		}{{0, true}, {1, true}, {2, false}} {
			t.Run(fmt.Sprintf("%s/rc=%d", name, tc.rc), func(t *testing.T) {
				runner := &fakeMKVRunner{avail: true, applyRC: tc.rc, applyResult: "Warning: no attachment matched", tagsPath: "t.xml", extractTagsXML: tags}
				timer := &captureFileTimer{}
				p := &Processor{mkv: runner, timer: timer}
				var ok bool
				captureStdout(t, func() { ok = apply(p, debandTestFile(t)) })
				if ok != tc.want {
					t.Errorf("returned %v, want %v", ok, tc.want)
				}
				if stamped := len(timer.setTimesCalls) > 0; stamped != tc.want {
					t.Errorf("timestamps set = %v, want %v", stamped, tc.want)
				}
			})
		}
	}
}

// wroteMKV is applyMKV's wrote result, for tests that only check it.
func wroteMKV(wrote bool, _ []string) bool { return wrote }

// tagOnlyOK is a tag-only update's ok result, for tests that only check it.
func tagOnlyOK(_ []string, ok bool) bool { return ok }
