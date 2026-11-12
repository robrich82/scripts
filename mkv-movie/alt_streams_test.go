package main

import (
	"errors"
	"strings"
	"testing"
)

// altRun processes testDetails' movie once, with original language "en", a
// primary account whose streams are already right, and one alt account.
func altRun(t *testing.T, cache *MovieRatingCache, alt *fakeMultiPlexMovieClient, applier MKVApplier) (FileResult, string) {
	t.Helper()
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	primary := &fakePlexMovieClient{tmdbRatingKey: "10"}
	plex := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: alt})
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())
	var result FileResult
	out := captureStdout(t, func() {
		result = processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: detailsWithLanguage()}, applier, &fakeFileTimer{}, rr, cache, nil)
	})
	return result, out
}

// #141: an alt account's failure shows even when no account's streams changed.
// It's a warning, but isn't recorded, so a later run retries the alt.
func TestProcessFile_AltFailure_ShownAndNotRecorded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		applier  MKVApplier
		wantLine string
	}{
		{"tagged", &fakeApplier{}, "[UPDATED] Avengers.Endgame.2019.mkv -> 2019-04-26 (lang: unchanged; alt: alt failed)"},
		{"no MKVToolNix", nil, "(Plex lang: unchanged; alt: alt failed)"},
		{"tag write failed", &fakeApplier{err: errors.New("rc 2")}, "tags not written (Plex lang: unchanged; alt: alt failed)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := makeCache(t)
			alt := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}
			_, out := altRun(t, cache, alt, tc.applier)
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("result line doesn't show the alt failure %q:\n%s", tc.wantLine, out)
			}
			if _, ok := cache.GetStreamsApplied("12345"); ok {
				t.Error("streams recorded as applied though an alt account failed")
			}
		})
	}

	t.Run("not an error", func(t *testing.T) {
		result, _ := altRun(t, makeCache(t), &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}, &fakeApplier{})
		if result != ResultUpdated {
			t.Errorf("got %v, want ResultUpdated", result)
		}
	})

	t.Run("retried and recorded once the alt succeeds", func(t *testing.T) {
		cache := makeCache(t)
		altRun(t, cache, &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}, &fakeApplier{})
		alt := &fakeMultiPlexMovieClient{}
		_, out := altRun(t, cache, alt, &fakeApplier{})
		if alt.setStreamsCalls != 1 {
			t.Errorf("alt SetMovieStreams calls = %d, want 1", alt.setStreamsCalls)
		}
		if got, ok := cache.GetStreamsApplied("12345"); !ok || got != "en|alt=en" {
			t.Errorf("streams recorded = %q, %v; want en|alt=en", got, ok)
		}
		if strings.Contains(out, "alt:") {
			t.Errorf("alt failure still shown after it succeeded:\n%s", out)
		}
	})
}

// #141: a change on an alt account alone shows on the result line.
func TestProcessFile_AltChangeOnly_Shown(t *testing.T) {
	cache := makeCache(t)
	result, out := altRun(t, cache, &fakeMultiPlexMovieClient{}, &fakeApplier{})
	if result != ResultUpdated || !strings.Contains(out, "(lang: en|alt=en)") {
		t.Errorf("got %v; want the alt change on the result line:\n%s", result, out)
	}
	if _, ok := cache.GetStreamsApplied("12345"); !ok {
		t.Error("streams not recorded")
	}
}

func TestLangNote(t *testing.T) {
	for _, tc := range []struct{ langSet, suffix, want string }{
		{"", "", ""},
		{"ja", "", "ja"},
		{"ja|alt=en", "; alt: 401", "ja|alt=en; alt: 401"},
		{"", "; alt: 401", "unchanged; alt: 401"},
	} {
		if got := langNote(tc.langSet, tc.suffix); got != tc.want {
			t.Errorf("langNote(%q, %q) = %q; want %q", tc.langSet, tc.suffix, got, tc.want)
		}
	}
}
