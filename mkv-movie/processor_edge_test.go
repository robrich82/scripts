package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- buildMovieTagXML tests ---

func TestBuildMovieTagXML_Empty_ReturnsEmpty(t *testing.T) {
	xml := buildMovieTagXML("", "", "", RatingNone)
	if xml != "" {
		t.Errorf("got %q, want empty string when all inputs are empty", xml)
	}
}

func TestBuildMovieTagXML_DateOnly_ContainsTags(t *testing.T) {
	xml := buildMovieTagXML("", "", "2021-06-15", RatingNone)
	if !strings.Contains(xml, "<String>2021-06-15</String>") {
		t.Errorf("xml missing DATE tag: %s", xml)
	}
	if !strings.Contains(xml, "<String>2021</String>") {
		t.Errorf("xml missing YEAR tag: %s", xml)
	}
}

func TestBuildMovieTagXML_YearOnly_ExpandsToFullDate(t *testing.T) {
	xml := buildMovieTagXML("", "", "2019", RatingNone)
	if !strings.Contains(xml, "<String>2019</String>") {
		t.Errorf("xml missing year string: %s", xml)
	}
}

func TestBuildMovieTagXML_Overview_EscapesXML(t *testing.T) {
	xml := buildMovieTagXML("A & <B> \"C\"", "", "2020-01-01", RatingNone)
	if strings.Contains(xml, "<B>") {
		t.Errorf("xml should escape < and >: %s", xml)
	}
	if !strings.Contains(xml, "&lt;B&gt;") {
		t.Errorf("xml should contain escaped tags: %s", xml)
	}
	if !strings.Contains(xml, "&amp;") {
		t.Errorf("xml should contain &amp;: %s", xml)
	}
	if !strings.Contains(xml, "&quot;") {
		t.Errorf("xml should contain &quot;: %s", xml)
	}
}

func TestBuildMovieTagXML_RatingIncluded(t *testing.T) {
	xml := buildMovieTagXML("", "99", "2021-01-01", RatingR16)
	if !strings.Contains(xml, "R16") {
		t.Errorf("xml should contain rating R16: %s", xml)
	}
	if !strings.Contains(xml, "LAW_RATING") {
		t.Errorf("xml should contain LAW_RATING tag: %s", xml)
	}
}

func TestBuildMovieTagXML_TMDBID_Included(t *testing.T) {
	xml := buildMovieTagXML("", "42", "", RatingNone)
	if !strings.Contains(xml, "<String>42</String>") {
		t.Errorf("xml should contain TMDB ID: %s", xml)
	}
	if !strings.Contains(xml, "TMDB_ID") {
		t.Errorf("xml should contain TMDB_ID tag: %s", xml)
	}
}

func TestBuildMovieTagXML_LongDateString_TruncatesToTen(t *testing.T) {
	xml := buildMovieTagXML("", "", "2022-08-20T00:00:00Z", RatingNone)
	if !strings.Contains(xml, "<String>2022-08-20</String>") {
		t.Errorf("xml should truncate date to 10 chars: %s", xml)
	}
}

// --- writeTempTagFile tests ---

func TestWriteTempTagFile_WritesContent(t *testing.T) {
	content := "<Tags><Tag/></Tags>"
	path, err := writeTempTagFile(content)
	if err != nil {
		t.Fatalf("writeTempTagFile: %v", err)
	}
	defer os.Remove(path)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != content {
		t.Errorf("file content = %q, want %q", string(b), content)
	}
}

func TestWriteTempTagFile_HasXMLExtension(t *testing.T) {
	path, err := writeTempTagFile("<Tags/>")
	if err != nil {
		t.Fatalf("writeTempTagFile: %v", err)
	}
	defer os.Remove(path)
	if !strings.HasSuffix(path, ".xml") {
		t.Errorf("temp file %q does not have .xml extension", path)
	}
}

// --- extractYear tests ---

func TestExtractYear_FoundInFilename(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{"The.Matrix.1999.mkv", "1999"},
		{"Inception (2010).mkv", "2010"},
		{"Movie.2099.mkv", "2099"},
		{"Movie.1900.mkv", "1900"},
	}
	for _, tc := range cases {
		got := extractYear(tc.filename)
		if got != tc.want {
			t.Errorf("extractYear(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}
}

func TestExtractYear_NotFound_ReturnsEmpty(t *testing.T) {
	cases := []string{
		"NoBrackets.mkv",
		"file.mkv",
		"1899film.mkv", // below range
		"2100film.mkv", // above range
	}
	for _, tc := range cases {
		got := extractYear(tc)
		if got != "" {
			t.Errorf("extractYear(%q) = %q, want empty", tc, got)
		}
	}
}

// --- processFile edge cases ---

func TestProcessFile_BadFilename_Skipped(t *testing.T) {
	// A filename whose title portion is empty after stripping year should be skipped.
	// "(2019).mkv" → year="2019", title="" after truncation.
	path := makeTempMKVNamed(t, "(2019).mkv")
	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails("2019-04-26")}
	result := processFile(path, tmdb, nil, &fakeFileTimer{}, nil, makeCache(t), false)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped for empty-title filename", result)
	}
}

