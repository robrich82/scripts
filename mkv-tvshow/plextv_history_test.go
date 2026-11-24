package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePlexTV serves plex.tv's /api/v2/user and its community GraphQL API:
// a watch history of history (newest first) and removeActivity, with
// switches for misbehaviour.
type fakePlexTV struct {
	t          *testing.T
	username   string
	uuid       string
	userStatus int // /api/v2/user's status
	userHits   int
	graphHits  int

	history      []cloudEntry
	pageSize     int    // 0: the size asked for
	repeatCursor bool   // every page's endCursor is the same
	historyErr   string // a GraphQL error for GetWatchHistoryHub

	removed      []string
	rateLimits   int     // removeActivity calls refused with RATE_LIMITED before the rest
	retryAfter   float64 // seconds, in those refusals
	failRemove   map[string]bool
	removeResult any // removeActivity's value; nil means true
}

func newFakePlexTV(t *testing.T) *fakePlexTV {
	return &fakePlexTV{t: t, username: "QuienNZ", uuid: "u-1", userStatus: http.StatusOK, retryAfter: 60}
}

func (f *fakePlexTV) serve() *httptest.Server {
	f.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != secretToken || r.Header.Get("X-Plex-Client-Identifier") == "" {
			f.t.Errorf("plex.tv request headers: token %q, client id %q", r.Header.Get("X-Plex-Token"), r.Header.Get("X-Plex-Client-Identifier"))
		}
		switch r.URL.Path {
		case "/api/v2/user":
			f.userHits++
			if f.userStatus != http.StatusOK {
				w.WriteHeader(f.userStatus)
				return
			}
			writeJSON(w, map[string]any{"username": f.username, "uuid": f.uuid, "authToken": "not-for-logs"})
		case "/api":
			f.graphHits++
			f.graphQL(w, r)
		default:
			f.t.Errorf("unexpected plex.tv request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakePlexTV) graphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		f.t.Errorf("GraphQL request %s with content type %q", r.Method, r.Header.Get("Content-Type"))
	}
	var req struct {
		OperationName string         `json:"operationName"`
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("GraphQL body: %v", err)
		return
	}
	gqlErr := func(msg, code string, retry float64) {
		writeJSON(w, map[string]any{"errors": []any{map[string]any{"message": msg, "extensions": map[string]any{"code": code, "retryAfter": retry}}}})
	}
	switch req.OperationName {
	case "GetWatchHistoryHub":
		if !strings.Contains(req.Query, "watchHistory(first: $first, after: $after)") {
			f.t.Errorf("history query %q", req.Query)
		}
		if req.Variables["uuid"] != f.uuid {
			f.t.Errorf("history asked for user %v; want %s", req.Variables["uuid"], f.uuid)
		}
		if f.historyErr != "" {
			gqlErr(f.historyErr, "", 0)
			return
		}
		start := 0
		if after, ok := req.Variables["after"].(string); ok {
			n, err := strconv.Atoi(strings.TrimPrefix(after, "c"))
			if err != nil {
				f.t.Errorf("cursor %q", after)
			}
			start = n
		}
		size := int(req.Variables["first"].(float64))
		if f.pageSize > 0 {
			size = f.pageSize
		}
		end := min(start+size, len(f.history))
		var nodes []any
		for _, e := range f.history[start:end] {
			nodes = append(nodes, cloudNode(e))
		}
		cursor := "c" + strconv.Itoa(end)
		if f.repeatCursor {
			cursor = "c0"
		}
		writeJSON(w, map[string]any{"data": map[string]any{"user": map[string]any{"watchHistory": map[string]any{
			"nodes":    nodes,
			"pageInfo": map[string]any{"hasNextPage": end < len(f.history), "endCursor": cursor},
		}}}})
	case "removeActivity":
		input, _ := req.Variables["input"].(map[string]any)
		id, _ := input["id"].(string)
		if input["type"] != "WATCH_HISTORY" || !strings.Contains(req.Query, "removeActivity(input: $input)") {
			f.t.Errorf("removeActivity input %v, query %q", input, req.Query)
		}
		switch {
		case f.rateLimits > 0:
			f.rateLimits--
			gqlErr("Too many requests", "RATE_LIMITED", f.retryAfter)
		case f.failRemove[id]:
			gqlErr("boom", "", 0)
		default:
			f.removed = append(f.removed, id)
			result := f.removeResult
			if result == nil {
				result = true
			}
			writeJSON(w, map[string]any{"data": map[string]any{"removeActivity": result}})
		}
	default:
		f.t.Errorf("unexpected GraphQL operation %q", req.OperationName)
	}
}

// cloudNode is the watchHistory node plex.tv sends for e.
func cloudNode(e cloudEntry) map[string]any {
	item := map[string]any{"id": "m-" + e.ID, "title": e.Title, "type": e.Type}
	switch e.Type {
	case "EPISODE":
		item["index"] = e.Episode
		item["year"] = e.Year + 1 // an episode's own year isn't the show's
		item["parent"] = map[string]any{"title": fmt.Sprintf("Season %d", e.Season), "year": e.Year + 1, "index": e.Season}
		item["grandparent"] = map[string]any{"title": e.Show, "year": nullIfZero(e.Year)}
	case "SEASON":
		item["index"] = e.Season
		item["parent"] = map[string]any{"title": e.Show, "year": nullIfZero(e.Year)}
	default:
		item["year"] = nullIfZero(e.Year)
	}
	return map[string]any{"id": e.ID, "date": e.Date, "metadataItem": item}
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func cloudEpisode(id, show string, year, season, ep int, title, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: "EPISODE", Title: title, Show: show, Year: year, Season: season, Episode: ep}
}

func cloudSeason(id, show string, year, season int, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: "SEASON", Title: fmt.Sprintf("Season %d", season), Show: show, Year: year, Season: season}
}

