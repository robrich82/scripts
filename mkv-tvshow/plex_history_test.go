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
				if e.AccountID == account && !f.isDeleted(e.HistoryKey) {
					mine = append(mine, e)
				}
			}
			end := min(start+size, len(mine))
			start = min(start, end)
			writeJSON(w, map[string]any{"MediaContainer": map[string]any{
				"totalSize": len(mine), "size": end - start, "Metadata": mine[start:end],
			}})
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
	if want := []int{0, historyPageSize, historyPageSize * 2}; fmt.Sprint(f.pageStarts) != fmt.Sprint(want) {
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
	if fmt.Sprint(got) != fmt.Sprint(f.accounts) {
		t.Errorf("Accounts() = %v; want %v", got, f.accounts)
	}
}

func TestHTTPPlexClient_DeleteHistory(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexClient(f.serve().URL, secretToken)
	if err := c.DeleteHistory(historyKeyPrefix + "45"); err != nil {
		t.Fatal(err)
	}
	if want := []string{historyKeyPrefix + "45"}; fmt.Sprint(f.deleted) != fmt.Sprint(want) {
		t.Errorf("deleted %v; want %v", f.deleted, want)
	}
}

// The key comes from the server's response, so anything but a history key
// is refused before a request is sent.
func TestHTTPPlexClient_DeleteHistory_RefusesOtherPaths(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexClient(f.serve().URL, secretToken)
	for _, key := range []string{"/library/metadata/1", historyKeyPrefix, historyKeyPrefix + "1/../../library/sections/1", historyKeyPrefix + "all", ""} {
		if err := c.DeleteHistory(key); err == nil {
			t.Errorf("DeleteHistory(%q) succeeded; want an error", key)
		}
	}
	if len(f.deleted) != 0 {
		t.Errorf("sent DELETEs %v", f.deleted)
	}
}
