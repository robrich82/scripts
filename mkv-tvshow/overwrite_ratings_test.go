package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runOverwrite runs a full pass over two episodes of one show with a cached
// R16 rating, the given sources and prompt answers, under --overwrite-ratings.
func runOverwrite(t *testing.T, sources []RatingSource, prompter *scriptedPrompter) *RatingCache {
	t.Helper()
	dir := t.TempDir()
	showDir := filepath.Join(dir, "Rated Show")
	if err := os.MkdirAll(showDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Rated Show - S01E01.mkv", "Rated Show - S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(showDir, name), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}
	}
	rc := emptyRatingCache(t)
	rc.SetShowRating("999", RatingR16)
	tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{
		{EpisodeNumber: 1, AirDate: "2022-01-01"}, {EpisodeNumber: 2, AirDate: "2022-01-08"},
	}}}
	runner := &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}
	p := NewProcessor(Config{MediaDir: dir, ProcessAll: true, OverwriteRatings: true},
		cacheWith(t, map[string]string{"Rated Show": "999"}), map[string]ManualEntry{}, tmdb, runner, prompter,
		&captureFileTimer{}, emptyRetryList(t), rc, sources, nil, nil)
	captureStdout(t, func() {
		if stats := p.Run(); stats.Success != 2 {
			t.Errorf("stats = %+v; want 2 success", stats)
		}
	})
	return rc
}

func TestRun_OverwriteRatings_RefetchesOncePerShow(t *testing.T) {
	prompter := &scriptedPrompter{answers: []string{""}} // confirm the source's rating
	rc := runOverwrite(t, []RatingSource{&fakeRatingSource{showRating: RatingM}}, prompter)

	if prompter.idx != 1 {
		t.Errorf("prompted %d times; want once per show", prompter.idx)
	}
	if got, _ := rc.GetShowRating("999"); got != RatingM {
		t.Errorf("cached rating = %q; want M re-fetched over the cached R16", got)
	}
}

func TestRun_OverwriteRatings_NoSourceData_KeepsCachedRating(t *testing.T) {
	prompter := &scriptedPrompter{}
	rc := runOverwrite(t, []RatingSource{&fakeRatingSource{}}, prompter)

	if prompter.idx != 0 {
		t.Errorf("prompted %d times; with no source data the cached rating is kept without a prompt", prompter.idx)
	}
	if got, _ := rc.GetShowRating("999"); got != RatingR16 {
		t.Errorf("cached rating = %q; want R16 kept", got)
	}
}

// A Plex lookup that fails (not "not found") is reported and gives no prompt
// default, rather than falling through to a title match that may be another show.
func TestResolveEpisodeRating_PlexLookupError_NoFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		plex     *fakePlexClient
		want     NZRating
		wantWarn string
	}{
		{
			name:     "GUID lookup fails",
			plex:     &fakePlexClient{findErr: errors.New("plex: 401 Unauthorized"), titleRatingKey: "77", contentRating: RatingM},
			want:     RatingNone,
			wantWarn: `[!] Plex: TMDB GUID lookup failed for "Test Show": plex: 401 Unauthorized`,
		},
		{
			name:     "GUID not found falls back to title",
			plex:     &fakePlexClient{findErr: ErrPlexNotFound, titleRatingKey: "77", contentRating: RatingM},
			want:     RatingM,
			wantWarn: "",
		},
		{
			name:     "rating fetch fails",
			plex:     &fakePlexClient{showRatingKey: "42", contentRatingErr: errors.New("plex: 500")},
			want:     RatingNone,
			wantWarn: `[!] Plex: rating fetch failed for "Test Show": plex: 500`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Processor{
				ratingCache:           emptyRatingCache(t),
				prompter:              &scriptedPrompter{answers: []string{""}},
				plex:                  tc.plex,
				sessionPromptedRating: map[string]NZRating{},
			}
			var got NZRating
			out := captureStdout(t, func() { got = p.resolveEpisodeRating("12345", "Test Show", "", 1, 1) })
			if got != tc.want {
				t.Errorf("rating = %q; want %q", got, tc.want)
			}
			if tc.wantWarn != "" && !strings.Contains(out, tc.wantWarn) {
				t.Errorf("missing %q in:\n%s", tc.wantWarn, out)
			}
			if tc.wantWarn == "" && strings.Contains(out, "[!]") {
				t.Errorf("unexpected warning:\n%s", out)
			}
		})
	}
}
