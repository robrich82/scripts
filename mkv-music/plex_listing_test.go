package main

import (
	"errors"
	"fmt"
	"maps"
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
	return maps.Clone(f.listings)
}

func (f *fakeArtistServer) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func wantListings(t *testing.T, f *fakeArtistServer, when string, want map[string]int) {
	t.Helper()
	if got := f.listingCounts(); !maps.Equal(got, want) {
		t.Errorf("%s: listings per section = %v; want %v", when, got, want)
	}
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
	wantListings(t, f, "after three hits", map[string]int{"1": 1, "2": 1})
}

// A miss re-lists each section once, in case Plex added the artist since, but
// not a section this lookup has just listed.
func TestHTTPPlexClient_FindArtistByTitle_MissListsEachSectionOnce(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha"}, "2": {"Gamma"}})

	if _, err := c.FindArtistByTitle("Nobody"); !errors.Is(err, ErrArtistNotFound) {
		t.Fatalf("err = %v; want ErrArtistNotFound", err)
	}
	wantListings(t, f, "first miss", map[string]int{"1": 1, "2": 1})
	if _, err := c.FindArtistByTitle("Nobody"); !errors.Is(err, ErrArtistNotFound) {
		t.Fatalf("err = %v; want ErrArtistNotFound", err)
	}
	wantListings(t, f, "second miss", map[string]int{"1": 2, "2": 2})
}

// An artist Plex added after its section was listed is found by the fresh
// pass, which caches the new listing for later lookups.
func TestHTTPPlexClient_FindArtistByTitle_FreshPassFindsAddedArtist(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha"}, "2": {"Gamma"}})
	if _, err := c.FindArtistByTitle("Gamma"); err != nil { // lists both sections
		t.Fatalf("FindArtistByTitle(Gamma): %v", err)
	}
	f.set(func() { f.artists["2"] = append(f.artists["2"], "Delta") })

	for range 2 {
		if got, err := c.FindArtistByTitle("Delta"); err != nil || got != "21" {
			t.Errorf("FindArtistByTitle(Delta) = %q, %v; want 21", got, err)
		}
	}
	wantListings(t, f, "after the fresh hit and a repeat", map[string]int{"1": 2, "2": 2})
}

// A cached match wins over a copy Plex added to another section since.
func TestHTTPPlexClient_FindArtistByTitle_CachedMatchWins(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"2": {"Alpha"}})
	if _, err := c.FindArtistByTitle("Alpha"); err != nil { // lists both sections
		t.Fatalf("FindArtistByTitle(Alpha): %v", err)
	}
	f.set(func() { f.artists["1"] = []string{"Alpha"} })

	if got, err := c.FindArtistByTitle("Alpha"); err != nil || got != "20" {
		t.Errorf("FindArtistByTitle(Alpha) = %q, %v; want the cached 20", got, err)
	}
	wantListings(t, f, "after the cached hit", map[string]int{"1": 1, "2": 1})
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

// A failed re-listing on a miss is returned too, and the cached listing still
// serves later hits.
func TestHTTPPlexClient_FindArtistByTitle_FreshPassListingError(t *testing.T) {
	f, c := newFakeArtistServer(t, map[string][]string{"1": {"Alpha"}})
	if _, err := c.FindArtistByTitle("Alpha"); err != nil {
		t.Fatalf("FindArtistByTitle(Alpha): %v", err)
	}
	f.set(func() { f.listErr = true })

	_, err := c.FindArtistByTitle("Nobody")
	if err == nil || errors.Is(err, ErrArtistNotFound) || !strings.Contains(err.Error(), "list section") {
		t.Errorf("miss: err = %v; want a section listing error", err)
	}
	if got, err := c.FindArtistByTitle("Alpha"); err != nil || got != "10" {
		t.Errorf("FindArtistByTitle(Alpha) after the error = %q, %v; want the cached 10", got, err)
	}
}

// A title with no ASCII letters or digits normalises to "", like every other
// such title, so it matches only its exact title (ignoring case).
func TestHTTPPlexClient_FindArtistByTitle_NonASCIITitle(t *testing.T) {
	_, c := newFakeArtistServer(t, map[string][]string{"1": {"椎名林檎", "宇多田ヒカル", "ÉÈ"}})

	for title, want := range map[string]string{"宇多田ヒカル": "11", "éè": "12"} {
		if got, err := c.FindArtistByTitle(title); err != nil || got != want {
			t.Errorf("FindArtistByTitle(%q) = %q, %v; want %s", title, got, err, want)
		}
	}
	for _, title := range []string{"浜崎あゆみ", " "} {
		if got, err := c.FindArtistByTitle(title); !errors.Is(err, ErrArtistNotFound) {
			t.Errorf("FindArtistByTitle(%q) = %q, %v; want ErrArtistNotFound", title, got, err)
		}
	}
}
