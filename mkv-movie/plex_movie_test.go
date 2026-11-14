package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func plexMovieSectionsJSON(sectionKey, sectionType string) string {
	return `{"MediaContainer":{"Directory":[{"key":"` + sectionKey + `","type":"` + sectionType + `"}]}}`
}

func plexAllMoviesJSON(ratingKey, tmdbGUID string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":[{"id":"` + tmdbGUID + `"}]}]}}`
}

func plexAllMoviesJSONLegacyGuid(ratingKey, guidStr string) string {
	return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + ratingKey + `","Guid":"` + guidStr + `"}]}}`
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, plexAllMoviesJSON("77", "tmdb://12345"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "77" {
		t.Errorf("got ratingKey %q; want 77", key)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, plexAllMoviesJSON("99", "tmdb://99999"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTMDBID("12345")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_SkipsShowSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// Only a "show" section — should be skipped.
			fmt.Fprint(w, plexMovieSectionsJSON("1", "show"))
		} else {
			t.Error("should not scan non-movie section")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTMDBID("12345")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_LegacyGuid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, plexAllMoviesJSONLegacyGuid("88", "tmdb://12345"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTMDBID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "88" {
		t.Errorf("got ratingKey %q; want 88", key)
	}
}

// A legacy-agent movie listed with includeGuids=1 may have an empty Guid
// array; its lowercase guid still identifies it.
func TestHTTPPlexMovieClient_FindMovieByTMDBID_LegacyGuidWithEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"88","guid":"com.plexapp.agents.themoviedb://12345?lang=en","Guid":[]}]}}`)
		}
	}))
	defer srv.Close()

	key, err := newHTTPPlexMovieClient(srv.URL, "token").FindMovieByTMDBID("12345")
	if err != nil || key != "88" {
		t.Errorf("got %q, %v; want 88", key, err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTMDBID_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, plexAllMoviesJSON("77", "tmdb://12345"))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	if _, err := c.FindMovieByTMDBID("12345"); err != nil {
		t.Fatal(err)
	}
	after := calls
	if _, err := c.FindMovieByTMDBID("12345"); err != nil { // should use cache
		t.Fatal(err)
	}
	if calls != after {
		t.Errorf("second FindMovieByTMDBID made HTTP requests")
	}
}

func TestHTTPPlexMovieClient_SetContentRating_SendsCorrectParams(t *testing.T) {
	var gotQuery url.Values
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "mytoken")
	err := c.SetContentRating("42", RatingM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotQuery.Get("contentRating.value") != "nz/M" {
		t.Errorf("contentRating.value = %q; want nz/M", gotQuery.Get("contentRating.value"))
	}
	if gotQuery.Get("contentRating.locked") != "1" {
		t.Errorf("contentRating.locked = %q; want 1", gotQuery.Get("contentRating.locked"))
	}
	if gotQuery.Get("type") != "1" {
		t.Errorf("type = %q; want 1 (movie)", gotQuery.Get("type"))
	}
}

func streamsMetaJSON(audioLangTag string, audioSelected bool, subSelected bool) string {
	return `{"MediaContainer":{"Metadata":[{"Media":[{"Part":[{"id":100,"Stream":[` +
		`{"id":201,"streamType":2,"languageTag":"` + audioLangTag + `","selected":` + fmt.Sprintf("%t", audioSelected) + `},` +
		`{"id":202,"streamType":2,"languageTag":"en","selected":false},` +
		`{"id":301,"streamType":3,"languageTag":"en","selected":` + fmt.Sprintf("%t", subSelected) + `},` +
		`{"id":302,"streamType":3,"languageTag":"en","forced":true}` +
		`]}]}]}]}}`
}

func TestHTTPPlexMovieClient_SetMovieStreams_SelectsAudioAndSubtitle(t *testing.T) {
	var reqs []struct{ method, path string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, struct{ method, path string }{r.Method, r.URL.Path})
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, streamsMetaJSON("ja", false, false))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "tok")
	audioSet, subtitleSet, err := c.SetMovieStreams("42", AudioLangs{Default: "ja"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !audioSet {
		t.Error("expected audioSet=true")
	}
	if !subtitleSet {
		t.Error("expected subtitleSet=true")
	}
	if len(reqs) != 3 {
		t.Fatalf("expected 3 requests (GET + 2 PUTs), got %d", len(reqs))
	}
	if reqs[1].path != "/library/parts/100" || reqs[1].method != http.MethodPut {
		t.Errorf("req[1] = %+v; want PUT /library/parts/100", reqs[1])
	}
	if reqs[2].path != "/library/parts/100" || reqs[2].method != http.MethodPut {
		t.Errorf("req[2] = %+v; want PUT /library/parts/100", reqs[2])
	}
}

func TestHTTPPlexMovieClient_SetMovieStreams_SkipsAlreadySelected(t *testing.T) {
	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCount++
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, streamsMetaJSON("ja", true, true))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "tok")
	audioSet, subtitleSet, err := c.SetMovieStreams("42", AudioLangs{Default: "ja"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audioSet || subtitleSet {
		t.Error("expected no changes when streams already selected")
	}
	if putCount != 0 {
		t.Errorf("expected 0 PUT requests, got %d", putCount)
	}
}

func TestHTTPPlexMovieClient_SetMovieStreams_NoAudioInLanguage(t *testing.T) {
	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCount++
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, streamsMetaJSON("ja", false, false))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "tok")
	_, _, err := c.SetMovieStreams("42", AudioLangs{Default: "ko"})
	if !errors.Is(err, ErrPlexNoAudioStream) {
		t.Errorf("err = %v; want ErrPlexNoAudioStream", err)
	}
	if putCount != 0 {
		t.Errorf("expected 0 PUT requests, got %d", putCount)
	}
}

func plexMetadataJSON(contentRating string) string {
	return `{"MediaContainer":{"Metadata":[{"contentRating":"` + contentRating + `"}]}}`
}

func TestHTTPPlexMovieClient_GetContentRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, plexMetadataJSON("nz/R16"))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingR16 {
		t.Errorf("got %q; want R16", rating)
	}
}

func TestHTTPPlexMovieClient_GetContentRating_UnknownRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, plexMetadataJSON("TV-MA"))
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone for unrecognised rating", rating)
	}
}

func TestHTTPPlexMovieClient_GetContentRating_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No Metadata entries at all.
		fmt.Fprint(w, `{"MediaContainer":{"Metadata":[]}}`)
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	rating, err := c.GetContentRating("99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("got %q; want RatingNone when metadata is empty", rating)
	}
}

// --- multiPlexMovieClient tests ---

type fakeMultiPlexMovieClient struct {
	setStreamsRatingKey string
	setStreamsLangTag   string
	setStreamsCalls     int
	setStreamsErr       error
	unchanged           bool // SetMovieStreams reports no change
}

func (f *fakeMultiPlexMovieClient) FindMovieByTMDBID(_ string) (string, error) {
	return "", ErrPlexMovieNotFound
}
func (f *fakeMultiPlexMovieClient) FindMovieByTitle(_, _ string) (string, error) {
	return "", ErrPlexMovieNotFound
}
func (f *fakeMultiPlexMovieClient) GetContentRating(_ string) (NZRating, error) {
	return RatingNone, nil
}
func (f *fakeMultiPlexMovieClient) SetContentRating(_ string, _ NZRating) error { return nil }
func (f *fakeMultiPlexMovieClient) RefreshLibraries() error                     { return nil }

// SetMovieStreams records the language this account would actually apply, i.e.
// langs.Default — multiPlexMovieClient resolves each alt's override before
// calling its client, so Default is that account's own language.
func (f *fakeMultiPlexMovieClient) SetMovieStreams(rk string, langs AudioLangs) (bool, bool, error) {
	f.setStreamsCalls++
	f.setStreamsRatingKey = rk
	f.setStreamsLangTag = langs.Default
	return !f.unchanged, !f.unchanged, f.setStreamsErr
}

// An alt account's change counts as a change even when the primary had none;
// a failed alt's flags don't.
func TestMultiPlexMovieClient_SetMovieStreams_ReportsAltChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		primary *fakeMultiPlexMovieClient
		alt     *fakeMultiPlexMovieClient
		wantSet bool
	}{
		{"alt changed", &fakeMultiPlexMovieClient{unchanged: true}, &fakeMultiPlexMovieClient{}, true},
		{"alt unchanged", &fakeMultiPlexMovieClient{unchanged: true}, &fakeMultiPlexMovieClient{unchanged: true}, false},
		{"alt failed", &fakeMultiPlexMovieClient{unchanged: true}, &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}, false},
		{"primary changed, alt unchanged", &fakeMultiPlexMovieClient{}, &fakeMultiPlexMovieClient{unchanged: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMultiPlexMovieClient(tc.primary, namedPlexMovieClient{name: altAccount, client: tc.alt})
			audioSet, subtitleSet, err := mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if audioSet != tc.wantSet || subtitleSet != tc.wantSet {
				t.Errorf("audioSet=%v subtitleSet=%v; want %v", audioSet, subtitleSet, tc.wantSet)
			}
		})
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_CallsAll(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{}
	alt2 := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary,
		namedPlexMovieClient{name: altAccount, client: alt},
		namedPlexMovieClient{name: alt2Account, client: alt2})
	audioSet, subtitleSet, err := mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !audioSet || !subtitleSet {
		t.Error("expected audioSet and subtitleSet from primary")
	}
	for name, c := range map[string]*fakeMultiPlexMovieClient{"primary": primary, "alt": alt, "alt2": alt2} {
		if c.setStreamsCalls != 1 || c.setStreamsRatingKey != "rk1" || c.setStreamsLangTag != "ja" {
			t.Errorf("%s: calls=%d key=%q lang=%q", name, c.setStreamsCalls, c.setStreamsRatingKey, c.setStreamsLangTag)
		}
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_PrimaryErrorPropagates(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("primary failed")}
	alt := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: alt})
	if _, _, err := mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"}); err == nil {
		t.Fatal("expected error from primary")
	}
	if alt.setStreamsCalls != 0 {
		t.Error("alt should not be called when primary fails")
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_AltErrorStoredNotPropagated(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}
	alt2 := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("plex: set audio stream: status 404: nope")}
	mc := newMultiPlexMovieClient(primary,
		namedPlexMovieClient{name: altAccount, client: alt},
		namedPlexMovieClient{name: alt2Account, client: alt2})
	if _, _, err := mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"}); err != nil {
		t.Fatalf("alt error should not propagate: %v", err)
	}
	r, ok := mc.(multiAccountReporter)
	if !ok {
		t.Fatal("multiPlexMovieClient should implement multiAccountReporter")
	}
	want := "; alt: alt failed; alt2: not in library"
	if got := r.altErrSuffix(); got != want {
		t.Errorf("altErrSuffix() = %q; want %q", got, want)
	}
}

// One alt fails, the next still runs; the record holds the primary and the alt
// that succeeded, and both failures would be joined in the suffix.
func TestMultiPlexMovieClient_SetStreamsPending_PartialFailure(t *testing.T) {
	alt := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("alt failed")}
	alt2 := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(&fakeMultiPlexMovieClient{unchanged: true},
		namedPlexMovieClient{name: altAccount, client: alt},
		namedPlexMovieClient{name: alt2Account, client: alt2}).(*multiPlexMovieClient)
	langs := AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "en"}}
	audioSet, _, record, err := mc.setStreamsPending("rk1", langs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alt2.setStreamsCalls != 1 || !audioSet {
		t.Errorf("alt2 calls %d, audioSet %v; want alt2 set after alt failed", alt2.setStreamsCalls, audioSet)
	}
	if record != "ja|alt2=en" {
		t.Errorf("record = %q; want ja|alt2=en", record)
	}
	if got := mc.altErrSuffix(); got != "; alt: alt failed" {
		t.Errorf("altErrSuffix() = %q", got)
	}

	// A changed language for one account re-sets only that account.
	primary := mc.primary.(*fakeMultiPlexMovieClient)
	alt.setStreamsErr = nil
	primary.setStreamsCalls, alt.setStreamsCalls, alt2.setStreamsCalls = 0, 0, 0
	langs.Alts[alt2Account] = "ko"
	_, _, record, _ = mc.setStreamsPending("rk1", langs, "ja|alt=ja|alt2=en")
	if primary.setStreamsCalls != 0 || alt.setStreamsCalls != 0 || alt2.setStreamsCalls != 1 {
		t.Errorf("calls primary %d, alt %d, alt2 %d; want only alt2", primary.setStreamsCalls, alt.setStreamsCalls, alt2.setStreamsCalls)
	}
	if record != "ja|alt=ja|alt2=ko" {
		t.Errorf("record = %q; want ja|alt=ja|alt2=ko", record)
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_AltErrClearedOnSuccess(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, namedPlexMovieClient{name: altAccount, client: alt})
	alt.setStreamsErr = errors.New("alt failed")
	mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"}) //nolint
	alt.setStreamsErr = nil
	mc.SetMovieStreams("rk1", AudioLangs{Default: "ja"}) //nolint
	r := mc.(multiAccountReporter)
	if got := r.altErrSuffix(); got != "" {
		t.Errorf("altErrSuffix() should be empty after successful alt call, got %q", got)
	}
}

func TestNewMultiPlexMovieClient_NoAltsReturnsPrimary(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary)
	if mc != PlexMovieClient(primary) {
		t.Error("expected primary to be returned when there are no alts")
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_PerAccountOverrides(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{}
	alt2 := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary,
		namedPlexMovieClient{name: altAccount, client: alt},
		namedPlexMovieClient{name: alt2Account, client: alt2})
	// alt overrides the language; alt2 has none and falls back to the default.
	langs := AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}}
	if _, _, err := mc.SetMovieStreams("rk1", langs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"primary": "ja", "alt": "en", "alt2": "ja"}
	for name, c := range map[string]*fakeMultiPlexMovieClient{"primary": primary, "alt": alt, "alt2": alt2} {
		if c.setStreamsLangTag != want[name] {
			t.Errorf("%s: lang = %q; want %q", name, c.setStreamsLangTag, want[name])
		}
	}
}

func TestAppliedLangSignature(t *testing.T) {
	alts := []namedPlexMovieClient{
		{name: altAccount, client: &fakeMultiPlexMovieClient{}},
		{name: alt2Account, client: &fakeMultiPlexMovieClient{}},
	}
	cases := []struct {
		name  string
		alts  []namedPlexMovieClient
		langs AudioLangs
		want  string
	}{
		{
			// No alt tokens: the bare language, so caches written before
			// per-account languages existed still match and do not re-apply.
			"no alt accounts", nil, AudioLangs{Default: "ja"}, "ja",
		},
		{
			// Listed even though they inherit "ja" — otherwise adding a token
			// would leave already-applied titles skipped forever.
			"alt accounts inheriting the default", alts, AudioLangs{Default: "ja"},
			"ja|alt=ja|alt2=ja",
		},
		{
			"one account overriding", alts,
			AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}},
			"ja|alt=en|alt2=ja",
		},
		{
			"both accounts overriding", alts,
			AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en", alt2Account: "fr"}},
			"ja|alt=en|alt2=fr",
		},
		{
			"only alt2 configured", alts[1:],
			AudioLangs{Default: "ja", Alts: map[string]string{alt2Account: "fr"}},
			"ja|alt2=fr",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newMultiPlexMovieClient(&fakeMultiPlexMovieClient{}, tc.alts...)
			if got := appliedLangSignature(client, tc.langs); got != tc.want {
				t.Errorf("appliedLangSignature = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestAudioLangs_ForAccount_FallsBackToDefault(t *testing.T) {
	langs := AudioLangs{Default: "ja", Alts: map[string]string{altAccount: "en"}}
	if got := langs.forAccount(altAccount); got != "en" {
		t.Errorf("forAccount(alt) = %q; want en", got)
	}
	if got := langs.forAccount(alt2Account); got != "ja" {
		t.Errorf("forAccount(alt2) = %q; want ja (default)", got)
	}
}

func TestMovieManualEntry_AudioLangs(t *testing.T) {
	// No "language": TMDB's original_language is the default.
	got := MovieManualEntry{AltLanguage: "en"}.audioLangs("ja")
	if got.Default != "ja" || got.forAccount(altAccount) != "en" || got.forAccount(alt2Account) != "ja" {
		t.Errorf("tmdb fallback: %+v", got)
	}
	// "language" overrides TMDB, and alts fall back to it rather than to TMDB.
	got = MovieManualEntry{Language: "ko", Alt2Language: "fr"}.audioLangs("ja")
	if got.Default != "ko" || got.forAccount(altAccount) != "ko" || got.forAccount(alt2Account) != "fr" {
		t.Errorf("language override: %+v", got)
	}
}

func TestAltPlexMovieClients_ReadsBothTokens(t *testing.T) {
	t.Setenv("PLEX_ALT_TOKEN", "")
	t.Setenv("PLEX_ALT2_TOKEN", "")
	if got := altPlexMovieClients("http://plex"); got != nil {
		t.Errorf("no tokens set: got %d clients; want 0", len(got))
	}

	t.Setenv("PLEX_ALT2_TOKEN", "tok2")
	got := altPlexMovieClients("http://plex")
	if len(got) != 1 || got[0].name != "alt2" {
		t.Fatalf("only PLEX_ALT2_TOKEN set: got %+v; want one client named alt2", got)
	}

	t.Setenv("PLEX_ALT_TOKEN", "tok1")
	got = altPlexMovieClients("http://plex")
	if len(got) != 2 || got[0].name != "alt" || got[1].name != "alt2" {
		t.Fatalf("both tokens set: got %+v; want alt then alt2", got)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"The Matrix","year":1999}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTitle("The Matrix", "1999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "55" {
		t.Errorf("got ratingKey %q; want 55", key)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_NormalisedMatch(t *testing.T) {
	// Title in Plex has punctuation; search title differs in case and punctuation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("3", "movie"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"66","title":"It's a Wonderful Life","year":1946}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	key, err := c.FindMovieByTitle("its a wonderful life", "1946")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "66" {
		t.Errorf("got ratingKey %q; want 66", key)
	}
}

func TestHTTPPlexMovieClient_RefreshLibraries_RefreshesMovieSections(t *testing.T) {
	var refreshPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			// One movie section and one show section — only the movie section should be refreshed.
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"}]}}`)
		} else {
			refreshPaths = append(refreshPaths, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	if err := c.RefreshLibraries(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshPaths) != 1 || refreshPaths[0] != "POST /library/sections/1/refresh" {
		t.Errorf("refresh requests = %v; want [POST /library/sections/1/refresh]", refreshPaths)
	}
}

// A failed section refresh is returned, naming the section, so the caller can
// warn that Plex didn't rescan.
func TestHTTPPlexMovieClient_RefreshLibraries_RefreshError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/sections" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"}]}}`)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := newHTTPPlexMovieClient(srv.URL, "token").RefreshLibraries()
	if err == nil || !strings.Contains(err.Error(), "refresh section 1") {
		t.Errorf("err %v; want it to name section 1", err)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, plexMovieSectionsJSON("2", "movie"))
		} else {
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"77","title":"Other Film","year":2000}]}}`)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTitle("The Matrix", "1999")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}
