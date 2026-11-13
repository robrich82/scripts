package main

import (
	"errors"
	"strings"
	"testing"
)

// altFixture is one movie's Plex accounts: a primary whose streams are already
// right (its fake reports no change) and the given alts, sharing one cache.
type altFixture struct {
	t       *testing.T
	path    string
	cache   *MovieRatingCache
	primary *fakePlexMovieClient
}

func newAltFixture(t *testing.T) *altFixture {
	t.Helper()
	return &altFixture{t: t, path: makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv"), cache: makeCache(t), primary: &fakePlexMovieClient{tmdbRatingKey: "10"}}
}

// run processes the movie (original language "en") once with the given alt
// accounts, in altAccount, alt2Account order.
func (f *altFixture) run(applier MKVApplier, overwriteStreams bool, alts ...*fakeMultiPlexMovieClient) (FileResult, string) {
	f.t.Helper()
	names := []string{altAccount, alt2Account}
	var named []namedPlexMovieClient
	for i, a := range alts {
		named = append(named, namedPlexMovieClient{name: names[i], client: a})
	}
	rr := newMovieRatingResolver(nil, f.cache, false, overwriteStreams, newMultiPlexMovieClient(f.primary, named...), answerPrompter())
	var result FileResult
	out := captureStdout(f.t, func() {
		result = processFile(f.path, &fakeTMDBMovieClient{tmdbID: "12345", details: detailsWithLanguage()}, applier, &fakeFileTimer{}, rr, f.cache, nil)
	})
	return result, out
}

func (f *altFixture) recorded() string {
	got, _ := f.cache.GetStreamsApplied("12345")
	return got
}

func failingAlt() *fakeMultiPlexMovieClient {
	return &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}
}

// #141: an alt account's failure shows even when no account's streams changed,
// and is a warning, not an error. The accounts that succeeded are recorded; the
// failed alt isn't, so a later run retries it alone.
func TestProcessFile_AltFailure_ShownAndRecordedWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		applier    MKVApplier
		wantLine   string
		wantResult FileResult
	}{
		{"tagged", &fakeApplier{}, "[UPDATED] Avengers.Endgame.2019.mkv -> 2019-04-26 (lang: unchanged; alt: alt failed)\n", ResultUpdated},
		{"no MKVToolNix", nil, "[SKIP]    Avengers.Endgame.2019.mkv (mkvpropedit/mkvextract not found; file not changed) (Plex lang: unchanged; alt: alt failed)\n", ResultSkipped},
		{"tag write failed", &fakeApplier{err: errors.New("rc 2")}, "[ERR]     Avengers.Endgame.2019.mkv: tags not written (Plex lang: unchanged; alt: alt failed)\n", ResultError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAltFixture(t)
			result, out := f.run(tc.applier, false, failingAlt())
			if result != tc.wantResult {
				t.Errorf("got %v, want %v", result, tc.wantResult)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("result line isn't %q:\n%s", tc.wantLine, out)
			}
			if strings.Contains(out, "failed: Plex streams") {
				t.Errorf("alt failure counted as a failed step:\n%s", out)
			}
			if got := f.recorded(); got != "en" {
				t.Errorf("recorded %q; want the primary only (en)", got)
			}
		})
	}
}

// The retry pushes only to the alt that failed: the primary, whose streams the
// user may have changed by hand since, is left alone.
func TestProcessFile_AltFailure_RetriesOnlyThatAlt(t *testing.T) {
	f := newAltFixture(t)
	okAlt := &fakeMultiPlexMovieClient{}
	f.run(&fakeApplier{}, false, okAlt, failingAlt())
	if got := f.recorded(); got != "en|alt=en" {
		t.Fatalf("first run recorded %q; want en|alt=en", got)
	}

	f.primary.streamSets = 0
	okAlt.setStreamsCalls = 0
	alt2 := &fakeMultiPlexMovieClient{}
	_, out := f.run(&fakeApplier{}, false, okAlt, alt2)
	if f.primary.streamSets != 0 || okAlt.setStreamsCalls != 0 {
		t.Errorf("retry re-set accounts that had succeeded: primary %d, alt %d", f.primary.streamSets, okAlt.setStreamsCalls)
	}
	if alt2.setStreamsCalls != 1 {
		t.Errorf("alt2 calls on the retry = %d, want 1", alt2.setStreamsCalls)
	}
	if got := f.recorded(); got != "en|alt=en|alt2=en" {
		t.Errorf("recorded %q; want en|alt=en|alt2=en", got)
	}
	if strings.Contains(out, "alt2:") {
		t.Errorf("alt2 failure still shown after it succeeded:\n%s", out)
	}

	// Everything recorded: nothing is pushed again.
	alt2.setStreamsCalls = 0
	f.run(&fakeApplier{}, false, okAlt, alt2)
	if f.primary.streamSets != 0 || okAlt.setStreamsCalls != 0 || alt2.setStreamsCalls != 0 {
		t.Errorf("streams re-set once recorded: primary %d, alt %d, alt2 %d", f.primary.streamSets, okAlt.setStreamsCalls, alt2.setStreamsCalls)
	}
}

