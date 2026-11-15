package main

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Piped answers reach successive prompts, rather than the first prompt's
// read-ahead swallowing them.
func TestStdinPrompter_SuccessivePromptsReadSuccessiveLines(t *testing.T) {
	old := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader("M\nPG\n"))
	t.Cleanup(func() { stdinScanner = old })

	var a, b string
	captureStdout(t, func() {
		a, _ = stdinPrompter("1? ")
		b, _ = stdinPrompter("2? ")
	})

	if a != "M" || b != "PG" {
		t.Errorf("answers %q, %q; want M, PG", a, b)
	}
}

// recordingPrompter answers each prompt in turn and records the prompts.
type recordingPrompter struct {
	answers []string
	prompts []string
}

func (p *recordingPrompter) ask(msg string) (string, error) {
	p.prompts = append(p.prompts, msg)
	if len(p.answers) == 0 {
		return "", nil
	}
	ans := p.answers[0]
	p.answers = p.answers[1:]
	return ans, nil
}

// A rating found by a source is shown for confirmation: blank accepts it, and
// any other rating overrides it. Either way the answer is cached.
func TestResolveMovieRating_SourceRatingIsConfirmed(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   NZRating
	}{
		{"", RatingG},
		{"M", RatingM},
	} {
		cache := makeCache(t)
		p := &recordingPrompter{answers: []string{tc.answer}}
		src := &fakeRatingSource{name: "Src", rating: RatingG}
		rr := newMovieRatingResolver([]RatingSource{src}, cache, false, false, nil, p.ask)

		got := rr.ResolveMovieRating("12345", "Coco", "2017")

		if got != tc.want {
			t.Errorf("answer %q: got %q, want %q", tc.answer, got, tc.want)
		}
		if len(p.prompts) != 1 || !strings.Contains(p.prompts[0], "NZ rating for \"Coco\": G\n") {
			t.Errorf("answer %q: prompts = %q; want one confirmation of G", tc.answer, p.prompts)
		}
		if cached, _ := cache.Get("12345"); cached != tc.want {
			t.Errorf("answer %q: cached %q, want %q", tc.answer, cached, tc.want)
		}
	}
}

// The prompt's Plex default comes from the movie found by TMDB GUID, without a
// title lookup.
func TestResolveMovieRating_PlexDefaultPrefersTMDBGUID(t *testing.T) {
	plex := &fakePlexMovieClient{
		tmdbRatingKey:  "10",
		titleRatingKey: "20",
		ratingsByKey:   map[string]NZRating{"10": RatingR16, "20": RatingPG},
	}
	cache := makeCache(t)
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter(""))

	if got := rr.ResolveMovieRating("12345", "Coco", "2017"); got != RatingR16 {
		t.Errorf("got %q, want R16 from the GUID match", got)
	}
	if cached, _ := cache.Get("12345"); cached != RatingR16 {
		t.Errorf("cached %q; want Plex's R16, accepted with a blank answer", cached)
	}
	if len(plex.titleLookups) != 0 {
		t.Errorf("title lookups %v; want none after a GUID match", plex.titleLookups)
	}
}

// When the GUID isn't in Plex, the title lookup uses TMDB's release year, so a
// remake sharing the title doesn't lend its rating.
func TestProcessFile_PlexTitleFallbackUsesReleaseYear(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.mkv") // no year in the file name
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	plex := &fakePlexMovieClient{titleRatingKey: "77", contentRating: RatingM}
	cache := makeCache(t)
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter(""))

	captureStdout(t, func() {
		processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil)
	})

	// Both the prompt default and the rating push look the movie up.
	if len(plex.titleLookups) != 2 {
		t.Fatalf("title lookups %v; want the default's and the push's", plex.titleLookups)
	}
	for _, l := range plex.titleLookups {
		if l != [2]string{"Avengers Endgame", "2019"} {
			t.Errorf("title lookup %v; want (Avengers Endgame, 2019) from the release date", l)
		}
	}
}

// With a source rating, Plex isn't consulted for a default the prompt won't use.
func TestResolveMovieRating_SourceRatingSkipsPlexLookup(t *testing.T) {
	plex := &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500")}
	rr := newMovieRatingResolver([]RatingSource{&fakeRatingSource{name: "Src", rating: RatingG}}, makeCache(t), false, false, plex, answerPrompter(""))

	out := captureStdout(t, func() { rr.ResolveMovieRating("12345", "Coco", "2017") })

	if strings.Contains(out, "Plex") {
		t.Errorf("Plex was consulted:\n%s", out)
	}
}

// Plex having the movie but no rating is reported.
func TestResolveMovieRating_PlexFoundWithoutRating(t *testing.T) {
	plex := &fakePlexMovieClient{tmdbRatingKey: "10", contentRating: RatingNone}
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, answerPrompter(""))

	out := captureStdout(t, func() { rr.ResolveMovieRating("12345", "Coco", "2017") })

	if !strings.Contains(out, `[i] Plex: "Coco" found but no NZ content rating is set`) {
		t.Errorf("missing notice:\n%s", out)
	}
}

