package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("77", "tmdb://12345")))
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
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("99", "tmdb://99999")))
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
			w.Write([]byte(plexMovieSectionsJSON("1", "show")))
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
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSONLegacyGuid("88", "tmdb://12345")))
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

func TestHTTPPlexMovieClient_FindMovieByTMDBID_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(plexAllMoviesJSON("77", "tmdb://12345")))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	c.FindMovieByTMDBID("12345")
	after := calls
	c.FindMovieByTMDBID("12345") // should use cache
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
	audioSet, subtitleSet, err := c.SetMovieStreams("42", "ja")
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
	audioSet, subtitleSet, err := c.SetMovieStreams("42", "ja")
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

func plexMetadataJSON(contentRating string) string {
	return `{"MediaContainer":{"Metadata":[{"contentRating":"` + contentRating + `"}]}}`
}

func TestHTTPPlexMovieClient_GetContentRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(plexMetadataJSON("nz/R16")))
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
		w.Write([]byte(plexMetadataJSON("TV-MA")))
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
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
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
func (f *fakeMultiPlexMovieClient) SetMovieStreams(rk, lang string) (bool, bool, error) {
	f.setStreamsCalls++
	f.setStreamsRatingKey = rk
	f.setStreamsLangTag = lang
	return true, true, f.setStreamsErr
}

func TestMultiPlexMovieClient_SetMovieStreams_CallsBoth(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, alt)
	audioSet, subtitleSet, err := mc.SetMovieStreams("rk1", "ja")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !audioSet || !subtitleSet {
		t.Error("expected audioSet and subtitleSet from primary")
	}
	if primary.setStreamsCalls != 1 || primary.setStreamsRatingKey != "rk1" || primary.setStreamsLangTag != "ja" {
		t.Errorf("primary: calls=%d key=%q lang=%q", primary.setStreamsCalls, primary.setStreamsRatingKey, primary.setStreamsLangTag)
	}
	if alt.setStreamsCalls != 1 || alt.setStreamsRatingKey != "rk1" || alt.setStreamsLangTag != "ja" {
		t.Errorf("alt: calls=%d key=%q lang=%q", alt.setStreamsCalls, alt.setStreamsRatingKey, alt.setStreamsLangTag)
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_PrimaryErrorPropagates(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{setStreamsErr: errors.New("primary failed")}
	alt := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, alt)
	if _, _, err := mc.SetMovieStreams("rk1", "ja"); err == nil {
		t.Fatal("expected error from primary")
	}
	if alt.setStreamsCalls != 0 {
		t.Error("alt should not be called when primary fails")
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_AltErrorStoredNotPropagated(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	altErr := errors.New("alt failed")
	alt := &fakeMultiPlexMovieClient{setStreamsErr: altErr}
	mc := newMultiPlexMovieClient(primary, alt)
	if _, _, err := mc.SetMovieStreams("rk1", "ja"); err != nil {
		t.Fatalf("alt error should not propagate: %v", err)
	}
	r, ok := mc.(altMovieErrReporter)
	if !ok {
		t.Fatal("multiPlexMovieClient should implement altMovieErrReporter")
	}
	if r.getAltErr() != altErr {
		t.Errorf("getAltErr() = %v; want %v", r.getAltErr(), altErr)
	}
}

func TestMultiPlexMovieClient_SetMovieStreams_AltErrClearedOnSuccess(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	alt := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, alt)
	alt.setStreamsErr = errors.New("alt failed")
	mc.SetMovieStreams("rk1", "ja") //nolint
	alt.setStreamsErr = nil
	mc.SetMovieStreams("rk1", "ja") //nolint
	r := mc.(altMovieErrReporter)
	if r.getAltErr() != nil {
		t.Errorf("getAltErr() should be nil after successful alt call, got %v", r.getAltErr())
	}
}

func TestNewMultiPlexMovieClient_NilAltReturnsPrimary(t *testing.T) {
	primary := &fakeMultiPlexMovieClient{}
	mc := newMultiPlexMovieClient(primary, nil)
	if mc != PlexMovieClient(primary) {
		t.Error("expected primary to be returned when alt is nil")
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"55","title":"The Matrix","year":1999}]}}`))
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
			w.Write([]byte(plexMovieSectionsJSON("3", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"66","title":"It's a Wonderful Life","year":1946}]}}`))
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
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"}]}}`))
		} else {
			refreshPaths = append(refreshPaths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	if err := c.RefreshLibraries(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshPaths) != 1 || refreshPaths[0] != "/library/sections/1/refresh" {
		t.Errorf("refreshPaths = %v; want [/library/sections/1/refresh]", refreshPaths)
	}
}

func TestHTTPPlexMovieClient_FindMovieByTitle_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			w.Write([]byte(plexMovieSectionsJSON("2", "movie")))
		} else {
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"77","title":"Other Film","year":2000}]}}`))
		}
	}))
	defer srv.Close()

	c := newHTTPPlexMovieClient(srv.URL, "token")
	_, err := c.FindMovieByTitle("The Matrix", "1999")
	if err != ErrPlexMovieNotFound {
		t.Errorf("expected ErrPlexMovieNotFound, got %v", err)
	}
}
