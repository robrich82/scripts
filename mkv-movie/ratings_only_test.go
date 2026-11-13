package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingTMDB counts TMDB calls, so tests can check that a cached movie
// costs none.
type countingTMDB struct {
	fakeTMDBMovieClient
	searches, fetches int
}

func (c *countingTMDB) SearchAndFetch(title, year string) (string, MovieDetails, error) {
	c.searches++
	return c.fakeTMDBMovieClient.SearchAndFetch(title, year)
}

func (c *countingTMDB) GetMovieDetails(id string) (MovieDetails, error) {
	c.fetches++
	return c.fakeTMDBMovieClient.GetMovieDetails(id)
}

// existingTags is a tagged file's mkvextract output: a full metadata run's
// tags with an R13 rating, plus a track tag.
const existingTags = `<Tags>` +
	`<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>` +
	`<Simple><Name>SUMMARY</Name><String>Heroes assemble.</String></Simple>` +
	`<Simple><Name>COUNTRY</Name><String>NZ</String><Simple><Name>LAW_RATING</Name><String>R13</String></Simple></Simple>` +
	`<Simple><Name>TMDB_ID</Name><String>12345</String></Simple>` +
	`</Tag>` +
	`<Tag><Targets><TrackUID>111</TrackUID></Targets><Simple><Name>BPS</Name><String>1</String></Simple></Tag>` +
	`</Tags>`

var oldMtime = time.Date(2019, 4, 26, 0, 0, 0, 0, time.Local)

// ratingsOnlyLibrary is a folder with one tagged movie file, last modified
// long before today, whose TMDB ID and R16 rating are cached.
func ratingsOnlyLibrary(t *testing.T) (dir, path string, cache *MovieRatingCache) {
	t.Helper()
	path = makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	if err := os.Chtimes(path, oldMtime, oldMtime); err != nil {
		t.Fatal(err)
	}
	cache = makeCache(t)
	cache.SetTMDBID("Avengers Endgame:2019", "12345")
	cache.Set("12345", RatingR16)
	return filepath.Dir(path), path, cache
}

func ratingsOnlyRun(t *testing.T, dir string, tmdb TMDBMovieClient, tagger MKVTagger, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache) (Stats, string) {
	t.Helper()
	var stats Stats
	out := captureStdout(t, func() {
		stats = walkRatingsOnly(dir, tmdb, tagger, timer, rr, cache)
	})
	return stats, out
}

