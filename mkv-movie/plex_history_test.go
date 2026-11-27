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
	onDelete func()          // runs before each DELETE is answered
	// Misbehaviours a real server might have.
	ignoreAccount bool // return every account's history
	omitTotal     bool // leave out totalSize
	totalOver     int  // add this to totalSize
	pageCap       int  // serve at most this many per page

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
			if f.pageCap > 0 {
				size = min(size, f.pageCap)
			}
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
			if f.onDelete != nil {
				f.onDelete()
			}
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

// heatPlays is n of account 1's plays of Heat, keyed 0 to n-1.
func heatPlays(n int) []historyEntry {
	var out []historyEntry
	for i := range n {
		out = append(out, moviePlay(i, 1, "Heat", "1995-12-15"))
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// moviePlay is account's play of title, released on date ("" for none),
// with history id id.
func moviePlay(id, account int, title, date string) historyEntry {
	return historyEntry{
		HistoryKey: fmt.Sprintf("%s%d", historyKeyPrefix, id), Type: "movie",
		Title: title, OriginallyAvailableAt: date, ViewedAt: int64(1700000000 + id), AccountID: account,
	}
}

func TestHTTPPlexMovieClient_WatchHistory_PagesOneAccount(t *testing.T) {
	f := &fakePMS{t: t, history: append(heatPlays(historyPageSize*2+7), moviePlay(9999, 2, "Not mine", ""))}
	c := newHTTPPlexMovieClient(f.serve().URL, secretToken)

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

func TestHTTPPlexMovieClient_WatchHistory_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := newHTTPPlexMovieClient(srv.URL, secretToken).WatchHistory(1); err == nil {
		t.Error("expected an error for a 401")
	}
}

func TestHTTPPlexMovieClient_Accounts(t *testing.T) {
	f := &fakePMS{t: t, accounts: []plexAccount{{ID: 1, Name: "Owner"}, {ID: 5, Name: "Stephanie"}}}
	got, err := newHTTPPlexMovieClient(f.serve().URL, secretToken).Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, f.accounts) {
		t.Errorf("Accounts() = %v; want %v", got, f.accounts)
	}
}

func TestHTTPPlexMovieClient_DeleteHistory(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexMovieClient(f.serve().URL, secretToken)
	if err := c.DeleteHistory(historyKeyPrefix + "45"); err != nil {
		t.Fatal(err)
	}
	if want := []string{historyKeyPrefix + "45"}; !slices.Equal(f.deleted, want) {
		t.Errorf("deleted %v; want %v", f.deleted, want)
	}
}

// The key comes from the server's response, so anything but a history key
// is refused before a request is sent.
func TestHTTPPlexMovieClient_DeleteHistory_RefusesOtherPaths(t *testing.T) {
	f := &fakePMS{t: t}
	c := newHTTPPlexMovieClient(f.serve().URL, secretToken)
	for _, key := range []string{"/library/metadata/1", historyKeyPrefix, historyKeyPrefix + "1/../../library/sections/1", historyKeyPrefix + "all", historyKeyPrefix + "+1", historyKeyPrefix + "1?x=1", ""} {
		if err := c.DeleteHistory(key); err == nil {
			t.Errorf("DeleteHistory(%q) succeeded; want an error", key)
		}
	}
	if len(f.deleted) != 0 {
		t.Errorf("sent DELETEs %v", f.deleted)
	}
}

// Without totalSize, paging goes on until an empty page.
func TestHTTPPlexMovieClient_WatchHistory_NoTotalSize(t *testing.T) {
	f := &fakePMS{t: t, omitTotal: true, history: heatPlays(historyPageSize + 3)}
	got, err := newHTTPPlexMovieClient(f.serve().URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize+3 || len(f.pageStarts) != 3 {
		t.Errorf("got %d entries in %d requests; want %d in 3", len(got), len(f.pageStarts), historyPageSize+3)
	}
}

// A server that sends fewer entries than asked for is paged to totalSize,
// or without it to an empty page.
func TestHTTPPlexMovieClient_WatchHistory_ShortPages(t *testing.T) {
	for _, omitTotal := range []bool{false, true} {
		f := &fakePMS{t: t, pageCap: 100, omitTotal: omitTotal, history: heatPlays(250)}
		got, err := newHTTPPlexMovieClient(f.serve().URL, secretToken).WatchHistory(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 250 {
			t.Errorf("omitTotal %v: got %d entries; want 250", omitTotal, len(got))
		}
	}
}

// A totalSize larger than the history ends at the empty page, not in a loop.
func TestHTTPPlexMovieClient_WatchHistory_OverReportedTotal(t *testing.T) {
	f := &fakePMS{t: t, totalOver: 10000, history: heatPlays(historyPageSize)}
	got, err := newHTTPPlexMovieClient(f.serve().URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize || len(f.pageStarts) != 2 {
		t.Errorf("got %d entries in %d requests; want %d in 2", len(got), len(f.pageStarts), historyPageSize)
	}
}

// A server that serves the same page whatever the offset can't be read in
// full, so the fetch fails rather than pass off the first page as all.
func TestHTTPPlexMovieClient_WatchHistory_IgnoredOffsetFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		page  []historyEntry
		total int
	}{
		{"full page", heatPlays(historyPageSize), 0},
		{"short page under totalSize", heatPlays(100), 250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				container := map[string]any{"Metadata": tc.page}
				if tc.total > 0 {
					container["totalSize"] = tc.total
				}
				writeJSON(w, map[string]any{"MediaContainer": container})
			}))
			defer srv.Close()
			if _, err := newHTTPPlexMovieClient(srv.URL, secretToken).WatchHistory(1); err == nil || !strings.Contains(err.Error(), "can't be read in full") {
				t.Errorf("err = %v; want one saying the history can't be read in full", err)
			}
		})
	}
}

