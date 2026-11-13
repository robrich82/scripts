package main

import (
	"errors"
	"fmt"
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
		timer      *fakeFileTimer
		wantFailed string
	}{
		{"Plex lookup", &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500")}, &fakeFileTimer{}, "(failed: Plex lookup)"},
		{"Plex rating", &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}, &fakeFileTimer{}, "(failed: Plex rating)"},
		{"Plex streams", &fakePlexMovieClient{tmdbRatingKey: "10", setStreamsErr: errors.New("plex: 403")}, &fakeFileTimer{}, "(failed: Plex streams)"},
		{"both pushes", &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("x"), setStreamsErr: errors.New("y")}, &fakeFileTimer{}, "(failed: Plex rating, Plex streams)"},
		{"mtime", &fakePlexMovieClient{tmdbRatingKey: "10"}, &fakeFileTimer{setTimesErr: errors.New("access denied")}, "(failed: mtime)"},
		{"ctime", &fakePlexMovieClient{tmdbRatingKey: "10"}, &fakeFileTimer{setCreationTimeErr: errors.New("access denied")}, "(failed: ctime)"},
		{"mtime and ctime", &fakePlexMovieClient{tmdbRatingKey: "10"}, &fakeFileTimer{setTimesErr: errors.New("x"), setCreationTimeErr: errors.New("y")}, "(failed: mtime, ctime)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timer := tc.timer
			result, out, cache := outcomeRun(t, &fakeApplier{}, timer, tc.plex, detailsWithLanguage())

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
			// A failed push isn't recorded, so a later run retries it.
			pushFailed := func(step string) bool {
				return strings.Contains(tc.wantFailed, step) || strings.Contains(tc.wantFailed, "Plex lookup")
			}
			if _, ok := cache.GetPlexRating("12345"); ok == pushFailed("Plex rating") {
				t.Errorf("Plex rating recorded = %v; want %v", ok, !pushFailed("Plex rating"))
			}
			if _, ok := cache.GetStreamsApplied("12345"); ok == pushFailed("Plex streams") {
				t.Errorf("streams recorded = %v; want %v", ok, !pushFailed("Plex streams"))
			}
		})
	}
}

// A primary-account push that fails because the file has no audio track in the
// language says which manual_data.json key picks the language.
func TestProcessFile_NoAudioStream_NamesManualDataKey(t *testing.T) {
	plex := &fakePlexMovieClient{tmdbRatingKey: "10", setStreamsErr: fmt.Errorf("%w: en, key 10", ErrPlexNoAudioStream)}
	result, out, _ := outcomeRun(t, &fakeApplier{}, &fakeFileTimer{}, plex, detailsWithLanguage())
	if result != ResultError || !strings.Contains(out, "(failed: Plex streams)") {
		t.Errorf("got %v; want ResultError naming the streams:\n%s", result, out)
	}
	if !strings.Contains(out, `Set "language" for "Avengers.Endgame.2019" in manual_data.json`) {
		t.Errorf("no manual_data.json hint:\n%s", out)
	}

	plex = &fakePlexMovieClient{tmdbRatingKey: "10", setStreamsErr: errors.New("plex: 403")}
	_, out, _ = outcomeRun(t, &fakeApplier{}, &fakeFileTimer{}, plex, detailsWithLanguage())
	if strings.Contains(out, "manual_data.json") {
		t.Errorf("hint shown for an unrelated stream error:\n%s", out)
	}
}

// A failed tag write still pushes to Plex; the line reports both, and the
// file isn't backdated.
func TestProcessFile_ApplierError_StillPushesPlex(t *testing.T) {
	t.Run("push succeeds", func(t *testing.T) {
		timer := &fakeFileTimer{}
		plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
		result, out, _ := outcomeRun(t, &fakeApplier{err: errors.New("mkvpropedit: rc 2")}, timer, plex, testDetails())
		if result != ResultError || !strings.Contains(out, "[ERR]     Avengers.Endgame.2019.mkv: tags not written (Plex rating: R16)") {
			t.Errorf("got %v; want a tags-not-written line naming the Plex rating:\n%s", result, out)
		}
		if plex.ratingSets != 1 {
			t.Errorf("SetContentRating calls = %d, want 1", plex.ratingSets)
		}
		if len(timer.setTimesCalls) != 0 || len(timer.setCreationTimeCalls) != 0 {
			t.Errorf("backdated an untagged file: mtime %v, ctime %v", timer.setTimesCalls, timer.setCreationTimeCalls)
		}
	})
	t.Run("push fails", func(t *testing.T) {
		plex := &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}
		result, out, _ := outcomeRun(t, &fakeApplier{err: errors.New("mkvpropedit: rc 2")}, &fakeFileTimer{}, plex, testDetails())
		if result != ResultError || !strings.Contains(out, "tags not written (failed: Plex rating)") {
			t.Errorf("got %v; want a tags-not-written line naming the failed push:\n%s", result, out)
		}
	})
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
	timer := &fakeFileTimer{}
	result, out, cache := outcomeRun(t, &fakeApplier{}, timer, plex, detailsWithLanguage())

	if result != ResultUpdated || !strings.Contains(out, "Not in Plex library") || !strings.Contains(out, "--all") {
		t.Errorf("got %v; want ResultUpdated with the not-in-Plex notice naming --all:\n%s", result, out)
	}
	if len(timer.setTimesCalls) != 1 || len(timer.setCreationTimeCalls) != 1 {
		t.Errorf("mtime %v, ctime %v; want the tagged file backdated", timer.setTimesCalls, timer.setCreationTimeCalls)
	}
	if _, ok := cache.GetPlexRating("12345"); ok {
		t.Error("Plex rating recorded for a movie not in Plex")
	}
	if _, ok := cache.GetStreamsApplied("12345"); ok {
		t.Error("streams recorded for a movie not in Plex")
	}
}