// A cached movie: only the rating changes in the tags, the file keeps its
// timestamps, Plex gets the rating (TMDB details are fetched once, for Plex's
// title lookup), and a second run calls nothing.
func TestRatingsOnly_CachedMovie_WritesRatingOnce(t *testing.T) {
	dir, path, cache := ratingsOnlyLibrary(t)
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: testDetails()}}
	tagger := &fakeMKVTagger{available: true, extractedXML: existingTags}
	timer := &fakeFileTimer{}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

	stats, out := ratingsOnlyRun(t, dir, tmdb, tagger, timer, rr, cache)

	if stats.Updated != 1 || stats.Errors != 0 || stats.SkippedOld != 0 {
		t.Errorf("stats %+v; want 1 updated, the old file included", stats)
	}
	if !strings.Contains(out, "[UPDATED] Avengers.Endgame.2019.mkv (rating: R16) (Plex rating: R16)") {
		t.Errorf("result line:\n%s", out)
	}
	if tmdb.searches != 0 || tmdb.fetches != 1 {
		t.Errorf("TMDB searches %d, details %d; want 0 and 1 (for the Plex push)", tmdb.searches, tmdb.fetches)
	}
	w := tagger.writtenXML
	if !strings.Contains(w, "<String>R16</String>") || strings.Contains(w, "R13") {
		t.Errorf("rating not replaced:\n%s", w)
	}
	for _, keep := range []string{"Heroes assemble.", "<Name>TMDB_ID</Name>", "<TrackUID>111</TrackUID>"} {
		if !strings.Contains(w, keep) {
			t.Errorf("lost %q:\n%s", keep, w)
		}
	}
	if len(timer.setTimesCalls) != 1 || !timer.setTimesCalls[0].Equal(oldMtime) ||
		len(timer.setCreationTimeCalls) != 1 || !timer.setCreationTimeCalls[0].Equal(oldMtime) {
		t.Errorf("timestamps: mtime %v, ctime %v; want both restored to %v", timer.setTimesCalls, timer.setCreationTimeCalls, oldMtime)
	}
	if plex.ratingSets != 1 {
		t.Errorf("SetContentRating calls = %d, want 1", plex.ratingSets)
	}

	// The cache was saved with the file's rating applied.
	saved, err := LoadMovieRatingCache(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.IsApplied(path, RatingR16) {
		t.Error("applied rating not saved")
	}

	tagger.appliedPaths = nil
	stats, out = ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, newMovieRatingResolver(nil, saved, false, false, plex, answerPrompter()), saved)
	if stats.Skipped != 1 || len(tagger.appliedPaths) != 0 || plex.ratingSets != 1 || tmdb.fetches != 1 {
		t.Errorf("second run: stats %+v, tag writes %d, Plex sets %d, TMDB details %d; want nothing done", stats, len(tagger.appliedPaths), plex.ratingSets, tmdb.fetches)
	}
	if !strings.Contains(out, "[SKIP]    Avengers.Endgame.2019.mkv (rating: R16, written earlier)") {
		t.Errorf("second run result line:\n%s", out)
	}
}

// After a full metadata run, --ratings-only finds the rating already written.
func TestRatingsOnly_AfterFullRun_SkipsTags(t *testing.T) {
	dir, _, cache := ratingsOnlyLibrary(t)
	tmdb := &fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}
	rr := newMovieRatingResolver(nil, cache, false, false, nil, answerPrompter())
	captureStdout(t, func() {
		walkAndProcess(dir, tmdb, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil, time.Time{})
	})

	tagger := &fakeMKVTagger{available: true, extractedXML: existingTags}
	stats, _ := ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Skipped != 1 || len(tagger.appliedPaths) != 0 {
		t.Errorf("stats %+v, tag writes %d; want the full run's tags left alone", stats, len(tagger.appliedPaths))
	}
}

// A rating changed since the tags were written (e.g. by --set-rating) is
// written again, and pushed.
func TestRatingsOnly_ChangedRating_Rewritten(t *testing.T) {
	dir, path, cache := ratingsOnlyLibrary(t)
	cache.MarkApplied(path, RatingR13)
	cache.SetPlexRating("12345", RatingR13)
	tagger := &fakeMKVTagger{available: true, extractedXML: existingTags}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

	stats, _ := ratingsOnlyRun(t, dir, &countingTMDB{}, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Updated != 1 || len(tagger.appliedPaths) != 1 || plex.ratingSets != 1 {
		t.Errorf("stats %+v, tag writes %d, Plex sets %d; want the new rating written and pushed", stats, len(tagger.appliedPaths), plex.ratingSets)
	}
	if !cache.IsApplied(path, RatingR16) {
		t.Error("applied rating not updated")
	}
}

// --overwrite-ratings re-fetches the rating, with TMDB details for the
// sources, and rewrites tags that already hold it.
func TestRatingsOnly_OverwriteRatings_RewritesApplied(t *testing.T) {
	dir, path, cache := ratingsOnlyLibrary(t)
	cache.MarkApplied(path, RatingR16)
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: testDetails()}}
	tagger := &fakeMKVTagger{available: true, extractedXML: existingTags}
	src := &fakeRatingSource{name: "fake", rating: RatingR16}
	rr := newMovieRatingResolver([]RatingSource{src}, cache, true, false, nil, answerPrompter())

	stats, _ := ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Updated != 1 || len(tagger.appliedPaths) != 1 {
		t.Errorf("stats %+v, tag writes %d; want the applied rating rewritten", stats, len(tagger.appliedPaths))
	}
	if tmdb.fetches != 1 {
		t.Errorf("TMDB details fetched %d times, want 1", tmdb.fetches)
	}
}

