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
func runOverwrite(t *testing.T, sources []RatingSource, prompter *scriptedPrompter) (*RatingCache, string) {
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
	out := captureStdout(t, func() {
		if stats := p.Run(); stats.Success != 2 {
			t.Errorf("stats = %+v; want 2 success", stats)
		}
	})
	return rc, out
}

func TestRun_OverwriteRatings_RefetchesOncePerShow(t *testing.T) {
	prompter := &scriptedPrompter{answers: []string{""}} // confirm the source's rating
	rc, _ := runOverwrite(t, []RatingSource{&fakeRatingSource{showRating: RatingM}}, prompter)

	if prompter.idx != 1 {
		t.Errorf("prompted %d times; want once per show", prompter.idx)
	}
	if got, _ := rc.GetShowRating("999"); got != RatingM {
		t.Errorf("cached rating = %q; want M re-fetched over the cached R16", got)
	}
}

func TestRun_OverwriteRatings_NoSourceData_KeepsCachedRating(t *testing.T) {
	prompter := &scriptedPrompter{}
	rc, out := runOverwrite(t, []RatingSource{&fakeRatingSource{}}, prompter)

	if prompter.idx != 0 {
		t.Errorf("prompted %d times; with no source data the cached rating is kept without a prompt", prompter.idx)
	}
	if got, _ := rc.GetShowRating("999"); got != RatingR16 {
		t.Errorf("cached rating = %q; want R16 kept", got)
	}
	if n := strings.Count(out, `[i] No source has a rating for "Rated Show"; keeping cached R16`); n != 1 {
		t.Errorf("keep-cached notice printed %d times; want once per show:\n%s", n, out)
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

// recordingPrompter answers each prompt in turn, then blank, and records the prompts.
type recordingPrompter struct {
	answers []string
	prompts []string
}

func (p *recordingPrompter) Ask(msg string) (string, error) {
	p.prompts = append(p.prompts, msg)
	if len(p.answers) == 0 {
		return "", nil
	}
	ans := p.answers[0]
	p.answers = p.answers[1:]
	return ans, nil
}

// After an invalid answer, the re-prompt says what blank does in that prompt.
func TestResolveEpisodeRating_RepromptNamesWhatBlankDoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []RatingSource
		plex    PlexClient
		want    string
	}{
		{"source", []RatingSource{&fakeRatingSource{showRating: RatingG}}, nil, "or blank to accept G: "},
		{"plex", nil, &fakePlexClient{showRatingKey: "42", contentRating: RatingM}, "or blank to use Plex's M: "},
		{"none", nil, nil, "or blank to skip: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := &recordingPrompter{answers: []string{"PG-13", ""}}
			p := &Processor{
				ratingCache:           emptyRatingCache(t),
				prompter:              pr,
				plex:                  tc.plex,
				ratingSources:         tc.sources,
				sessionPromptedRating: map[string]NZRating{},
			}
			captureStdout(t, func() { p.resolveEpisodeRating("12345", "Test Show", "", 1, 1) })
			if len(pr.prompts) != 2 || !strings.HasSuffix(pr.prompts[1], tc.want) {
				t.Errorf("prompts %q; want the re-prompt to end %q", pr.prompts, tc.want)
			}
		})
	}
}

// A typed code is cached as the show's rating and echoed, and the prompt shows
// Plex's rating as the default.
func TestResolveEpisodeRating_TypedCodeIsEchoed(t *testing.T) {
	pr := &recordingPrompter{answers: []string{"r16"}}
	rc := emptyRatingCache(t)
	p := &Processor{
		ratingCache:           rc,
		prompter:              pr,
		plex:                  &fakePlexClient{showRatingKey: "42", contentRating: RatingM},
		sessionPromptedRating: map[string]NZRating{},
	}

	var got NZRating
	out := captureStdout(t, func() { got = p.resolveEpisodeRating("12345", "Test Show", "", 1, 1) })

	if got != RatingR16 {
		t.Errorf("got %q; want R16", got)
	}
	if cached, _ := rc.GetShowRating("12345"); cached != RatingR16 {
		t.Errorf("cached %q; want R16", cached)
	}
	if len(pr.prompts) != 1 || !strings.Contains(pr.prompts[0], ") [M], or blank to use Plex's M: ") {
		t.Errorf("prompts %q; want Plex's M offered as the default", pr.prompts)
	}
	if !strings.Contains(out, "  → R16\n") {
		t.Errorf("choice not echoed:\n%s", out)
	}
}

// A full run records the rating it writes to an episode's tags in the applied
// map, as --ratings-only does, so the map can't disagree with the tags.
func TestRun_RecordsAppliedRating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached NZRating // the show's cached rating
		runner *fakeMKVRunner
		want   NZRating // applied afterwards; RatingNone means no entry
	}{
		{"written", RatingR16, &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}, RatingR16},
		{"no rating clears it", RatingNone, &fakeMKVRunner{avail: true, applyResult: "Done.", tagsPath: "t.xml"}, RatingNone},
		{"write fails", RatingR16, &fakeMKVRunner{avail: true, applyRC: 2, tagsPath: "t.xml"}, RatingM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			showDir := filepath.Join(dir, "Rated Show")
			if err := os.MkdirAll(showDir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(showDir, "Rated Show - S01E01.mkv")
			if err := os.WriteFile(path, []byte{}, 0644); err != nil {
				t.Fatal(err)
			}
			rc := emptyRatingCache(t)
			rc.SetShowRating("999", tc.cached)
			rc.MarkApplied(path, RatingM) // left by an earlier --ratings-only run
			tmdb := &fakeTMDB{seasonResult: SeasonResult{Episodes: []Episode{{EpisodeNumber: 1, AirDate: "2022-01-01"}}}}
			p := NewProcessor(Config{MediaDir: dir, ProcessAll: true},
				cacheWith(t, map[string]string{"Rated Show": "999"}), map[string]ManualEntry{}, tmdb, tc.runner,
				&scriptedPrompter{answers: []string{""}}, &captureFileTimer{}, emptyRetryList(t), rc, nil, nil, nil)
			captureStdout(t, func() { p.Run() })

			saved, err := LoadRatingCache(rc.path)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := saved.data.Applied[path]
			if NZRating(got) != tc.want || ok != (tc.want != RatingNone) {
				t.Errorf("saved applied = %q (present %v); want %q", got, ok, tc.want)
			}
		})
	}
}

func TestMarkApplied_NoneClears(t *testing.T) {
	rc := emptyRatingCache(t)
	rc.MarkApplied("/a.mkv", RatingR16)
	rc.MarkApplied("/a.mkv", RatingNone)
	if _, ok := rc.data.Applied["/a.mkv"]; ok {
		t.Error("RatingNone should delete the applied entry, not store \"\"")
	}
}
