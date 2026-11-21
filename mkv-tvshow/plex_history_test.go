package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakePMS is a Plex server with accounts and watch history. It pages
// history as Plex does and records every DELETE.
type fakePMS struct {
	t        *testing.T
	accounts []plexAccount
	history  []historyEntry
	failKeys map[string]bool // DELETEs of these keys get a 500
	// Misbehaviours a real server might have.
	ignoreAccount bool // return every account's history
	omitTotal     bool // leave out totalSize
	totalOver     int  // add this to totalSize

	mu         sync.Mutex
	deleted    []string
	pageStarts []int
}

func (f *fakePMS) serve() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != secretToken {
			f.t.Errorf("%s %s: X-Plex-Token header = %q", r.Method, r.URL.Path, r.Header.Get("X-Plex-Token"))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts":
			writeJSON(w, map[string]any{"MediaContainer": map[string]any{"Account": f.accounts}})
		case r.Method == http.MethodGet && r.URL.Path == "/status/sessions/history/all":
			q := r.URL.Query()
			account, _ := strconv.Atoi(q.Get("accountID"))
			start, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
			size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
			f.pageStarts = append(f.pageStarts, start)
			var mine []historyEntry
			for _, e := range f.history {
				if (f.ignoreAccount || e.AccountID == account) && !f.isDeleted(e.HistoryKey) {
					mine = append(mine, e)
				}
			}
			end := min(start+size, len(mine))
			start = min(start, end)
			container := map[string]any{"size": end - start, "Metadata": mine[start:end]}
			if !f.omitTotal {
				container["totalSize"] = len(mine) + f.totalOver
			}
			writeJSON(w, map[string]any{"MediaContainer": container})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, historyKeyPrefix):
			if f.failKeys[r.URL.Path] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.deleted = append(f.deleted, r.URL.Path)
		default:
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakePMS) isDeleted(key string) bool { return slices.Contains(f.deleted, key) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func episode(id, account int, show, title string) historyEntry {
	return historyEntry{
		HistoryKey: fmt.Sprintf("%s%d", historyKeyPrefix, id), Type: "episode",
		Title: title, GrandparentTitle: show, ParentIndex: 1, Index: id, AccountID: account,
	}
}

func TestHTTPPlexClient_WatchHistory_PagesOneAccount(t *testing.T) {
	f := &fakePMS{t: t}
	for i := range historyPageSize*2 + 7 {
		f.history = append(f.history, episode(i, 1, "Bluey", "Ep"))
	}
	f.history = append(f.history, episode(9999, 2, "Not mine", "Ep"))
	c := newHTTPPlexClient(f.serve().URL, secretToken)

	got, err := c.WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize*2+7 {
		t.Errorf("got %d entries; want %d", len(got), historyPageSize*2+7)
	}
	for _, e := range got {
		if e.AccountID != 1 {
			t.Fatalf("got account %d's entry %s", e.AccountID, e.HistoryKey)
		}
	}
	if want := []int{0, historyPageSize, historyPageSize * 2}; !slices.Equal(f.pageStarts, want) {
		t.Errorf("page starts %v; want %v", f.pageStarts, want)
	}
}

func TestHTTPPlexClient_WatchHistory_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := newHTTPPlexClient(srv.URL, secretToken).WatchHistory(1); err == nil {
		t.Error("expected an error for a 401")
	}
}

func TestHTTPPlexClient_Accounts(t *testing.T) {
	f := &fakePMS{t: t, accounts: []plexAccount{{ID: 1, Name: "Owner"}, {ID: 5, Name: "Stephanie"}}}
	got, err := newHTTPPlexClient(f.serve().URL, secretToken).Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, f.accounts) {
		t.Errorf("Accounts() = %v; want %v", got, f.accounts)
	}
}