// A movie with no cached TMDB ID is searched for, and its TMDB title is what
// the rating prompt shows.
func TestRatingsOnly_UncachedMovie_SearchesTMDB(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	cache := makeCache(t)
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}}
	tagger := &fakeMKVTagger{available: true}
	var prompts []string
	prompter := func(p string) (string, error) { prompts = append(prompts, p); return "M", nil }
	rr := newMovieRatingResolver(nil, cache, false, false, nil, prompter)

	stats, _ := ratingsOnlyRun(t, filepath.Dir(path), tmdb, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Updated != 1 || tmdb.searches != 1 {
		t.Errorf("stats %+v, searches %d", stats, tmdb.searches)
	}
	if cache.GetTMDBID("Avengers Endgame:2019") != "12345" || !cache.IsApplied(path, RatingM) {
		t.Error("TMDB ID or applied rating not recorded")
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], `"Avengers Endgame"`) {
		t.Errorf("prompts %q", prompts)
	}
	if !strings.Contains(tagger.writtenXML, "<String>M</String>") {
		t.Errorf("untagged file not given the rating:\n%s", tagger.writtenXML)
	}
}

// With no rating, nothing is written or pushed.
func TestRatingsOnly_NoRating_Skipped(t *testing.T) {
	dir, _, cache := ratingsOnlyLibrary(t)
	cache.Set("12345", RatingNone)
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: testDetails()}}
	tagger := &fakeMKVTagger{available: true}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter(""))

	stats, out := ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Skipped != 1 || len(tagger.appliedPaths) != 0 || plex.ratingSets != 0 {
		t.Errorf("stats %+v, tag writes %d, Plex sets %d", stats, len(tagger.appliedPaths), plex.ratingSets)
	}
	if !strings.Contains(out, "(no NZ rating)") {
		t.Errorf("result line:\n%s", out)
	}
}