func cloudShowItem(id, show string, year int, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: "SHOW", Title: show, Show: show, Year: year}
}

func (f *fakePlexTV) client() *plexTVClient {
	srv := f.serve()
	c := newPlexTVClient(srv.URL, secretToken)
	c.communityURL = srv.URL
	return c
}

// Every page is read, and each item names its show and year.
func TestCloudHistory_PagesAndShows(t *testing.T) {
	f := newFakePlexTV(t)
	f.pageSize = 2
	f.history = []cloudEntry{
		cloudEpisode("e1", "Bluey", 2018, 2, 5, "Hospital", "2026-11-20T10:00:00.000Z"),
		cloudSeason("s1", "Bluey", 2018, 2, "2026-11-19T10:00:00.000Z"),
		cloudShowItem("sh1", "Bluey", 2018, "2026-11-18T10:00:00.000Z"),
		{ID: "m1", Date: "2026-11-17T10:00:00.000Z", Type: "MOVIE", Title: "Bluey"},
		cloudShowItem("sh2", "Peppa Pig", 0, "2026-11-16T10:00:00.000Z"),
	}
	got, err := f.client().CloudHistory("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, f.history) {
		t.Errorf("history\n%+v\nwant\n%+v", got, f.history)
	}
	if f.graphHits != 3 {
		t.Errorf("%d requests; want 3 pages", f.graphHits)
	}
}

// A next page that can't move forward would loop forever or read part of
// the history as all of it.
func TestCloudHistory_RepeatedCursor(t *testing.T) {
	f := newFakePlexTV(t)
	f.pageSize, f.repeatCursor = 1, true
	f.history = []cloudEntry{cloudShowItem("a", "A", 0, ""), cloudShowItem("b", "B", 0, ""), cloudShowItem("c", "C", 0, "")}
	if _, err := f.client().CloudHistory("u-1"); err == nil || !strings.Contains(err.Error(), "can't be read whole") {
		t.Errorf("err = %v; want a stuck-paging error", err)
	}
}

func TestCloudHistory_GraphQLError(t *testing.T) {
	f := newFakePlexTV(t)
	f.historyErr = "user not found"
	if _, err := f.client().CloudHistory("u-1"); err == nil || !strings.Contains(err.Error(), "user not found") {
		t.Errorf("err = %v; want plex.tv's error", err)
	}
}

func TestCloudHistory_NoUUID(t *testing.T) {
	if _, err := newPlexTVClient("http://127.0.0.1:1", secretToken).CloudHistory(""); err == nil {
		t.Error("read a history with no user id")
	}
}

func TestRemoveActivity(t *testing.T) {
	f := newFakePlexTV(t)
	c := f.client()
	if err := c.RemoveActivity("e1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.removed, []string{"e1"}) {
		t.Errorf("removed %v", f.removed)
	}

	f.removeResult = false
	if err := c.RemoveActivity("e2"); err == nil || !strings.Contains(err.Error(), "didn't remove it") {
		t.Errorf("err = %v; want a not-removed error", err)
	}

	f.rateLimits, f.retryAfter = 1, 42
	var limited *rateLimitError
	if err := c.RemoveActivity("e3"); !errors.As(err, &limited) || limited.After != 42*time.Second {
		t.Errorf("err = %v; want a 42s rate limit", err)
	}

	f.failRemove = map[string]bool{"e4": true}
	if err := c.RemoveActivity("e4"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v; want plex.tv's error", err)
	}
}

// An HTTP 429 is a rate limit too, with its wait in Retry-After.
func TestRemoveActivity_HTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := newPlexTVClient(srv.URL, secretToken)
	c.communityURL = srv.URL
	var limited *rateLimitError
	if err := c.RemoveActivity("e1"); !errors.As(err, &limited) || limited.After != 30*time.Second {
		t.Errorf("err = %v; want a 30s rate limit", err)
	}
}