// After an invalid answer, the re-prompt says what blank does in that prompt.
func TestResolveMovieRating_RepromptNamesWhatBlankDoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []RatingSource
		plex    PlexMovieClient
		want    string
	}{
		{"source", []RatingSource{&fakeRatingSource{name: "Src", rating: RatingG}}, nil, "or blank to accept G: "},
		{"plex", nil, &fakePlexMovieClient{tmdbRatingKey: "10", contentRating: RatingM}, "or blank to use Plex's M: "},
		{"none", nil, nil, "or blank to skip: "},
	} {
		p := &recordingPrompter{answers: []string{"PG-13", ""}}
		rr := newMovieRatingResolver(tc.sources, makeCache(t), false, false, tc.plex, p.ask)

		captureStdout(t, func() { rr.ResolveMovieRating("12345", "Coco", "2017") })

		if len(p.prompts) != 2 || !strings.HasSuffix(p.prompts[1], tc.want) {
			t.Errorf("%s: prompts %q; want the re-prompt to end %q", tc.name, p.prompts, tc.want)
		}
	}
}

// A failed GUID lookup is reported and gives no default; it doesn't fall back
// to the title, which could match a different film.
func TestResolveMovieRating_PlexGUIDErrorGivesNoDefault(t *testing.T) {
	plex := &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500"), titleRatingKey: "77", contentRating: RatingR16}
	p := &recordingPrompter{}
	cache := makeCache(t)
	rr := newMovieRatingResolver(nil, cache, false, false, plex, p.ask)

	var got NZRating
	out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

	if got != RatingNone {
		t.Errorf("got %q; want no rating (blank answer, no default)", got)
	}
	if _, ok := cache.Get("12345"); ok {
		t.Error("a skipped rating was cached")
	}
	if len(plex.titleLookups) != 0 {
		t.Errorf("title lookups %v; want none after a lookup error", plex.titleLookups)
	}
	if !strings.Contains(out, `[!] Plex: lookup failed for "Coco": plex: 500`) {
		t.Errorf("lookup error not reported:\n%s", out)
	}
	if len(p.prompts) != 1 || strings.Contains(p.prompts[0], "Plex currently has") {
		t.Errorf("prompts = %q; want one prompt with no Plex default", p.prompts)
	}
}

// Under --overwrite-ratings with no source rating, the cached rating is kept,
// with a notice and no prompt.
func TestResolveMovieRating_OverwriteKeepsCachedWhenNoSourceHasOne(t *testing.T) {
	cache := makeCache(t)
	cache.Set("12345", RatingM)
	p := &recordingPrompter{}
	src := &fakeRatingSource{name: "Src", rating: RatingNone}
	rr := newMovieRatingResolver([]RatingSource{src}, cache, true, false, nil, p.ask)

	var got NZRating
	out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

	if got != RatingM {
		t.Errorf("got %q, want the cached M", got)
	}
	if len(p.prompts) != 0 {
		t.Errorf("prompted %q; want no prompt", p.prompts)
	}
	if !strings.Contains(out, `[i] No source has a rating for "Coco"; keeping cached M`) {
		t.Errorf("missing keep-cached notice:\n%s", out)
	}
}

// ...and says so differently when the sources failed rather than had no data.
func TestResolveMovieRating_OverwriteKeepsCachedWhenSourcesFail(t *testing.T) {
	cache := makeCache(t)
	cache.Set("12345", RatingM)
	src := &fakeRatingSource{name: "Src", err: errors.New("timeout")}
	rr := newMovieRatingResolver([]RatingSource{src}, cache, true, false, nil, answerPrompter())

	out := captureStdout(t, func() { rr.ResolveMovieRating("12345", "Coco", "2017") })

	if !strings.Contains(out, `[i] No rating source answered for "Coco"; keeping cached M`) {
		t.Errorf("missing sources-failed notice:\n%s", out)
	}
}

// Under --overwrite-ratings a source rating that differs from the cached one
// is confirmed, with the cached rating shown; one that agrees isn't asked about.
func TestResolveMovieRating_OverwriteConfirmsOnlyAChangedRating(t *testing.T) {
	for _, tc := range []struct {
		source  NZRating
		prompts int
	}{
		{RatingR16, 1},
		{RatingM, 0},
	} {
		cache := makeCache(t)
		cache.Set("12345", RatingM)
		p := &recordingPrompter{}
		src := &fakeRatingSource{name: "Src", rating: tc.source}
		rr := newMovieRatingResolver([]RatingSource{src}, cache, true, false, nil, p.ask)

		var got NZRating
		out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

		if got != tc.source {
			t.Errorf("source %s: got %q", tc.source, got)
		}
		if cached, _ := cache.Get("12345"); cached != tc.source {
			t.Errorf("source %s: cached %q", tc.source, cached)
		}
		if len(p.prompts) != tc.prompts {
			t.Errorf("source %s: %d prompts %q; want %d", tc.source, len(p.prompts), p.prompts, tc.prompts)
		}
		if tc.prompts == 1 && !strings.Contains(p.prompts[0], "R16 (cached: M)\n") {
			t.Errorf("prompt doesn't show the cached rating: %q", p.prompts[0])
		}
		if tc.prompts == 0 && out != "" {
			t.Errorf("source %s: unexpected output %q", tc.source, out)
		}
	}
}