func TestRatingsOnly_Failures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tagger      *fakeMKVTagger
		noTagger    bool
		timer       *fakeFileTimer
		plex        *fakePlexMovieClient
		wantLine    string
		wantResult  string // "updated", "skipped" or "error"
		wantApplied bool
		wantPushed  bool
		wantWarning string
	}{
		{
			name:     "tag write fails",
			tagger:   &fakeMKVTagger{available: true, applyErr: errors.New("rc 2")},
			plex:     &fakePlexMovieClient{tmdbRatingKey: "10"},
			wantLine: "[ERR]     Avengers.Endgame.2019.mkv: tags not written (Plex rating: R16)", wantResult: "error",
			wantPushed: true,
		},
		{
			name:     "unreadable tags",
			tagger:   &fakeMKVTagger{available: true, extractedXML: "<Tags><Tag>"},
			plex:     &fakePlexMovieClient{tmdbRatingKey: "10"},
			wantLine: "[ERR]     Avengers.Endgame.2019.mkv: tags not written (Plex rating: R16)", wantResult: "error",
			wantPushed: true,
		},
		{
			name:     "no MKVToolNix",
			noTagger: true,
			plex:     &fakePlexMovieClient{tmdbRatingKey: "10"},
			wantLine: "[UPDATED] Avengers.Endgame.2019.mkv (mkvpropedit/mkvextract not found; file not changed) (Plex rating: R16)", wantResult: "updated",
			wantPushed: true,
		},
		{
			name:     "timestamp restore fails",
			tagger:   &fakeMKVTagger{available: true},
			timer:    &fakeFileTimer{setTimesErr: errors.New("access denied")},
			wantLine: "[ERR]     Avengers.Endgame.2019.mkv (rating: R16) (failed: mtime)", wantResult: "error",
			wantApplied: true,
		},
		{
			name:     "Plex lookup fails",
			tagger:   &fakeMKVTagger{available: true},
			plex:     &fakePlexMovieClient{findTMDBErr: errors.New("plex: 500")},
			wantLine: "[ERR]     Avengers.Endgame.2019.mkv (rating: R16) (failed: Plex lookup)", wantResult: "error",
			wantApplied: true,
		},
		{
			name:     "Plex push fails",
			tagger:   &fakeMKVTagger{available: true},
			plex:     &fakePlexMovieClient{tmdbRatingKey: "10", setRatingErr: errors.New("plex: 403")},
			wantLine: "[ERR]     Avengers.Endgame.2019.mkv (rating: R16) (failed: Plex rating)", wantResult: "error",
			wantApplied: true,
		},
		{
			name:     "not in Plex",
			tagger:   &fakeMKVTagger{available: true},
			plex:     &fakePlexMovieClient{},
			wantLine: "[UPDATED] Avengers.Endgame.2019.mkv (rating: R16)\n", wantResult: "updated",
			wantApplied: true,
			wantWarning: "rerun with --ratings-only once Plex has it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, path, cache := ratingsOnlyLibrary(t)
			var tagger MKVTagger
			if !tc.noTagger {
				tagger = tc.tagger
			}
			timer := tc.timer
			if timer == nil {
				timer = &fakeFileTimer{}
			}
			var plex PlexMovieClient
			if tc.plex != nil {
				plex = tc.plex
			}
			rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

			stats, out := ratingsOnlyRun(t, dir, &countingTMDB{}, tagger, timer, rr, cache)

			got := map[string]int{"updated": stats.Updated, "skipped": stats.Skipped, "error": stats.Errors}
			if got[tc.wantResult] != 1 {
				t.Errorf("stats %+v; want 1 %s", stats, tc.wantResult)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("want %q in:\n%s", tc.wantLine, out)
			}
			if got := cache.IsApplied(path, RatingR16); got != tc.wantApplied {
				t.Errorf("applied recorded = %v, want %v", got, tc.wantApplied)
			}
			if _, got := cache.GetPlexRating("12345"); got != tc.wantPushed {
				t.Errorf("Plex rating recorded = %v, want %v", got, tc.wantPushed)
			}
			if tc.wantWarning != "" && !strings.Contains(out, tc.wantWarning) {
				t.Errorf("no %q warning:\n%s", tc.wantWarning, out)
			}
			if !tc.wantApplied && len(timer.setTimesCalls)+len(timer.setCreationTimeCalls) != 0 {
				t.Errorf("timestamps touched on an untagged file: %v %v", timer.setTimesCalls, timer.setCreationTimeCalls)
			}
		})
	}
}

// Plex's title lookup uses TMDB's title and year, even when the rating is
// cached; the filename's year stands in only when TMDB has no release date.
func TestRatingsOnly_PlexTitleLookup_UsesTMDBTitle(t *testing.T) {
	for _, tc := range []struct {
		name        string
		releaseDate string
		wantYear    string
	}{
		{"TMDB release year", "2018-04-26", "2018"},
		{"no TMDB release date", "", "2019"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, cache := ratingsOnlyLibrary(t)
			d := testDetails()
			d.Title = "Avengers: Endgame"
			d.ReleaseDate = tc.releaseDate
			plex := &fakePlexMovieClient{titleRatingKey: "77"}
			rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

			stats, _ := ratingsOnlyRun(t, dir, &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: d}}, &fakeMKVTagger{available: true}, &fakeFileTimer{}, rr, cache)
			if stats.Updated != 1 || plex.ratingSets != 1 {
				t.Errorf("stats %+v, Plex sets %d", stats, plex.ratingSets)
			}
			if len(plex.titleLookups) != 1 || plex.titleLookups[0] != [2]string{"Avengers: Endgame", tc.wantYear} {
				t.Errorf("title lookups %v", plex.titleLookups)
			}
		})
	}
}

