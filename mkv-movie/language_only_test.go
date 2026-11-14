package main

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// moviesTMDB serves each movie's details by TMDB ID, counting the calls.
type moviesTMDB struct {
	details map[string]MovieDetails
	errs    map[string]error
	fetches int
}

func (m *moviesTMDB) SearchAndFetch(_, _ string) (string, MovieDetails, error) {
	return "", MovieDetails{}, errors.New("--language-only must not search TMDB")
}

func (m *moviesTMDB) GetMovieDetails(id string) (MovieDetails, error) {
	m.fetches++
	if err := m.errs[id]; err != nil {
		return MovieDetails{}, err
	}
	return m.details[id], nil
}

// langPlex finds movies like fakePlexMovieClient and records the language of
// each SetMovieStreams call, reporting a change when changed is set.
type langPlex struct {
	fakePlexMovieClient
	changed   bool
	streamErr error
	langs     []string
	lastLangs AudioLangs
}

func (p *langPlex) SetMovieStreams(_ string, langs AudioLangs) (bool, bool, error) {
	p.langs = append(p.langs, langs.Default)
	p.lastLangs = langs
	if p.streamErr != nil {
		return false, false, p.streamErr
	}
	return p.changed, false, nil
}

var (
	cocoDetails     = MovieDetails{TMDBID: "354912", Title: "Coco", ReleaseDate: "2017-10-27", OriginalLanguage: "en"}
	spiritedDetails = MovieDetails{TMDBID: "129", Title: "Spirited Away", ReleaseDate: "2001-07-20", OriginalLanguage: "ja"}
)

// languageCache is a cache mapping files, which don't exist, to movies.
func languageCache(t *testing.T, files map[string]string) *MovieRatingCache {
	t.Helper()
	cache := makeCache(t)
	dir := t.TempDir()
	for name, id := range files {
		cache.SetFileTMDBID(filepath.Join(dir, "missing", name), id)
	}
	return cache
}

func languageOnlyRun(t *testing.T, tmdb TMDBMovieClient, plex PlexMovieClient, cache *MovieRatingCache, manualData map[string]MovieManualEntry, overwrite bool) (Stats, string) {
	t.Helper()
	var stats Stats
	out := captureStdout(t, func() {
		stats = walkLanguageOnly(tmdb, plex, cache, manualData, overwrite)
	})
	return stats, out
}