func TestProcessFile_ApplierError_ReturnsUpdated_WithoutMarkingApplied(t *testing.T) {
	path := makeTempMKVNamed(t, "Inception.2010.mkv")
	past := time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	tmdb := &fakeTMDBMovieClient{tmdbID: "27205", details: MovieDetails{
		TMDBID:      "27205",
		Title:       "Inception",
		Overview:    "Dreams.",
		ReleaseDate: "2010-07-16",
	}}
	applier := &fakeApplier{err: os.ErrPermission}
	timer := &fakeFileTimer{}
	cache := makeCache(t)

	// When apply fails, meta should NOT be marked applied.
	processFile(path, tmdb, applier, timer, nil, cache, false)
	if cache.IsMetaApplied(path, "27205") {
		t.Error("meta should not be marked applied when Apply() returns an error")
	}
}

func TestProcessFile_CachedTMDBID_SkipsSearch(t *testing.T) {
	path := makeTempMKVNamed(t, "Interstellar.2014.mkv")
	past := time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	// Client will error on SearchAndFetch but succeed on GetMovieDetails.
	searchCalled := false
	details := MovieDetails{
		TMDBID:      "157336",
		Title:       "Interstellar",
		Overview:    "Space.",
		ReleaseDate: "2014-11-07",
	}
	tmdb := &trackingTMDBClient{
		details:        details,
		onSearchCalled: func() { searchCalled = true },
	}

	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	// Pre-seed the TMDB ID so search should be skipped.
	cache.SetTMDBID("Interstellar:2014", "157336")

	result := processFile(path, tmdb, applier, timer, nil, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if searchCalled {
		t.Error("SearchAndFetch should not be called when TMDB ID is already cached")
	}
}

func TestProcessFile_ForceFlag_ReappliesEvenWhenApplied(t *testing.T) {
	path := makeTempMKVNamed(t, "Parasite.2019.mkv")
	target := time.Date(2019, 5, 30, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	tmdb := &fakeTMDBMovieClient{tmdbID: "496243", details: MovieDetails{
		TMDBID:      "496243",
		Title:       "Parasite",
		Overview:    "Class struggle.",
		ReleaseDate: "2019-05-30",
	}}
	applier := &fakeApplier{}
	timer := &fakeFileTimer{}
	cache := makeCache(t)
	cache.SetTMDBID("Parasite:2019", "496243")
	cache.MarkMetaApplied(path, "496243")

	result := processFile(path, tmdb, applier, timer, nil, cache, true /* force */)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated when force=true", result)
	}
	if len(applier.calls) != 1 {
		t.Errorf("expected 1 Apply call under force, got %d", len(applier.calls))
	}
}

func TestProcessFile_CtimeNeedsUpdate_SetsCreationTime(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.2012.mkv")
	target := time.Date(2012, 5, 4, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, target, target)

	tmdb := &fakeTMDBMovieClient{tmdbID: "24428", details: MovieDetails{
		TMDBID:      "24428",
		Title:       "The Avengers",
		Overview:    "Heroes.",
		ReleaseDate: "2012-05-04",
	}}
	applier := &fakeApplier{}
	// Simulate a timer where ctime is out of date.
	timer := &fakeFileTimer{ctimeNeedsUpdate: true}
	cache := makeCache(t)
	cache.SetTMDBID("Avengers:2012", "24428")
	cache.MarkMetaApplied(path, "24428")

	result := processFile(path, tmdb, applier, timer, nil, cache, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated (ctime needs fixing)", result)
	}
	if len(timer.setCreationTimeCalls) == 0 {
		t.Error("SetCreationTime should be called when ctimeNeedsUpdate=true")
	}
}

// --- walkAndProcess edge cases ---

func TestWalkAndProcess_MultipleFiles_CountsAll(t *testing.T) {
	dir := t.TempDir()

	// Two findable movies, one file with unknown title.
	names := []string{
		"Avengers.Endgame.2019.mkv",
		"Inception.2010.mkv",
		"2019.mkv", // year-only filename → skipped
	}
	for _, name := range names {
		os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)
	}

	// Client always returns not-found.
	tmdb := &fakeTMDBMovieClient{tmdbID: ""}
	cache := makeCache(t)
	stats := walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, false, time.Time{})

	// All three have derivable titles and go to TMDB, which returns not-found for each.
	if stats.Skipped != 3 {
		t.Errorf("got Skipped=%d, want 3 (all TMDB-not-found)", stats.Skipped)
	}
}