// With no year from TMDB or the filename, the title lookup is skipped, so a
// remake can't take the rating.
func TestRatingsOnly_NoYear_NoTitleLookup(t *testing.T) {
	path := makeTempMKVNamed(t, "Dune.mkv")
	cache := makeCache(t)
	cache.SetTMDBID("Dune:", "438631")
	cache.Set("438631", RatingM)
	plex := &fakePlexMovieClient{titleRatingKey: "77"}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: MovieDetails{Title: "Dune"}}}

	stats, out := ratingsOnlyRun(t, filepath.Dir(path), tmdb, &fakeMKVTagger{available: true}, &fakeFileTimer{}, rr, cache)
	if len(plex.titleLookups) != 0 || plex.ratingSets != 0 {
		t.Errorf("title lookups %v, Plex sets %d; want none", plex.titleLookups, plex.ratingSets)
	}
	if _, ok := cache.GetPlexRating("438631"); ok || stats.Updated != 1 || !strings.Contains(out, `"Dune" not found by TMDB GUID, and with no release year it isn't searched by title – rating not pushed; rerun with --ratings-only once Plex has matched it`) {
		t.Errorf("Plex rating recorded %v, stats %+v; want tags written and a not-found warning:\n%s", ok, stats, out)
	}
}

// Tags written by an earlier run aren't written again, but a Plex push that
// failed then is retried.
func TestRatingsOnly_TagsWritten_RetriesPlex(t *testing.T) {
	dir, path, cache := ratingsOnlyLibrary(t)
	cache.MarkApplied(path, RatingR16)
	tagger := &fakeMKVTagger{available: true}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())

	stats, out := ratingsOnlyRun(t, dir, &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{details: testDetails()}}, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Updated != 1 || len(tagger.appliedPaths) != 0 || plex.ratingSets != 1 {
		t.Errorf("stats %+v, tag writes %d, Plex sets %d", stats, len(tagger.appliedPaths), plex.ratingSets)
	}
	if !strings.Contains(out, "[UPDATED] Avengers.Endgame.2019.mkv (rating: R16, written earlier) (Plex rating: R16)") {
		t.Errorf("result line:\n%s", out)
	}
}

// Two files of one uncached movie: one TMDB search (the ID is cached by
// title:YEAR after the first), one prompt, one Plex push, both tagged.
func TestRatingsOnly_TwoFilesOneMovie(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "Avengers.Endgame.2019.mkv"), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cache := makeCache(t)
	tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{tmdbID: "12345", details: testDetails()}}
	tagger := &fakeMKVTagger{available: true}
	plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
	prompts := 0
	prompter := func(string) (string, error) { prompts++; return "R16", nil }
	rr := newMovieRatingResolver(nil, cache, false, false, plex, prompter)

	stats, _ := ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, rr, cache)
	if stats.Updated != 2 || len(tagger.appliedPaths) != 2 || prompts != 1 || plex.ratingSets != 1 {
		t.Errorf("stats %+v, tag writes %d, prompts %d, Plex sets %d", stats, len(tagger.appliedPaths), prompts, plex.ratingSets)
	}
	if tmdb.searches != 1 || tmdb.fetches != 0 {
		t.Errorf("TMDB searches %d, details %d; want 1 and 0", tmdb.searches, tmdb.fetches)
	}
}

