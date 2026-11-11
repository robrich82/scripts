package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

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
		if len(p.prompts) != 1 || !strings.Contains(p.prompts[0], `NZ rating for "Coco": G — press enter to confirm`) {
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
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, answerPrompter(""))

	if got := rr.ResolveMovieRating("12345", "Coco", "2017"); got != RatingR16 {
		t.Errorf("got %q, want R16 from the GUID match", got)
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
		processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil, false)
	})

	if len(plex.titleLookups) == 0 || plex.titleLookups[0] != [2]string{"Avengers Endgame", "2019"} {
		t.Errorf("title lookups %v; want (Avengers Endgame, 2019) from the release date", plex.titleLookups)
	}
}

// A failed GUID lookup is reported and gives no default; it doesn't fall back
// to the title, which could match a different film.
func TestResolveMovieRating_PlexGUIDErrorGivesNoDefault(t *testing.T) {
	plex := &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500"), titleRatingKey: "77", contentRating: RatingR16}
	p := &recordingPrompter{}
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, plex, p.ask)

	var got NZRating
	out := captureStdout(t, func() { got = rr.ResolveMovieRating("12345", "Coco", "2017") })

	if got != RatingNone {
		t.Errorf("got %q; want no rating (blank answer, no default)", got)
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

// Under --overwrite-ratings a source rating replaces the cached one, after
// confirmation.
func TestResolveMovieRating_OverwriteConfirmsSourceRating(t *testing.T) {
	cache := makeCache(t)
	cache.Set("12345", RatingM)
	src := &fakeRatingSource{name: "Src", rating: RatingR16}
	rr := newMovieRatingResolver([]RatingSource{src}, cache, true, false, nil, answerPrompter(""))

	if got := rr.ResolveMovieRating("12345", "Coco", "2017"); got != RatingR16 {
		t.Errorf("got %q, want R16 from the source", got)
	}
	if cached, _ := cache.Get("12345"); cached != RatingR16 {
		t.Errorf("cached %q, want R16", cached)
	}
}