func TestHTTPPlexClient_DeleteHistory(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexClient(f.serve().URL, secretToken)
	if err := c.DeleteHistory(historyKeyPrefix + "45"); err != nil {
		t.Fatal(err)
	}
	if want := []string{historyKeyPrefix + "45"}; !slices.Equal(f.deleted, want) {
		t.Errorf("deleted %v; want %v", f.deleted, want)
	}
}

// The key comes from the server's response, so anything but a history key
// is refused before a request is sent.
func TestHTTPPlexClient_DeleteHistory_RefusesOtherPaths(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexClient(f.serve().URL, secretToken)
	for _, key := range []string{"/library/metadata/1", historyKeyPrefix, historyKeyPrefix + "1/../../library/sections/1", historyKeyPrefix + "all", historyKeyPrefix + "+1", historyKeyPrefix + "1?x=1", ""} {
		if err := c.DeleteHistory(key); err == nil {
			t.Errorf("DeleteHistory(%q) succeeded; want an error", key)
		}
	}
	if len(f.deleted) != 0 {
		t.Errorf("sent DELETEs %v", f.deleted)
	}
}

// Without totalSize, paging goes on until a short page.
func TestHTTPPlexClient_WatchHistory_NoTotalSize(t *testing.T) {
	f := &fakePMS{t: t, omitTotal: true}
	for i := range historyPageSize + 3 {
		f.history = append(f.history, episode(i, 1, "Bluey", "Ep"))
	}
	got, err := newHTTPPlexClient(f.serve().URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize+3 {
		t.Errorf("got %d entries; want %d", len(got), historyPageSize+3)
	}
}

// A totalSize larger than the history ends at the short page, not in a loop.
func TestHTTPPlexClient_WatchHistory_OverReportedTotal(t *testing.T) {
	f := &fakePMS{t: t, totalOver: 10000}
	for i := range historyPageSize {
		f.history = append(f.history, episode(i, 1, "Bluey", "Ep"))
	}
	got, err := newHTTPPlexClient(f.serve().URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize || len(f.pageStarts) != 2 {
		t.Errorf("got %d entries in %d requests; want %d in 2", len(got), len(f.pageStarts), historyPageSize)
	}
}

// A server that serves the same full page whatever the offset stops after
// the page that adds nothing new.
func TestHTTPPlexClient_WatchHistory_RepeatedPageStops(t *testing.T) {
	var page []historyEntry
	for i := range historyPageSize {
		page = append(page, episode(i, 1, "Bluey", "Ep"))
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(w, map[string]any{"MediaContainer": map[string]any{"Metadata": page}})
	}))
	defer srv.Close()
	got, err := newHTTPPlexClient(srv.URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize || requests != 2 {
		t.Errorf("got %d entries in %d requests; want %d in 2", len(got), requests, historyPageSize)
	}
}

// A play logged between pages shifts the next page; the repeat is dropped.
func TestHTTPPlexClient_WatchHistory_DropsShiftedRepeat(t *testing.T) {
	var all []historyEntry
	for i := range historyPageSize + 2 {
		all = append(all, episode(i, 1, "Bluey", "Ep"))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		if start > 0 {
			start-- // a new play pushed everything down by one
		}
		writeJSON(w, map[string]any{"MediaContainer": map[string]any{
			"totalSize": len(all), "Metadata": all[start:min(start+historyPageSize, len(all))],
		}})
	}))
	defer srv.Close()
	got, err := newHTTPPlexClient(srv.URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Errorf("got %d entries; want %d, each once", len(got), len(all))
	}
}

// A malformed key fails the fetch, before any plan or prompt.
func TestHTTPPlexClient_WatchHistory_RejectsBadKey(t *testing.T) {
	f := &fakePMS{t: t, history: []historyEntry{episode(1, 1, "Bluey", "Ep"), {Type: "episode", Title: "No key", AccountID: 1}}}
	if _, err := newHTTPPlexClient(f.serve().URL, secretToken).WatchHistory(1); err == nil {
		t.Error("expected an error for an entry with no historyKey")
	}
}