// A movie settled once in a run isn't asked about again, including when the
// answer was blank: under --overwrite-ratings with a changed source rating,
// and with no rating anywhere.
func TestResolveMovieRating_SessionDedupAfterBlankAnswer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cached    NZRating
		overwrite bool
		source    NZRating
		want      NZRating
	}{
		{"overwrite, source differs", RatingM, true, RatingG, RatingG},
		{"no rating anywhere", RatingNone, false, RatingNone, RatingNone},
	} {
		cache := makeCache(t)
		if tc.cached != RatingNone {
			cache.Set("12345", tc.cached)
		}
		p := &recordingPrompter{}
		src := &fakeRatingSource{name: "Src", rating: tc.source}
		rr := newMovieRatingResolver([]RatingSource{src}, cache, tc.overwrite, false, nil, p.ask)

		var r1, r2 NZRating
		captureStdout(t, func() {
			r1 = rr.ResolveMovieRating("12345", "Coco", "2017")
			r2 = rr.ResolveMovieRating("12345", "Coco", "2017")
		})

		if r1 != tc.want || r2 != tc.want {
			t.Errorf("%s: got %q, %q; want %q twice", tc.name, r1, r2, tc.want)
		}
		if len(p.prompts) != 1 {
			t.Errorf("%s: %d prompts %q; want 1", tc.name, len(p.prompts), p.prompts)
		}
	}
}

// A failed rating fetch from Plex is reported and gives no default.
func TestResolveMovieRating_PlexRatingFetchErrorGivesNoDefault(t *testing.T) {
	plex := &fakePlexMovieClient{tmdbRatingKey: "10", contentRating: RatingM, contentRatingErr: errors.New("plex: 503")}
	p := &recordingPrompter{}
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, p.ask)

	var got NZRating
	out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

	if got != RatingNone {
		t.Errorf("got %q; want no rating (blank answer, no default)", got)
	}
	if !strings.Contains(out, `[!] Plex: rating fetch failed for "Coco": plex: 503`) {
		t.Errorf("fetch error not reported:\n%s", out)
	}
	if len(p.prompts) != 1 || strings.Contains(p.prompts[0], "Plex currently has") {
		t.Errorf("prompts = %q; want one prompt with no Plex default", p.prompts)
	}
}

// A lookup that succeeds with an empty rating key counts as not found: an
// empty GUID match falls back to the title, and an empty title match ends the
// search.
func TestResolveMovieRating_PlexEmptyRatingKeyIsNotFound(t *testing.T) {
	t.Run("GUID empty, title found", func(t *testing.T) {
		plex := &fakePlexMovieClient{tmdbEmptyKey: true, titleRatingKey: "77", contentRating: RatingR16}
		rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, answerPrompter(""))

		var got NZRating
		captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

		if got != RatingR16 {
			t.Errorf("got %q, want R16 from the title match", got)
		}
		if len(plex.titleLookups) != 1 {
			t.Errorf("title lookups %v; want one", plex.titleLookups)
		}
	})
	t.Run("both empty", func(t *testing.T) {
		plex := &fakePlexMovieClient{tmdbEmptyKey: true, contentRating: RatingR16}
		rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, answerPrompter(""))

		var got NZRating
		out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

		if got != RatingNone {
			t.Errorf("got %q; want no rating", got)
		}
		if !strings.Contains(out, `[i] Plex: "Coco" not found (tried TMDB GUID, then title and year)`) {
			t.Errorf("missing not-found notice:\n%s", out)
		}
	})
}

// A typed code is cached and echoed, and the prompt shows Plex's rating as
// the default.
func TestResolveMovieRating_TypedCodeIsEchoed(t *testing.T) {
	p := &recordingPrompter{answers: []string{"r16"}}
	cache := makeCache(t)
	rr := newMovieRatingResolver(nil, cache, false, false, &fakePlexMovieClient{tmdbRatingKey: "10", contentRating: RatingM}, p.ask)

	var got NZRating
	out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

	if got != RatingR16 {
		t.Errorf("got %q; want R16", got)
	}
	if cached, _ := cache.Get("12345"); cached != RatingR16 {
		t.Errorf("cached %q; want R16", cached)
	}
	if len(p.prompts) != 1 || !strings.Contains(p.prompts[0], ") [M], or blank to use Plex's M: ") {
		t.Errorf("prompts %q; want Plex's M offered as the default", p.prompts)
	}
	if !strings.Contains(out, "  → R16\n") {
		t.Errorf("choice not echoed:\n%s", out)
	}
}
