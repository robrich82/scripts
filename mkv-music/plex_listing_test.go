package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeArtistServer serves two music sections and a show section, and counts
// the listing requests per section. artists maps a section key to its
// artists' titles; a title's rating key is its section key plus its index.
type fakeArtistServer struct {
	mu       sync.Mutex
	artists  map[string][]string
	listings map[string]int
	listErr  bool
}

func (f *fakeArtistServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/library/sections" {
		fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"artist"},{"key":"2","type":"artist"},{"key":"3","type":"show"}]}}`)
		return
	}
	section := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/library/sections/"), "/all")
	f.listings[section]++
	if f.listErr {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	var items []string
	for i, title := range f.artists[section] {
		items = append(items, fmt.Sprintf(`{"ratingKey":"%s%d","title":%q}`, section, i, title))
	}
	fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[%s]}}`, strings.Join(items, ","))
}

func newFakeArtistServer(t *testing.T, artists map[string][]string) (*fakeArtistServer, *httpPlexClient) {
	t.Helper()
	f := &fakeArtistServer{artists: artists, listings: make(map[string]int)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, newHTTPPlexClient(srv.URL, "token")
}

func (f *fakeArtistServer) listingCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]int{"1": f.listings["1"], "2": f.listings["2"], "3": f.listings["3"]}
}

// A run that looks up several artists lists each music section once, not once
// per artist, and never lists a non-music section.
func TestHTTPPlexClient_FindArtistByTitle_ReusesSectionListing(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha", "Beta"}, "2": {"Gamma"}})

	for title, want := range map[string]string{"Gamma": "20", "alpha": "10", "Beta": "11"} {
		got, err := c.FindArtistByTitle(title)
		if err != nil || got != want {
			t.Errorf("FindArtistByTitle(%q) = %q, %v; want %q", title, got, err, want)
		}
	}
	if got, want := f.listingCounts(), map[string]int{"1": 1, "2": 1, "3": 0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("listings per section = %v; want %v", got, want)
	}
}

// A miss re-lists each section once, in case Plex added the artist since, but
// not a section this lookup has just listed.
func TestHTTPPlexClient_FindArtistByTitle_MissListsEachSectionOnce(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha"}, "2": {"Gamma"}})

	if _, err := c.FindArtistByTitle("Nobody"); !errors.Is(err, ErrArtistNotFound) {
		t.Fatalf("err = %v; want ErrArtistNotFound", err)
	}
	if got, want := f.listingCounts(), map[string]int{"1": 1, "2": 1, "3": 0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("first miss: listings per section = %v; want %v", got, want)
	}
	if _, err := c.FindArtistByTitle("Nobody"); !errors.Is(err, ErrArtistNotFound) {
		t.Fatalf("err = %v; want ErrArtistNotFound", err)
	}
	if got, want := f.listingCounts(), map[string]int{"1": 2, "2": 2, "3": 0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("second miss: listings per section = %v; want %v", got, want)
	}
}

// An artist Plex added after its section was listed is found by the fresh pass.
func TestHTTPPlexClient_FindArtistByTitle_FreshPassFindsAddedArtist(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha"}})
	if _, err := c.FindArtistByTitle("Alpha"); err != nil {
		t.Fatalf("FindArtistByTitle(Alpha): %v", err)
	}

	f.mu.Lock()
	f.artists["2"] = []string{"Delta"}
	f.mu.Unlock()

	got, err := c.FindArtistByTitle("Delta")
	if err != nil || got != "20" {
		t.Errorf("FindArtistByTitle(Delta) = %q, %v; want 20", got, err)
	}
}

// A failed section listing is returned, not reported as a missing artist.
func TestHTTPPlexClient_FindArtistByTitle_ListingError(t *testing.T) {
	f, c := newFakeArtistServer(t, nil)
	f.listErr = true

	_, err := c.FindArtistByTitle("Alpha")
	if err == nil || errors.Is(err, ErrArtistNotFound) || !strings.Contains(err.Error(), "list section 1") {
		t.Errorf("err = %v; want the section 1 listing error", err)
	}
}

// A title with no ASCII letters or digits normalises to "", the same as every
// other such artist; it must not match one of them, nor cost a request.
func TestHTTPPlexClient_FindArtistByTitle_NonASCIITitle(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"椎名林檎"}})

	key, err := c.FindArtistByTitle("宇多田ヒカル")
	if !errors.Is(err, ErrArtistNotFound) {
		t.Errorf("got key %q, err %v; want ErrArtistNotFound", key, err)
	}
	if got := f.listingCounts(); got["1"] != 0 {
		t.Errorf("listed section 1 %d times; want 0", got["1"])
	}
}
