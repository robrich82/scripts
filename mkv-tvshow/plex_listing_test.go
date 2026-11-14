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

// listingServer serves two show sections, "1" and "2". Each listing request
// gets the next body in that section's list (the last repeats), or a 500 for "".
// It returns how often each section was listed.
func listingServer(t *testing.T, bodies map[string][]string) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	listed := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"show"},{"key":"2","type":"show"},{"key":"3","type":"movie"}]}}`)
			return
		}
		for section, list := range bodies {
			if r.URL.Path != "/library/sections/"+section+"/all" {
				continue
			}
			mu.Lock()
			n := listed[section]
			listed[section]++
			mu.Unlock()
			body := list[min(n, len(list)-1)]
			if body == "" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]int, len(listed))
		for k, v := range listed {
			out[k] = v
		}
		return out
	}
}

func showsJSON(shows ...string) string {
	return `{"MediaContainer":{"Metadata":[` + strings.Join(shows, ",") + `]}}`
}

func showJSON(ratingKey, title, tmdbID string) string {
	return `{"ratingKey":"` + ratingKey + `","title":"` + title + `","Guid":[{"id":"tmdb://` + tmdbID + `"}]}`
}

// #172: a section's listing is fetched once a run and answers later lookups,
// by TMDB ID and by title alike.
func TestHTTPPlexClient_ReusesSectionListing(t *testing.T) {
	srv, listed := listingServer(t, map[string][]string{
		"1": {showsJSON(showJSON("77", "Show A", "129"), showJSON("78", "Show B", "354912"))},
		"2": {showsJSON(showJSON("88", "Show C", "1399"))},
	})
	c := newHTTPPlexClient(srv.URL, "tok")

	for id, want := range map[string]string{"129": "77", "354912": "78", "1399": "88"} {
		if rk, err := c.FindShowByTMDBID(id); err != nil || rk != want {
			t.Errorf("FindShowByTMDBID(%s) = %q, %v; want %s", id, rk, err, want)
		}
	}
	if rk, err := c.FindShowByTitle("show c"); err != nil || rk != "88" {
		t.Errorf("FindShowByTitle(show c) = %q, %v; want 88", rk, err)
	}
	if got := listed(); got["1"] != 1 || got["2"] != 1 {
		t.Errorf("listings %v; want each show section listed once", got)
	}
}

// A miss lists each section once more: one the run hasn't listed yet isn't
// listed again by the fresh pass.
func TestHTTPPlexClient_MissListsEachSectionOnce(t *testing.T) {
	srv, listed := listingServer(t, map[string][]string{
		"1": {showsJSON(showJSON("77", "Show A", "129"))},
		"2": {showsJSON(showJSON("88", "Show C", "1399"))},
	})
	c := newHTTPPlexClient(srv.URL, "tok")

	if rk, err := c.FindShowByTMDBID("129"); err != nil || rk != "77" {
		t.Fatalf("FindShowByTMDBID(129) = %q, %v; want 77", rk, err)
	}
	if got := listed(); got["1"] != 1 || got["2"] != 0 {
		t.Fatalf("after a hit in section 1: listings %v; want section 1 once, section 2 never", got)
	}

	if _, err := c.FindShowByTitle("Missing Show"); !errors.Is(err, ErrPlexNotFound) {
		t.Fatalf("missing show: err %v; want ErrPlexNotFound", err)
	}
	if got := listed(); got["1"] != 2 || got["2"] != 1 {
		t.Errorf("after a miss: listings %v; want each section listed once more by the miss", got)
	}
}

// A show Plex added after the run's first listing is found by the fresh pass.
func TestHTTPPlexClient_FreshPassFindsAddedShow(t *testing.T) {
	srv, _ := listingServer(t, map[string][]string{
		"1": {
			showsJSON(showJSON("77", "Show A", "129")),
			showsJSON(showJSON("77", "Show A", "129"), showJSON("78", "Show B", "354912")),
		},
		"2": {showsJSON()},
	})
	c := newHTTPPlexClient(srv.URL, "tok")

	if _, err := c.FindShowByTMDBID("129"); err != nil {
		t.Fatal(err)
	}
	if rk, err := c.FindShowByTitle("Show B"); err != nil || rk != "78" {
		t.Errorf("FindShowByTitle(Show B) = %q, %v; want 78 from the fresh listing", rk, err)
	}
}

// A listing that fails in the fresh pass is an error, not "not found".
func TestHTTPPlexClient_FreshPassListingError(t *testing.T) {
	srv, _ := listingServer(t, map[string][]string{
		"1": {showsJSON(showJSON("77", "Show A", "129")), ""},
		"2": {showsJSON(showJSON("88", "Show C", "1399"))},
	})
	c := newHTTPPlexClient(srv.URL, "tok")

	if _, err := c.FindShowByTMDBID("129"); err != nil {
		t.Fatal(err)
	}
	_, err := c.FindShowByTMDBID("999")
	if err == nil || errors.Is(err, ErrPlexNotFound) || !strings.Contains(err.Error(), "list section 1") {
		t.Errorf("err %v; want the section 1 listing error", err)
	}
}