func reloaded(t *testing.T, cache *MovieRatingCache) *MovieRatingCache {
	t.Helper()
	saved, err := LoadMovieRatingCache(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// #134: every cached movie gets its streams, in name order, without its file
// being read; the record is saved, so a second run sets nothing.
func TestLanguageOnly_SetsEveryCachedMovie(t *testing.T) {
	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129", "Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails, "354912": cocoDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Updated != 2 || stats.Skipped != 0 || stats.Errors != 0 {
		t.Errorf("stats %+v; want 2 updated", stats)
	}
	want := "[UPDATED] Coco (2017) (lang: en)\n[UPDATED] Spirited Away (2001) (lang: ja)\n"
	if !strings.Contains(out, want) {
		t.Errorf("output isn't %q:\n%s", want, out)
	}
	if tmdb.fetches != 2 {
		t.Errorf("TMDB details calls = %d; want one per movie", tmdb.fetches)
	}
	saved := reloaded(t, cache)
	if got, _ := saved.GetStreamsApplied("129"); got != "ja" {
		t.Errorf("saved record for 129 = %q; want ja", got)
	}

	plex.langs = nil
	stats, out = languageOnlyRun(t, tmdb, plex, saved, nil, false)
	if stats.Skipped != 2 || len(plex.langs) != 0 {
		t.Errorf("second run: stats %+v, stream sets %v; want both skipped, nothing set", stats, plex.langs)
	}
	if !strings.Contains(out, "[SKIP]    Coco (2017) (streams set earlier)") {
		t.Errorf("second run output:\n%s", out)
	}
}

// --overwrite-streams sets a movie's streams again; Plex already having them
// is a skip, not an update.
func TestLanguageOnly_OverwriteStreams(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	cache.SetStreamsApplied("354912", "en")
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, true)

	if !slices.Equal(plex.langs, []string{"en"}) {
		t.Errorf("stream sets %v; want [en]", plex.langs)
	}
	if stats.Skipped != 1 || !strings.Contains(out, "[SKIP]    Coco (2017) (lang: unchanged)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
}

// A movie's languages come from the first of its files' manual_data.json
// entries that sets one; an entry with only "deband" doesn't hide it. With
// "language" set, TMDB is called only once the streams are due.
func TestLanguageOnly_ManualDataLanguage(t *testing.T) {
	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129", "Spirited Away (2001) Remastered.mkv": "129"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true}
	deband := true
	manual := map[string]MovieManualEntry{
		"Spirited Away (2001)":            {Deband: &deband},
		"Spirited Away (2001) Remastered": {Language: "en"},
	}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, manual, false)

	if stats.Updated != 1 || !slices.Equal(plex.langs, []string{"en"}) {
		t.Errorf("stats %+v, stream sets %v; want one movie set to en", stats, plex.langs)
	}
	if !strings.Contains(out, "[UPDATED] Spirited Away (2001) (lang: en)") {
		t.Errorf("output:\n%s", out)
	}

	tmdb.fetches = 0
	stats, _ = languageOnlyRun(t, tmdb, plex, reloaded(t, cache), manual, false)
	if stats.Skipped != 1 || tmdb.fetches != 0 {
		t.Errorf("second run: stats %+v, TMDB calls %d; want a skip with no TMDB call", stats, tmdb.fetches)
	}
}

// Each language key comes from the first file whose entry sets it, so an
// entry with only an alt language doesn't hide another file's "language".
func TestLanguageOnly_ManualDataMergedPerKey(t *testing.T) {
	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129", "Spirited Away (2001) Remastered.mkv": "129"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true, streamErr: fmt.Errorf("%w: en, key 10", ErrPlexNoAudioStream)}
	manual := map[string]MovieManualEntry{
		"Spirited Away (2001)":            {AltLanguage: "ja"},
		"Spirited Away (2001) Remastered": {Language: "en", AltLanguage: "fr", Alt2Language: "de"},
	}

	_, out := languageOnlyRun(t, tmdb, plex, cache, manual, false)

	want := map[string]string{altAccount: "ja", alt2Account: "de"}
	if plex.lastLangs.Default != "en" || !maps.Equal(plex.lastLangs.Alts, want) {
		t.Errorf("streams set to %+v; want en, alts %v", plex.lastLangs, want)
	}
	if !strings.Contains(out, `Set "language" for "Spirited Away (2001) Remastered" in manual_data.json`) {
		t.Errorf("the hint should name the entry that set \"language\":\n%s", out)
	}
}

// A file a hand-edited cache maps to an empty TMDB ID is left alone: Plex
// would be searched for "", which can match an unrelated movie.
func TestLanguageOnly_EmptyTMDBIDSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{"files":{"C:\\m\\Coco (2017).mkv":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := LoadMovieRatingCache(path)
	if err != nil {
		t.Fatal(err)
	}
	tmdb := &moviesTMDB{}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}

	stats, _ := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats != (Stats{}) || tmdb.fetches != 0 || len(plex.langs) != 0 {
		t.Errorf("stats %+v, TMDB calls %d, stream sets %v; want nothing done", stats, tmdb.fetches, plex.langs)
	}
}

// A movie Plex doesn't have is searched by TMDB's title and year, not the
// file's; not finding it is a warning, and nothing is recorded.
func TestLanguageOnly_NotInPlex(t *testing.T) {
	cache := languageCache(t, map[string]string{"Thing 1982.mkv": "1091"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"1091": {Title: "The Thing", ReleaseDate: "1982-06-25", OriginalLanguage: "en"}}}
	plex := &langPlex{}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || stats.Errors != 0 {
		t.Errorf("stats %+v; want 1 skipped", stats)
	}
	if !slices.Equal(plex.titleLookups, [][2]string{{"The Thing", "1982"}}) {
		t.Errorf("title lookups %v; want TMDB's title and year", plex.titleLookups)
	}
	for _, want := range []string{`Plex: "The Thing" not found`, "rerun with --language-only", "[SKIP]    Thing 1982 (not in Plex)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, ok := reloaded(t, cache).GetStreamsApplied("1091"); ok || len(plex.langs) != 0 {
		t.Error("streams set or recorded for a movie Plex doesn't have")
	}
}

// Failures are errors that don't stop the run, and leave nothing recorded.
func TestLanguageOnly_Failures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tmdbErr  error
		plex     *langPlex
		wantLine string
		wantHint string
	}{
		{"TMDB details", errors.New("timeout"), &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}, "[ERR]     Coco (2017): TMDB details: timeout\n", ""},
		{"Plex lookup", nil, &langPlex{fakePlexMovieClient: fakePlexMovieClient{findTMDBErr: errors.New("status 500")}}, "[ERR]     Coco (2017) (failed: Plex lookup)\n", "Plex lookup failed – streams not set: status 500"},
		// The hint names the file whose entry set the language, not the first.
		{"no audio track", nil, &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, streamErr: fmt.Errorf("%w: ko, key 10", ErrPlexNoAudioStream)}, "[ERR]     Coco (2017) (failed: Plex streams)\n", `Set "language" for "Coco (2017) Korean dub" in manual_data.json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912", "Coco (2017) Korean dub.mkv": "354912", "Spirited Away (2001).mkv": "129"})
			tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails, "129": spiritedDetails}, errs: map[string]error{"354912": tc.tmdbErr}}
			if tc.tmdbErr == nil {
				tmdb.errs = nil
			}

			manual := map[string]MovieManualEntry{"Coco (2017) Korean dub": {Language: "ko"}}
			if tc.name == "TMDB details" {
				manual = nil // so Coco needs TMDB's language
			}

			stats, out := languageOnlyRun(t, tmdb, tc.plex, cache, manual, false)

			if stats.Errors < 1 || !strings.Contains(out, tc.wantLine) {
				t.Errorf("stats %+v; output lacks %q:\n%s", stats, tc.wantLine, out)
			}
			if tc.wantHint != "" && !strings.Contains(out, tc.wantHint) {
				t.Errorf("output lacks %q:\n%s", tc.wantHint, out)
			}
			if _, ok := reloaded(t, cache).GetStreamsApplied("354912"); ok {
				t.Error("failed movie recorded")
			}
			if !strings.Contains(out, "Spirited Away (2001)") {
				t.Errorf("run stopped at the failure:\n%s", out)
			}
		})
	}
}

// With no language from manual_data.json or TMDB, nothing is set.
func TestLanguageOnly_NoLanguage(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": {Title: "Coco"}}}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || len(plex.langs) != 0 {
		t.Errorf("stats %+v, stream sets %v; want a skip", stats, plex.langs)
	}
	if !strings.Contains(out, `[SKIP]    Coco (2017) (TMDB has no original language; set "language" for "Coco (2017)" in manual_data.json)`) {
		t.Errorf("output:\n%s", out)
	}
}

// #141 in --language-only: a failed alt is a warning shown on the line and
// left out of the record; the next run sets that alt alone.
func TestLanguageOnly_AltAccounts_RetryOnlyFailed(t *testing.T) {
	cache := languageCache(t, map[string]string{"Coco (2017).mkv": "354912"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"354912": cocoDetails}}
	primary := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}}
	plex := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: failingAlt()})

	stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

	if stats.Skipped != 1 || !strings.Contains(out, "[SKIP]    Coco (2017) (lang: unchanged; alt: alt failed)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
	saved := reloaded(t, cache)
	if got, _ := saved.GetStreamsApplied("354912"); got != "en" {
		t.Fatalf("recorded %q; want en, without the failed alt", got)
	}

	alt := &fakeMultiPlexMovieClient{}
	primary.langs = nil
	plex = newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: alt})
	stats, out = languageOnlyRun(t, tmdb, plex, saved, nil, false)

	if len(primary.langs) != 0 || alt.setStreamsCalls != 1 {
		t.Errorf("retry: primary sets %v, alt calls %d; want only the alt set", primary.langs, alt.setStreamsCalls)
	}
	if stats.Updated != 1 || !strings.Contains(out, "[UPDATED] Coco (2017) (lang: en|alt=en)") {
		t.Errorf("retry stats %+v:\n%s", stats, out)
	}
	if got, _ := reloaded(t, saved).GetStreamsApplied("354912"); got != "en|alt=en" {
		t.Errorf("recorded %q; want en|alt=en", got)
	}
}

// Integration: the real Plex client against a server. The movie is found by
// its TMDB GUID, its Japanese audio and English subtitle are selected, and the
// record reaches the cache file.
func TestLanguageOnly_HTTPPlex(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut:
			mu.Lock()
			puts = append(puts, r.URL.Path+"?"+r.URL.RawQuery)
			mu.Unlock()
		case r.URL.Path == "/library/sections":
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		case strings.HasPrefix(r.URL.Path, "/library/sections/2/"):
			fmt.Fprint(w, plexAllMoviesJSON("77", "tmdb://129"))
		case r.URL.Path == "/library/metadata/77":
			fmt.Fprint(w, streamsMetaJSON("ja-JP", false, false)) // Plex tags streams with a region
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cache := languageCache(t, map[string]string{"Spirited Away (2001).mkv": "129"})
	tmdb := &moviesTMDB{details: map[string]MovieDetails{"129": spiritedDetails}}

	stats, out := languageOnlyRun(t, tmdb, newHTTPPlexMovieClient(srv.URL, "tok"), cache, nil, false)

	if stats.Updated != 1 || !strings.Contains(out, "[UPDATED] Spirited Away (2001) (lang: ja)") {
		t.Errorf("stats %+v:\n%s", stats, out)
	}
	want := []string{"/library/parts/100?audioStreamID=201", "/library/parts/100?subtitleStreamID=301"}
	if len(puts) != 2 || !strings.HasPrefix(puts[0], want[0]) || !strings.HasPrefix(puts[1], want[1]) {
		t.Errorf("PUTs %v; want %v", puts, want)
	}
	if got, _ := reloaded(t, cache).GetStreamsApplied("129"); got != "ja" {
		t.Errorf("saved record = %q; want ja", got)
	}
}

// Movies go in order of their first file name, whatever the cache's map order.
func TestLanguageOnly_NameOrder(t *testing.T) {
	names := []string{"Akira (1988)", "Brazil (1985)", "Coco (2017)", "Dune (2021)", "Elf (2003)", "Fargo (1996)"}
	files := make(map[string]string)
	details := make(map[string]MovieDetails)
	for i, n := range names {
		id := fmt.Sprint(100 + len(names) - i) // IDs in reverse name order
		files[n+".mkv"] = id
		details[id] = MovieDetails{Title: n, ReleaseDate: "2000-01-01", OriginalLanguage: string(rune('a'+i)) + "x"}
	}
	plex := &langPlex{fakePlexMovieClient: fakePlexMovieClient{tmdbRatingKey: "10"}, changed: true}

	languageOnlyRun(t, &moviesTMDB{details: details}, plex, languageCache(t, files), nil, false)

	if want := []string{"ax", "bx", "cx", "dx", "ex", "fx"}; !slices.Equal(plex.langs, want) {
		t.Errorf("streams set in order %v; want %v", plex.langs, want)
	}
}

// With no TMDB release date, Plex's title search uses the year in the file
// name; with no year there either, only the TMDB GUID is tried.
func TestLanguageOnly_YearFromFileName(t *testing.T) {
	for _, tc := range []struct {
		file        string
		wantLookups [][2]string
		wantWarning string
	}{
		{"Thing 1982.mkv", [][2]string{{"The Thing", "1982"}}, "rerun with --language-only once Plex has it"},
		{"Thing.mkv", nil, "with no release year it isn't searched by title – streams not set; rerun with --language-only once Plex has matched it"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			cache := languageCache(t, map[string]string{tc.file: "1091"})
			tmdb := &moviesTMDB{details: map[string]MovieDetails{"1091": {Title: "The Thing", OriginalLanguage: "en"}}}
			plex := &langPlex{}

			stats, out := languageOnlyRun(t, tmdb, plex, cache, nil, false)

			if stats.Skipped != 1 || !slices.Equal(plex.titleLookups, tc.wantLookups) {
				t.Errorf("stats %+v, title lookups %v; want a skip and %v", stats, plex.titleLookups, tc.wantLookups)
			}
			if !strings.Contains(out, tc.wantWarning) {
				t.Errorf("output lacks %q:\n%s", tc.wantWarning, out)
			}
		})
	}
}

// Integration: a section's listing is fetched once a run and answers later
// lookups; a movie it lacks is looked for in one fresh listing, as Plex may
// have added it since.
func TestHTTPPlexMovieClient_ReusesSectionListing(t *testing.T) {
	var mu sync.Mutex
	listings := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/sections":
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		case "/library/sections/2/all":
			mu.Lock()
			listings++
			mu.Unlock()
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[`+
				`{"ratingKey":"77","Guid":[{"id":"tmdb://129"}]},`+
				`{"ratingKey":"78","Guid":[{"id":"tmdb://354912"}]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	for id, want := range map[string]string{"129": "77", "354912": "78"} {
		if rk, err := c.FindMovieByTMDBID(id); err != nil || rk != want {
			t.Errorf("FindMovieByTMDBID(%s) = %q, %v; want %s", id, rk, err, want)
		}
	}
	if listings != 1 {
		t.Errorf("section listed %d times for two movies; want 1", listings)
	}

	if _, err := c.FindMovieByTMDBID("999"); !errors.Is(err, ErrPlexMovieNotFound) {
		t.Errorf("missing movie: err %v; want ErrPlexMovieNotFound", err)
	}
	if listings != 2 {
		t.Errorf("section listed %d times in all; a miss should list it once more", listings)
	}
}

// sectionServer serves two movie sections, "1" and "2". Each listing request
// gets the next body in that section's list (the last repeats), or a 500 for "".
// It returns how often each section was listed.
func sectionServer(t *testing.T, bodies map[string][]string) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	listed := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"movie"}]}}`)
			return
		}
		for section, list := range bodies {
			if r.URL.Path != "/library/sections/"+section+"/all" {
				continue
			}
			mu.Lock()
			n := listed[section]
			listed[section]++
			mu.Unlock()
			body := list[min(n, len(list)-1)]
			if body == "" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(listed)
	}
}