func TestWalkAndProcess_SavesCache(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(t.TempDir(), "cache.json")
	cache, err := LoadMovieRatingCache(cacheFile)
	if err != nil {
		t.Fatalf("LoadMovieRatingCache: %v", err)
	}
	// Seed a rating; walkAndProcess must call cache.Save().
	cache.Set("test", RatingG)

	tmdb := &fakeTMDBMovieClient{tmdbID: ""}
	walkAndProcess(dir, tmdb, nil, &fakeFileTimer{}, nil, cache, false, time.Time{})

	if _, statErr := os.Stat(cacheFile); os.IsNotExist(statErr) {
		t.Error("cache file should be written by walkAndProcess")
	}
}

// --- ResolveMovieRating edge cases ---

func TestResolveMovieRating_NilCache_ReturnsNone(t *testing.T) {
	rr := &MovieRatingResolver{
		cache:           nil,
		sessionPrompted: make(map[string]NZRating),
	}
	rating := rr.ResolveMovieRating("key", "Title")
	if rating != RatingNone {
		t.Errorf("got %q, want RatingNone when cache is nil", rating)
	}
}

func TestResolveMovieRating_CachedRating_ReturnedWithoutPrompt(t *testing.T) {
	cache := makeCache(t)
	cache.Set("99999", RatingPG)

	promptCalled := false
	prompter := func(string) (string, error) {
		promptCalled = true
		return "", nil
	}
	rr := newMovieRatingResolver(nil, cache, false, false, nil, prompter)
	rating := rr.ResolveMovieRating("99999", "Some Film")
	if rating != RatingPG {
		t.Errorf("got %q, want PG from cache", rating)
	}
	if promptCalled {
		t.Error("prompter should not be called when rating is cached")
	}
}

func TestResolveMovieRating_SessionDedup_PromptsOnce(t *testing.T) {
	cache := makeCache(t)
	calls := 0
	prompter := func(string) (string, error) {
		calls++
		return "M", nil
	}
	rr := newMovieRatingResolver(nil, cache, false, false, nil, prompter)

	r1 := rr.ResolveMovieRating("film:2020", "Film")
	r2 := rr.ResolveMovieRating("film:2020", "Film")

	if r1 != RatingM || r2 != RatingM {
		t.Errorf("got r1=%q r2=%q, want both M", r1, r2)
	}
	if calls != 1 {
		t.Errorf("prompter called %d times, want 1 (session dedup)", calls)
	}
}

func TestResolveMovieRating_ExternalSourceError_ContinuesToNext(t *testing.T) {
	cache := makeCache(t)

	errSource := &fakeRatingSource{name: "ErrSource", err: os.ErrDeadlineExceeded}
	goodSource := &fakeRatingSource{name: "GoodSource", rating: RatingG}

	prompter := func(string) (string, error) { return "", nil }
	rr := newMovieRatingResolver([]RatingSource{errSource, goodSource}, cache, false, false, nil, prompter)

	rating := rr.ResolveMovieRating("film:2021", "Film")
	if rating != RatingG {
		t.Errorf("got %q, want G (from second source)", rating)
	}
}

// --- helpers ---

// trackingTMDBClient records whether SearchAndFetch was called.
type trackingTMDBClient struct {
	details        MovieDetails
	onSearchCalled func()
}

func (c *trackingTMDBClient) SearchAndFetch(_, _ string) (string, MovieDetails, error) {
	if c.onSearchCalled != nil {
		c.onSearchCalled()
	}
	return c.details.TMDBID, c.details, nil
}

func (c *trackingTMDBClient) GetMovieDetails(_ string) (MovieDetails, error) {
	return c.details, nil
}

// fakeRatingSource is a controllable RatingSource for tests.
type fakeRatingSource struct {
	name   string
	rating NZRating
	err    error
}

func (f *fakeRatingSource) Name() string { return f.name }
func (f *fakeRatingSource) FetchRating(_, _ string) (NZRating, error) {
	return f.rating, f.err
}