// Once Plex has the movie, the next run that processes the file pushes to it.
func TestProcessFile_NotInPlex_PushedOnLaterRun(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	cache := makeCache(t)
	cache.Set("12345", RatingR16)
	run := func(plex *fakePlexMovieClient) FileResult {
		rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())
		var result FileResult
		captureStdout(t, func() {
			result = processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: detailsWithLanguage()}, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil)
		})
		return result
	}

	if r := run(&fakePlexMovieClient{findTitleErr: ErrPlexMovieNotFound}); r != ResultUpdated {
		t.Fatalf("first run: got %v, want ResultUpdated", r)
	}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	if r := run(plex); r != ResultUpdated {
		t.Fatalf("second run: got %v, want ResultUpdated", r)
	}
	if plex.ratingSets != 1 || plex.streamSets != 1 {
		t.Errorf("second run pushed rating %d, streams %d times; want 1 each", plex.ratingSets, plex.streamSets)
	}
	if r, ok := cache.GetPlexRating("12345"); !ok || r != RatingR16 {
		t.Errorf("Plex rating recorded = %q, %v; want R16", r, ok)
	}
	if _, ok := cache.GetStreamsApplied("12345"); !ok {
		t.Error("streams not recorded after the push")
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
	t.Run("not in Plex", func(t *testing.T) {
		timer := &fakeFileTimer{}
		result, out, cache := outcomeRun(t, nil, timer, &fakePlexMovieClient{findTitleErr: ErrPlexMovieNotFound}, detailsWithLanguage())
		if result != ResultSkipped || !strings.Contains(out, "[SKIP]") {
			t.Errorf("got %v; want ResultSkipped:\n%s", result, out)
		}
		if _, ok := cache.GetPlexRating("12345"); ok {
			t.Error("Plex rating recorded for a movie not in Plex")
		}
		if _, ok := cache.GetStreamsApplied("12345"); ok {
			t.Error("streams recorded for a movie not in Plex")
		}
		if len(timer.setTimesCalls) != 0 {
			t.Errorf("backdated without MKVToolNix: %v", timer.setTimesCalls)
		}
	})
	t.Run("push failed", func(t *testing.T) {
		result, out, _ := outcomeRun(t, nil, &fakeFileTimer{}, &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}, testDetails())
		if result != ResultError || !strings.Contains(out, "(failed: Plex rating)") {
			t.Errorf("got %v; want ResultError naming the Plex rating:\n%s", result, out)
		}
	})
}

// The summary counts each outcome where processFile reports it.
func TestWalkAndProcess_OutcomeCounts(t *testing.T) {
	failingPlex := func() *fakePlexMovieClient {
		return &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")}
	}
	for _, tc := range []struct {
		name                     string
		applier                  MKVApplier
		plex                     *fakePlexMovieClient
		errors, updated, skipped int
	}{
		{"failed Plex push", &fakeApplier{}, failingPlex(), 1, 0, 0},
		{"failed tag write", &fakeApplier{err: errors.New("rc 2")}, nil, 1, 0, 0},
		{"no MKVToolNix", nil, nil, 0, 0, 1},
		{"no MKVToolNix, failed Plex push", nil, failingPlex(), 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMovieFile(t, dir, "Avengers Endgame (2019).mkv")
			cache := makeCache(t)
			cache.Set("12345", RatingR16)
			var rr *MovieRatingResolver
			if tc.plex != nil {
				rr = newMovieRatingResolver(nil, cache, false, false, tc.plex, answerPrompter())
			}

			var stats Stats
			captureStdout(t, func() {
				stats = walkAndProcess(dir, &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}, tc.applier, &fakeFileTimer{}, rr, cache, nil, time.Time{})
			})
			if stats.Errors != tc.errors || stats.Updated != tc.updated || stats.Skipped != tc.skipped {
				t.Errorf("stats = %+v; want %d errors, %d updated, %d skipped", stats, tc.errors, tc.updated, tc.skipped)
			}
		})
	}
}