// A play logged between pages shifts the next page; the repeat is dropped.
func TestHTTPPlexMovieClient_WatchHistory_DropsShiftedRepeat(t *testing.T) {
	all := heatPlays(historyPageSize + 2)
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
	got, err := newHTTPPlexMovieClient(srv.URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Errorf("got %d entries; want %d, each once", len(got), len(all))
	}
}

// A malformed key fails the fetch, before any plan or prompt.
func TestHTTPPlexMovieClient_WatchHistory_RejectsBadKey(t *testing.T) {
	f := &fakePMS{t: t, history: []historyEntry{moviePlay(1, 1, "Heat", ""), {Type: "movie", Title: "No key", AccountID: 1}}}
	if _, err := newHTTPPlexMovieClient(f.serve().URL, secretToken).WatchHistory(1); err == nil {
		t.Error("expected an error for an entry with no historyKey")
	}
}

// A DELETE that a redirect would send on as a GET fails, unsent.
func TestHTTPPlexMovieClient_DeleteHistory_RedirectFails(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/moved" {
			http.Redirect(w, r, "/moved", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	err := newHTTPPlexMovieClient(srv.URL, secretToken).DeleteHistory(historyKeyPrefix + "45")
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect that would send DELETE as GET") {
		t.Errorf("err = %v; want a redirect error", err)
	}
	if !slices.Equal(methods, []string{"DELETE " + historyKeyPrefix + "45"}) {
		t.Errorf("requests %v", methods)
	}
}

// A play logged on the last page shifts it so it holds only a repeat: that
// is the end of the history, not an ignored offset.
func TestHTTPPlexMovieClient_WatchHistory_ShiftedTailOfRepeats(t *testing.T) {
	all := heatPlays(historyPageSize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		page := all[:historyPageSize]
		if start > 0 {
			page = all[historyPageSize-1:] // a new play pushed the last one down
		}
		writeJSON(w, map[string]any{"MediaContainer": map[string]any{"totalSize": historyPageSize + 1, "Metadata": page}})
	}))
	defer srv.Close()
	got, err := newHTTPPlexMovieClient(srv.URL, secretToken).WatchHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyPageSize {
		t.Errorf("got %d entries; want %d", len(got), historyPageSize)
	}
}

// A 200 that isn't JSON (a proxy's or captive portal's page) is an error,
// not an empty list.
func TestHistoryClients_NonJSONReplyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>sign in</html>")
	}))
	defer srv.Close()
	pms := newHTTPPlexMovieClient(srv.URL, secretToken)
	if _, err := pms.Accounts(); err == nil {
		t.Error("Accounts: expected an error")
	}
	if _, err := pms.WatchHistory(1); err == nil {
		t.Error("WatchHistory: expected an error")
	}
	if _, err := newPlexTVClient(srv.URL, srv.URL, secretToken).User(); err == nil {
		t.Error("User: expected an error")
	}
}

// User reads the owner from a 2xx JSON reply with a username; anything else
// is an error that says what was wrong.
func TestPlexTVClient_User(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		want    string // the username, or what the error says
		wantErr bool
	}{
		{"owner", http.StatusOK, `{"username":"QuienNZ","uuid":"abc"}`, "QuienNZ", false},
		{"no username", http.StatusOK, `{"uuid":"abc"}`, "no username in the response", true},
		{"not JSON", http.StatusOK, `<html>`, "plex.tv: decode user", true},
		{"server error", http.StatusInternalServerError, `{"username":"QuienNZ"}`, "unexpected status 500", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/user" || r.Header.Get("X-Plex-Token") != secretToken {
				t.Errorf("%s: request %s with token %q", tc.name, r.URL.Path, r.Header.Get("X-Plex-Token"))
			}
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		u, err := newPlexTVClient(srv.URL, srv.URL, secretToken).User()
		srv.Close()
		switch {
		case tc.wantErr && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v; want one saying %q", tc.name, err, tc.want)
		case !tc.wantErr && (err != nil || u.Username != tc.want || u.UUID != "abc"):
			t.Errorf("%s: user %+v, err %v; want %s", tc.name, u, err, tc.want)
		}
	}
}