// --overwrite-streams sets every account again, and still leaves a failed alt
// out of the record, so a plain run afterwards retries it.
func TestProcessFile_AltFailure_OverwriteStreams(t *testing.T) {
	f := newAltFixture(t)
	f.cache.SetStreamsApplied("12345", "en|alt=en")
	_, out := f.run(&fakeApplier{}, true, failingAlt())
	if f.primary.streamSets != 1 {
		t.Errorf("primary calls = %d, want 1 under --overwrite-streams", f.primary.streamSets)
	}
	if !strings.Contains(out, "alt: alt failed") {
		t.Errorf("alt failure not shown:\n%s", out)
	}
	if got := f.recorded(); got != "en" {
		t.Errorf("recorded %q; want en, without the failed alt", got)
	}
}

// When the primary did change, the line shows the signature and the alt failure.
func TestProcessFile_AltFailure_PrimaryChanged(t *testing.T) {
	f := newAltFixture(t)
	primary := &primaryWithLookup{&fakeMultiPlexMovieClient{}} // reports a change
	plex := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: failingAlt()})
	rr := newMovieRatingResolver(nil, f.cache, false, false, plex, answerPrompter())
	out := captureStdout(t, func() {
		processFile(f.path, &fakeTMDBMovieClient{tmdbID: "12345", details: detailsWithLanguage()}, &fakeApplier{}, &fakeFileTimer{}, rr, f.cache, nil)
	})
	if !strings.Contains(out, "(lang: en|alt=en; alt: alt failed)") {
		t.Errorf("line doesn't show the change and the alt failure:\n%s", out)
	}
	if got := f.recorded(); got != "en" {
		t.Errorf("recorded %q; want en", got)
	}
}

// primaryWithLookup lets a fakeMultiPlexMovieClient, which reports stream
// changes, stand in as a primary that finds the movie.
type primaryWithLookup struct{ *fakeMultiPlexMovieClient }

func (p *primaryWithLookup) FindMovieByTMDBID(_ string) (string, error) { return "10", nil }

// #141: a change on an alt account alone shows on the result line.
func TestProcessFile_AltChangeOnly_Shown(t *testing.T) {
	f := newAltFixture(t)
	result, out := f.run(&fakeApplier{}, false, &fakeMultiPlexMovieClient{})
	if result != ResultUpdated || !strings.Contains(out, "(lang: en|alt=en)") {
		t.Errorf("got %v; want the alt change on the result line:\n%s", result, out)
	}
	if got := f.recorded(); got != "en|alt=en" {
		t.Errorf("recorded %q; want en|alt=en", got)
	}
}

// Adding an account sets only that account.
func TestProcessFile_NewAltAccount_SetsOnlyIt(t *testing.T) {
	f := newAltFixture(t)
	f.cache.SetStreamsApplied("12345", "en")
	alt := &fakeMultiPlexMovieClient{}
	f.run(&fakeApplier{}, false, alt)
	if f.primary.streamSets != 0 || alt.setStreamsCalls != 1 {
		t.Errorf("primary %d, alt %d calls; want 0 and 1", f.primary.streamSets, alt.setStreamsCalls)
	}
	if got := f.recorded(); got != "en|alt=en" {
		t.Errorf("recorded %q; want en|alt=en", got)
	}
}