// A TMDB failure is an error: before the rating is known nothing is written;
// when only the Plex push needs details, the tags are still written.
func TestRatingsOnly_TMDBDetailsFail(t *testing.T) {
	t.Run("rating not cached", func(t *testing.T) {
		dir, path, cache := ratingsOnlyLibrary(t)
		cache.Set("12345", RatingNone)
		tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{err: errors.New("tmdb: 503")}}
		tagger := &fakeMKVTagger{available: true}
		rr := newMovieRatingResolver(nil, cache, false, false, nil, answerPrompter("R16"))
		stats, out := ratingsOnlyRun(t, dir, tmdb, tagger, &fakeFileTimer{}, rr, cache)
		if stats.Errors != 1 || len(tagger.appliedPaths) != 0 || !strings.Contains(out, "TMDB details: tmdb: 503") {
			t.Errorf("stats %+v, tag writes %d:\n%s", stats, len(tagger.appliedPaths), out)
		}
		if cache.data.Files[path] != "12345" {
			t.Error("file mapping lost on a TMDB error")
		}
	})
	t.Run("Plex push", func(t *testing.T) {
		dir, path, cache := ratingsOnlyLibrary(t)
		tmdb := &countingTMDB{fakeTMDBMovieClient: fakeTMDBMovieClient{err: errors.New("tmdb: 503")}}
		plex := &fakePlexMovieClient{tmdbRatingKey: "10"}
		rr := newMovieRatingResolver(nil, cache, false, false, plex, answerPrompter())
		stats, out := ratingsOnlyRun(t, dir, tmdb, &fakeMKVTagger{available: true}, &fakeFileTimer{}, rr, cache)
		if stats.Errors != 1 || plex.ratingSets != 0 || !cache.IsApplied(path, RatingR16) {
			t.Errorf("stats %+v, Plex sets %d, applied %v", stats, plex.ratingSets, cache.IsApplied(path, RatingR16))
		}
		if !strings.Contains(out, "[ERR]     Avengers.Endgame.2019.mkv (rating: R16) (failed: TMDB details)") {
			t.Errorf("result line:\n%s", out)
		}
	})
}

// A full run records the rating it writes, and only when it writes it.
func TestProcessFile_RecordsAppliedRating(t *testing.T) {
	for _, tc := range []struct {
		name    string
		applier MKVApplier
		rating  NZRating // cached rating
		want    NZRating // applied afterwards; RatingNone means no entry
	}{
		{"written", &fakeApplier{}, RatingR16, RatingR16},
		{"no rating clears it", &fakeApplier{}, RatingNone, RatingNone},
		{"write fails", &fakeApplier{err: errors.New("rc 2")}, RatingR16, RatingR13},
		{"no MKVToolNix", nil, RatingR16, RatingR13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, path, cache := ratingsOnlyLibrary(t)
			cache.Set("12345", tc.rating)
			cache.MarkApplied(path, RatingR13)
			rr := newMovieRatingResolver(nil, cache, false, false, nil, answerPrompter())
			captureStdout(t, func() {
				walkAndProcess(dir, &fakeTMDBMovieClient{details: testDetails()}, tc.applier, &fakeFileTimer{}, rr, cache, nil, time.Time{})
			})
			if got := NZRating(cache.data.Applied[path]); got != tc.want {
				t.Errorf("applied %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUpdateRatingInXML(t *testing.T) {
	got, err := updateRatingInXML(existingTags, RatingR16)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "LAW_RATING") != 1 || !strings.Contains(got, "<String>R16</String>") {
		t.Errorf("rating not replaced:\n%s", got)
	}
	got, err = updateRatingInXML(existingTags, RatingNone)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "LAW_RATING") || !strings.Contains(got, "<Name>SUMMARY</Name>") {
		t.Errorf("rating not removed, or other tags lost:\n%s", got)
	}
}

func TestMarkApplied_NoneClears(t *testing.T) {
	cache := makeCache(t)
	cache.MarkApplied("/a.mkv", RatingR16)
	cache.MarkApplied("/a.mkv", RatingNone)
	if cache.IsApplied("/a.mkv", RatingR16) || cache.IsApplied("/a.mkv", RatingNone) {
		t.Error("RatingNone should clear the applied entry")
	}
}
