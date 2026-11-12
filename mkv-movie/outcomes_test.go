package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// outcomeRun processes testDetails' movie once, with a cached R16 rating and
// TMDB original language "en" so both a rating and streams are due in Plex.
func outcomeRun(t *testing.T, applier MKVApplier, timer *fakeFileTimer, plex *fakePlexMovieClient, details MovieDetails) (FileResult, string, *MovieRatingCache) {
	t.Helper()
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	cache := makeCache(t)
	cache.Set("12345", RatingR16)
	var rr *MovieRatingResolver
	if plex != nil {
		rr = newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())
	}
	var result FileResult
	out := captureStdout(t, func() {
		result = processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: details}, applier, timer, rr, cache, nil)
	})
	return result, out, cache
}

func detailsWithLanguage() MovieDetails {
	d := testDetails()
	d.OriginalLanguage = "en"
	return d
}

// A failed step after the tags were written is an error, named on the result
// line, but the file is still backdated: its tags are right.
func TestProcessFile_FailedStepAfterTagging_IsError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plex       *fakePlexMovieClient
		timerErr   error
		wantFailed string
	}{
		{"Plex lookup", &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500")}, nil, "(failed: Plex lookup)"},
		{"Plex rating", &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}, nil, "(failed: Plex rating)"},
		{"Plex streams", &fakePlexMovieClient{tmdbRatingKey: "10", setStreamsErr: errors.New("plex: 403")}, nil, "(failed: Plex streams)"},
		{"both pushes", &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("x"), setStreamsErr: errors.New("y")}, nil, "(failed: Plex rating, Plex streams)"},
		{"mtime", &fakePlexMovieClient{tmdbRatingKey: "10"}, errors.New("access denied"), "(failed: mtime)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timer := &fakeFileTimer{setTimesErr: tc.timerErr}
			result, out, _ := outcomeRun(t, &fakeApplier{}, timer, tc.plex, detailsWithLanguage())

			if result != ResultError {
				t.Errorf("got %v, want ResultError", result)
			}
			if !strings.Contains(out, "[ERR]     Avengers.Endgame.2019.mkv -> 2019-04-26") || !strings.Contains(out, tc.wantFailed) {
				t.Errorf("result line doesn't name %q:\n%s", tc.wantFailed, out)
			}
			if strings.Contains(out, "[UPDATED]") {
				t.Errorf("reported updated:\n%s", out)
			}
			if len(timer.setTimesCalls) != 1 || len(timer.setCreationTimeCalls) != 1 {
				t.Errorf("mtime %v, ctime %v; want the tagged file backdated", timer.setTimesCalls, timer.setCreationTimeCalls)
			}
		})
	}
}

// A failed poster download is an error too: the tags are written with the old
// cover, and the result says the poster failed.
func TestProcessFile_PosterFailure_IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	orig := tmdbImageBase
	tmdbImageBase = srv.URL
	t.Cleanup(func() { tmdbImageBase = orig })

	details := testDetails()
	details.PosterPath = "/poster.jpg"
	applier := &fakeApplier{}
	result, out, _ := outcomeRun(t, applier, &fakeFileTimer{}, nil, details)

	if result != ResultError || !strings.Contains(out, "(failed: poster)") {
		t.Errorf("got %v; want ResultError naming the poster:\n%s", result, out)
	}
	if len(applier.calls) != 1 || applier.calls[0].posterPath != "" {
		t.Errorf("Apply calls %+v; want one, with no poster", applier.calls)
	}
}

// A movie Plex doesn't have yet isn't recorded as pushed, so a later run
// pushes once Plex has picked it up. Its file still counts as updated.
func TestProcessFile_NotInPlex_RecordsNothing(t *testing.T) {
	plex := &fakePlexMovieClient{findTitleErr: ErrPlexMovieNotFound}
	result, out, cache := outcomeRun(t, &fakeApplier{}, &fakeFileTimer{}, plex, detailsWithLanguage())

	if result != ResultUpdated || !strings.Contains(out, "Not in Plex library") {
		t.Errorf("got %v; want ResultUpdated with the not-in-Plex notice:\n%s", result, out)
	}
	if _, ok := cache.GetPlexRating("12345"); ok {
		t.Error("Plex rating recorded for a movie not in Plex")
	}
	if _, ok := cache.GetStreamsApplied("12345"); ok {
		t.Error("streams recorded for a movie not in Plex")
	}
}

// #142: without MKVToolNix a file is skipped, never reported updated, and the
// line says when a rating reached Plex only. A failed Plex push makes it an error.
func TestProcessFile_NoApplier_Outcomes(t *testing.T) {
	t.Run("rating pushed", func(t *testing.T) {
		timer := &fakeFileTimer{}
		result, out, _ := outcomeRun(t, nil, timer, &fakePlexMovieClient{tmdbRatingKey: "10"}, testDetails())
		if result != ResultSkipped || !strings.Contains(out, "[SKIP]") || !strings.Contains(out, "(Plex rating: R16)") {
			t.Errorf("got %v; want a SKIP line naming the Plex rating:\n%s", result, out)
		}
		if strings.Contains(out, "[UPDATED]") || len(timer.setTimesCalls) != 0 {
			t.Errorf("reported updated or backdated without MKVToolNix:\n%s", out)
		}
	})
	t.Run("push failed", func(t *testing.T) {
		result, out, _ := outcomeRun(t, nil, &fakeFileTimer{}, &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}, testDetails())
		if result != ResultError || !strings.Contains(out, "(failed: Plex rating)") {
			t.Errorf("got %v; want ResultError naming the Plex rating:\n%s", result, out)
		}
	})
}

// The summary counts a failed file as an error, not an update.
func TestWalkAndProcess_FailedStepCountsAsError(t *testing.T) {
	dir := t.TempDir()
	writeMovieFile(t, dir, "Avengers Endgame (2019).mkv")
	cache := makeCache(t)
	cache.Set("12345", RatingR16)
	plex := &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

	var stats Stats
	captureStdout(t, func() {
		stats = walkAndProcess(dir, &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil, time.Time{})
	})
	if stats.Errors != 1 || stats.Updated != 0 {
		t.Errorf("stats = %+v; want 1 error, 0 updated", stats)
	}
}