// Removing every alt token doesn't set the primary's streams again, which would
// undo a selection made in Plex since; only the record shrinks.
func TestProcessFile_AltTokensRemoved_PrimaryNotSet(t *testing.T) {
	path := makeTempMKVNamed(t, "Avengers.Endgame.2019.mkv")
	cache := makeCache(t)
	cache.SetStreamsApplied("12345", "en|alt=en")
	primary := &fakePlexMovieClient{tmdbRatingKey: "10"}
	rr := newMovieRatingResolver(nil, cache, false, false, primary, answerPrompter())
	captureStdout(t, func() {
		processFile(path, &fakeTMDBMovieClient{tmdbID: "12345", details: detailsWithLanguage()}, &fakeApplier{}, &fakeFileTimer{}, rr, cache, nil)
	})
	if primary.streamSets != 0 {
		t.Errorf("primary streams set %d times; want 0", primary.streamSets)
	}
	if got, _ := cache.GetStreamsApplied("12345"); got != "en" {
		t.Errorf("recorded %q; want en", got)
	}
}

// A pending primary that fails is an error: nothing is recorded and the alts
// aren't tried.
func TestProcessFile_PrimaryStreamsFailure_RecordsNothing(t *testing.T) {
	f := newAltFixture(t)
	f.primary.setStreamsErr = errors.New("plex: 403")
	alt := &fakeMultiPlexMovieClient{}
	result, out := f.run(&fakeApplier{}, false, alt)
	if result != ResultError || !strings.Contains(out, "(failed: Plex streams)") {
		t.Errorf("got %v; want ResultError naming the streams:\n%s", result, out)
	}
	if alt.setStreamsCalls != 0 {
		t.Errorf("alt tried %d times after the primary failed", alt.setStreamsCalls)
	}
	if _, ok := f.cache.GetStreamsApplied("12345"); ok {
		t.Error("streams recorded though the primary failed")
	}
}

// With everything succeeding, the record equals appliedSignature, so
// plexStreamsNeedSet stops the next run.
func TestMultiPlexMovieClient_SetStreamsPending_RecordMatchesSignature(t *testing.T) {
	primary, alt, alt2 := &fakeMultiPlexMovieClient{}, &fakeMultiPlexMovieClient{}, &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary,
		namedPlexMovieClient{name: altAccount, client: alt},
		namedPlexMovieClient{name: alt2Account, client: alt2}).(*multiPlexMovieClient)
	langs := AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "en"}}
	_, _, record, _ := mc.setStreamsPending("rk1", langs, "")
	if want := mc.appliedSignature(langs); record != want {
		t.Errorf("record = %q; want %q", record, want)
	}
	mc.setStreamsPending("rk1", langs, record) //nolint:errcheck // only the call counts matter
	if primary.setStreamsCalls != 1 || alt.setStreamsCalls != 1 || alt2.setStreamsCalls != 1 {
		t.Errorf("calls primary %d, alt %d, alt2 %d; want 1 each", primary.setStreamsCalls, alt.setStreamsCalls, alt2.setStreamsCalls)
	}

	// A changed primary language sets only the primary when the alt has its own.
	primary.setStreamsCalls, alt.setStreamsCalls, alt2.setStreamsCalls = 0, 0, 0
	langs = AudioLangs{Default: "ko", Alts: map[string]string{altAccount: "ja", alt2Account: "en"}}
	_, _, record, _ = mc.setStreamsPending("rk1", langs, "ja|alt=ja|alt2=en")
	if primary.setStreamsCalls != 1 || alt.setStreamsCalls != 0 || alt2.setStreamsCalls != 0 || record != "ko|alt=ja|alt2=en" {
		t.Errorf("calls primary %d, alt %d, alt2 %d, record %q; want only the primary, ko|alt=ja|alt2=en", primary.setStreamsCalls, alt.setStreamsCalls, alt2.setStreamsCalls, record)
	}
}

func TestParseLangSignature(t *testing.T) {
	for _, tc := range []struct {
		sig  string
		want map[string]string
	}{
		{"", map[string]string{}},
		{"ja", map[string]string{"": "ja"}},
		{"ja|alt=en|alt2=ja", map[string]string{"": "ja", "alt": "en", "alt2": "ja"}},
		{"ja|alt||alt2=en", map[string]string{"": "ja", "alt2": "en"}}, // malformed entries dropped
	} {
		got := parseLangSignature(tc.sig)
		if len(got) != len(tc.want) {
			t.Errorf("parseLangSignature(%q) = %v; want %v", tc.sig, got, tc.want)
			continue
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("parseLangSignature(%q)[%q] = %q; want %q", tc.sig, k, got[k], v)
			}
		}
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