// A miss lists each section once: one the run hasn't listed yet isn't listed
// again by the fresh pass.
func TestHTTPPlexMovieClient_MissListsEachSectionOnce(t *testing.T) {
	srv, listed := sectionServer(t, map[string][]string{
		"1": {plexAllMoviesJSON("77", "tmdb://129")},
		"2": {plexAllMoviesJSON("88", "tmdb://354912")},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	if rk, err := c.FindMovieByTMDBID("129"); err != nil || rk != "77" {
		t.Fatalf("FindMovieByTMDBID(129) = %q, %v; want 77", rk, err)
	}
	if got := listed(); got["1"] != 1 || got["2"] != 0 {
		t.Fatalf("after a hit in section 1: listings %v; want section 1 once, section 2 never", got)
	}

	if _, err := c.FindMovieByTMDBID("999"); !errors.Is(err, ErrPlexMovieNotFound) {
		t.Fatalf("missing movie: err %v; want ErrPlexMovieNotFound", err)
	}
	if got := listed(); got["1"] != 2 || got["2"] != 1 {
		t.Errorf("after a miss: listings %v; want each section listed once more by the miss", got)
	}
}

// A movie Plex added after the run's first listing is found by the fresh pass.
func TestHTTPPlexMovieClient_FreshPassFindsAddedMovie(t *testing.T) {
	srv, _ := sectionServer(t, map[string][]string{
		"1": {
			plexAllMoviesJSON("77", "tmdb://129"),
			`{"MediaContainer":{"Metadata":[{"ratingKey":"77","Guid":[{"id":"tmdb://129"}]},{"ratingKey":"78","Guid":[{"id":"tmdb://354912"}]}]}}`,
		},
		"2": {`{"MediaContainer":{"Metadata":[]}}`},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	if _, err := c.FindMovieByTMDBID("129"); err != nil {
		t.Fatal(err)
	}
	if rk, err := c.FindMovieByTMDBID("354912"); err != nil || rk != "78" {
		t.Errorf("FindMovieByTMDBID(354912) = %q, %v; want 78 from the fresh listing", rk, err)
	}
}

// A listing that fails in the fresh pass is an error, not "not found".
func TestHTTPPlexMovieClient_FreshPassListingError(t *testing.T) {
	srv, _ := sectionServer(t, map[string][]string{
		"1": {plexAllMoviesJSON("77", "tmdb://129"), ""},
		"2": {plexAllMoviesJSON("88", "tmdb://354912")},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	if _, err := c.FindMovieByTMDBID("129"); err != nil {
		t.Fatal(err)
	}
	_, err := c.FindMovieByTMDBID("999")
	if err == nil || errors.Is(err, ErrPlexMovieNotFound) || !strings.Contains(err.Error(), "list section 1") {
		t.Errorf("err %v; want the section 1 listing error", err)
	}
}

func moviesJSON(movies ...string) string {
	return `{"MediaContainer":{"Metadata":[` + strings.Join(movies, ",") + `]}}`
}

func movieJSON(ratingKey, title string, year int, guid string) string {
	return fmt.Sprintf(`{"ratingKey":%q,"title":%q,"year":%d,"Guid":[{"id":%q}]}`, ratingKey, title, year, guid)
}

// #172: findPlexMovie searches by title right after a GUID miss; the miss has
// just listed every section, so the title search reuses those listings.
func TestHTTPPlexMovieClient_TitleLookupReusesListing(t *testing.T) {
	srv, listed := sectionServer(t, map[string][]string{
		"1": {moviesJSON(movieJSON("77", "Spirited Away", 2001, "imdb://tt0245429"))},
		"2": {moviesJSON(movieJSON("88", "Your Name.", 2016, "tmdb://372058"))},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	if _, err := c.FindMovieByTMDBID("129"); !errors.Is(err, ErrPlexMovieNotFound) {
		t.Fatalf("FindMovieByTMDBID(129): err %v; want ErrPlexMovieNotFound", err)
	}
	if got := listed(); got["1"] != 1 || got["2"] != 1 {
		t.Fatalf("after the GUID miss: listings %v; want each section once", got)
	}
	if rk, err := c.FindMovieByTitle("Spirited Away", "2001"); err != nil || rk != "77" {
		t.Errorf("FindMovieByTitle(Spirited Away) = %q, %v; want 77", rk, err)
	}
	if rk, err := c.FindMovieByTitle("your name", ""); err != nil || rk != "88" {
		t.Errorf("FindMovieByTitle(your name) = %q, %v; want 88", rk, err)
	}
	if got := listed(); got["1"] != 1 || got["2"] != 1 {
		t.Errorf("after two title hits: listings %v; want no more", got)
	}
}

// The title search keeps its year tolerance of one year either way when it
// matches against a cached listing.
func TestHTTPPlexMovieClient_TitleLookupYearTolerance(t *testing.T) {
	srv, listed := sectionServer(t, map[string][]string{
		"1": {moviesJSON(movieJSON("10", "Dune", 1984, "tmdb://841"))},
		"2": {moviesJSON(movieJSON("20", "Dune", 2021, "tmdb://438631"))},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	for _, tc := range []struct{ year, want string }{
		{"2021", "20"},
		{"2022", "20"},
		{"1985", "10"},
		{"", "10"}, // no year: the first title match
	} {
		if rk, err := c.FindMovieByTitle("Dune", tc.year); err != nil || rk != tc.want {
			t.Errorf("FindMovieByTitle(Dune, %q) = %q, %v; want %s", tc.year, rk, err, tc.want)
		}
	}
	if got := listed(); got["1"] != 1 || got["2"] != 1 {
		t.Errorf("after four title hits: listings %v; want each section once", got)
	}
	for _, year := range []string{"2023", "1986", "2000"} { // two years off is too far
		if _, err := c.FindMovieByTitle("Dune", year); !errors.Is(err, ErrPlexMovieNotFound) {
			t.Errorf("FindMovieByTitle(Dune, %s): err %v; want ErrPlexMovieNotFound", year, err)
		}
	}
}

// A movie Plex added after the run's first listing is found by title in the
// fresh pass.
func TestHTTPPlexMovieClient_TitleLookupFreshPassFindsAddedMovie(t *testing.T) {
	srv, _ := sectionServer(t, map[string][]string{
		"1": {
			moviesJSON(movieJSON("77", "Spirited Away", 2001, "tmdb://129")),
			moviesJSON(movieJSON("77", "Spirited Away", 2001, "tmdb://129"), movieJSON("78", "Your Name.", 2016, "imdb://tt5311514")),
		},
		"2": {moviesJSON()},
	})
	c := newHTTPPlexMovieClient(srv.URL, "tok")

	if _, err := c.FindMovieByTMDBID("129"); err != nil {
		t.Fatal(err)
	}
	if rk, err := c.FindMovieByTitle("Your Name", "2016"); err != nil || rk != "78" {
		t.Errorf("FindMovieByTitle(Your Name) = %q, %v; want 78 from the fresh listing", rk, err)
	}
}

// A listing that fails on the title path is an error, not "not found".
func TestHTTPPlexMovieClient_TitleLookupListingError(t *testing.T) {
	srv, _ := sectionServer(t, map[string][]string{
		"1": {""},
		"2": {moviesJSON(movieJSON("88", "Your Name.", 2016, "tmdb://372058"))},
	})

	_, err := newHTTPPlexMovieClient(srv.URL, "tok").FindMovieByTitle("Your Name", "2016")
	if err == nil || errors.Is(err, ErrPlexMovieNotFound) || !strings.Contains(err.Error(), "list section 1") {
		t.Errorf("err %v; want the section 1 listing error", err)
	}
}

// The title search never lists a show section.
func TestHTTPPlexMovieClient_TitleLookupSkipsShowSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/library/sections" {
			t.Errorf("listed %s; want only the section list", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, plexMovieSectionsJSON("1", "show"))
	}))
	defer srv.Close()

	if _, err := newHTTPPlexMovieClient(srv.URL, "tok").FindMovieByTitle("Spirited Away", "2001"); !errors.Is(err, ErrPlexMovieNotFound) {
		t.Errorf("err %v; want ErrPlexMovieNotFound", err)
	}
}

// RefreshLibraries reports a failed request for the section list.
func TestHTTPPlexMovieClient_RefreshLibraries_SectionsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := newHTTPPlexMovieClient(srv.URL, "tok").RefreshLibraries()
	if err == nil || !strings.Contains(err.Error(), "list sections") {
		t.Errorf("err %v; want the section list error", err)
	}
}
