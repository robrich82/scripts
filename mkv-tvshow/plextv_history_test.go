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
// a watch history of history, in order, and removeActivity, with switches
// for misbehaviour.
type fakePlexTV struct {
	t          *testing.T
	username   string
	uuid       string
	userStatus int // /api/v2/user's status
	userHits   int
	graphHits  int

	history           []cloudEntry
	pageSize          int     // 0: the size asked for
	repeatCursor      bool    // every page's endCursor is the same
	emptyCursor       bool    // every page's endCursor is ""
	endless           bool    // every page is empty, with more to come at a new cursor
	historyErr        string  // a GraphQL error for GetWatchHistoryHub
	historyStatus     int     // with historyErr, the HTTP status; 0 is 200
	historyRateLimits int     // GetWatchHistoryHub calls refused with RATE_LIMITED before the rest
	pagesRead         []int   // the start of each page served
	retryAfter        float64 // seconds, in RATE_LIMITED refusals

	removed      []string
	rateLimits   int // removeActivity calls refused with RATE_LIMITED before the rest
	failRemove   map[string]bool
	removeResult any            // removeActivity's value
	onRemove     func()         // runs before each removal is answered
	removeHTTP   int            // with failRemove, the HTTP status; 0 is 200
	removeExtra  []graphQLError // more errors beside failRemove's
}

func newFakePlexTV(t *testing.T) *fakePlexTV {
	return &fakePlexTV{t: t, username: "QuienNZ", uuid: "u-1", userStatus: http.StatusOK, retryAfter: 60, removeResult: true}
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
	gqlErr := func(status int, msg, code string, retry float64, extra ...graphQLError) {
		errs := []any{map[string]any{"message": msg, "extensions": map[string]any{"code": code, "retryAfter": retry}}}
		for _, e := range extra {
			errs = append(errs, e)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(max(status, http.StatusOK))
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs})
	}
	switch req.OperationName {
	case "GetWatchHistoryHub":
		if !strings.Contains(req.Query, "watchHistory(first: $first, after: $after)") || !strings.Contains(req.Query, "grandparent { id title year }") {
			f.t.Errorf("history query %q", req.Query)
		}
		if req.Variables["uuid"] != f.uuid {
			f.t.Errorf("history asked for user %v; want %s", req.Variables["uuid"], f.uuid)
		}
		if f.historyRateLimits > 0 {
			f.historyRateLimits--
			gqlErr(0, "Too many requests", "RATE_LIMITED", f.retryAfter)
			return
		}
		if f.historyErr != "" {
			gqlErr(f.historyStatus, f.historyErr, "", 0)
			return
		}
		start := 0
		if after, ok := req.Variables["after"].(string); ok {
			n, err := strconv.Atoi(strings.TrimPrefix(after, "c"))
			if err != nil {
				f.t.Errorf("cursor %q", after)
			}
			start = n
		} else if req.Variables["after"] != nil {
			f.t.Errorf("after = %v; want null or a cursor", req.Variables["after"])
		}
		f.pagesRead = append(f.pagesRead, start)
		size := int(req.Variables["first"].(float64))
		if f.pageSize > 0 {
			size = f.pageSize
		}
		end := min(start+size, len(f.history))
		nodes := []any{}
		for _, e := range f.history[min(start, end):end] {
			nodes = append(nodes, cloudNode(e))
		}
		cursor, hasNext := "c"+strconv.Itoa(end), end < len(f.history)
		switch {
		case f.repeatCursor:
			cursor = "c0"
		case f.emptyCursor:
			cursor = ""
		case f.endless:
			cursor, hasNext = "c"+strconv.Itoa(start+size), true
		}
		writeJSON(w, map[string]any{"data": map[string]any{"user": map[string]any{"watchHistory": map[string]any{
			"nodes":    nodes,
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
		}}}})
	case "removeActivity":
		input, _ := req.Variables["input"].(map[string]any)
		id, _ := input["id"].(string)
		if input["type"] != "WATCH_HISTORY" || !strings.Contains(req.Query, "removeActivity(input: $input)") {
			f.t.Errorf("removeActivity input %v, query %q", input, req.Query)
		}
		if f.onRemove != nil {
			f.onRemove()
		}
		switch {
		case f.rateLimits > 0:
			f.rateLimits--
			gqlErr(0, "Too many requests", "RATE_LIMITED", f.retryAfter)
		case f.failRemove[id]:
			gqlErr(f.removeHTTP, "boom", "", 0, f.removeExtra...)
		default:
			f.removed = append(f.removed, id)
			writeJSON(w, map[string]any{"data": map[string]any{"removeActivity": f.removeResult}})
		}
	default:
		f.t.Errorf("unexpected GraphQL operation %q", req.OperationName)
	}
}

// cloudNode is the watchHistory node plex.tv sends for e.
func cloudNode(e cloudEntry) map[string]any {
	item := map[string]any{"id": "m-" + e.ID, "title": e.Title, "type": e.Type}
	switch e.Type {
	case cloudTypeEpisode:
		item["index"] = e.Episode
		item["year"] = e.Year + 1 // an episode's own year isn't the show's
		item["parent"] = map[string]any{"id": "p-" + e.ID, "title": fmt.Sprintf("Season %d", e.Season), "year": e.Year + 1, "index": e.Season}
		item["grandparent"] = map[string]any{"id": e.ShowID, "title": e.Show, "year": nullIfZero(e.Year)}
	case cloudTypeSeason:
		item["index"] = e.Season
		item["parent"] = map[string]any{"id": e.ShowID, "title": e.Show, "year": nullIfZero(e.Year)}
	case cloudTypeShow:
		item["id"] = e.ShowID
		item["year"] = nullIfZero(e.Year)
	default:
		item["year"] = nullIfZero(e.Year)
		item["parent"] = nil
		item["grandparent"] = nil
	}
	return map[string]any{"id": e.ID, "date": e.Date, "metadataItem": item}
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// showID is the plex.tv id the test helpers give a show.
func showID(show string, year int) string { return fmt.Sprintf("sh-%s-%d", show, year) }

func cloudEpisode(id, show string, year, season, ep int, title, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: cloudTypeEpisode, Title: title, ShowID: showID(show, year), Show: show, Year: year, Season: season, Episode: ep}
}

func cloudSeason(id, show string, year, season int, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: cloudTypeSeason, Title: fmt.Sprintf("Season %d", season), ShowID: showID(show, year), Show: show, Year: year, Season: season}
}

func cloudShowItem(id, show string, year int, date string) cloudEntry {
	return cloudEntry{ID: id, Date: date, Type: cloudTypeShow, Title: show, ShowID: showID(show, year), Show: show, Year: year}
}

func (f *fakePlexTV) client() *plexTVClient {
	srv := f.serve()
	return newPlexTVClient(srv.URL, srv.URL, secretToken)
}

func noRetry(f func() error) error { return f() }

// Every page is read, and each item names its show, by id, title and year.
func TestReadCloudHistory_PagesAndShows(t *testing.T) {
	f := newFakePlexTV(t)
	f.pageSize = 2
	f.history = []cloudEntry{
		cloudEpisode("e1", "Bluey", 2018, 2, 5, "Hospital", "2026-11-20T10:00:00.000Z"),
		cloudSeason("s1", "Bluey", 2018, 2, "2026-11-19T10:00:00.000Z"),
		cloudShowItem("sh1", "Bluey", 2018, "2026-11-18T10:00:00.000Z"),
		{ID: "m1", Date: "2026-11-17T10:00:00.000Z", Type: "MOVIE", Title: "Bluey"},
		cloudShowItem("sh2", "Peppa Pig", 0, "2026-11-16T10:00:00.000Z"),
	}
	got, err := readCloudHistory(f.client(), "u-1", noRetry)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, f.history) {
		t.Errorf("history\n%+v\nwant\n%+v", got, f.history)
	}
	if !slices.Equal(f.pagesRead, []int{0, 2, 4}) {
		t.Errorf("read pages at %v; want 0, 2, 4", f.pagesRead)
	}
}

// An item on two pages, as when a new watch pushes the history along while
// it's read, is kept once.
func TestReadCloudHistory_DuplicateIDs(t *testing.T) {
	f := newFakePlexTV(t)
	f.pageSize = 2
	a, b, c := cloudShowItem("a", "A", 2001, ""), cloudShowItem("b", "B", 2002, ""), cloudShowItem("c", "C", 2003, "")
	f.history = []cloudEntry{a, b, b, c}
	got, err := readCloudHistory(f.client(), "u-1", noRetry)
	if err != nil {
		t.Fatal(err)
	}
	if want := []cloudEntry{a, b, c}; !slices.Equal(got, want) {
		t.Errorf("history %+v; want %+v", got, want)
	}
}

// Paging that can't move on would loop forever, or read part of the history
// as all of it.
func TestReadCloudHistory_StuckPaging(t *testing.T) {
	for name, stick := range map[string]func(*fakePlexTV){
		"repeated cursor": func(f *fakePlexTV) { f.repeatCursor = true },
		"no cursor":       func(f *fakePlexTV) { f.emptyCursor = true },
		"nothing new":     func(f *fakePlexTV) { f.endless = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakePlexTV(t)
			f.pageSize = 1
			f.history = []cloudEntry{cloudShowItem("a", "A", 0, ""), cloudShowItem("b", "B", 0, ""), cloudShowItem("c", "C", 0, "")}
			stick(f)
			if _, err := readCloudHistory(f.client(), "u-1", noRetry); err == nil || !strings.Contains(err.Error(), "can't be read whole") {
				t.Errorf("err = %v; want a stuck-paging error", err)
			}
			if f.graphHits > len(f.history)+1 {
				t.Errorf("%d requests; want it to stop at the first page that doesn't move on", f.graphHits)
			}
		})
	}
}

// Each page's request goes through retry, so a rate-limited page is read
// again rather than failing the run.
func TestReadCloudHistory_RetriesEachPage(t *testing.T) {
	f := newFakePlexTV(t)
	f.pageSize, f.historyRateLimits = 1, 1
	f.history = []cloudEntry{cloudShowItem("a", "A", 0, ""), cloudShowItem("b", "B", 0, "")}
	var slept []time.Duration
	retry := func(fn func() error) error {
		return waitOutRateLimit(&strings.Builder{}, func(d time.Duration) { slept = append(slept, d) }, fn)
	}
	got, err := readCloudHistory(f.client(), "u-1", retry)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(slept, []time.Duration{time.Minute}) {
		t.Errorf("read %d items after waiting %v; want 2 after one minute", len(got), slept)
	}
}

func TestCloudHistoryPage_GraphQLError(t *testing.T) {
	f := newFakePlexTV(t)
	f.historyErr = "user not found"
	if _, err := f.client().CloudHistoryPage("u-1", ""); err == nil || !strings.Contains(err.Error(), "user not found") {
		t.Errorf("err = %v; want plex.tv's error", err)
	}
}

// A reply with another status than 2xx keeps the GraphQL errors in its body.
func TestCloudHistoryPage_StatusKeepsErrors(t *testing.T) {
	f := newFakePlexTV(t)
	f.historyErr, f.historyStatus = "Variable $first is invalid", http.StatusBadRequest
	_, err := f.client().CloudHistoryPage("u-1", "")
	var status *plexTVStatusError
	if !errors.As(err, &status) || status.Code != http.StatusBadRequest || !strings.Contains(err.Error(), "Variable $first is invalid") {
		t.Errorf("err = %v; want the 400 with plex.tv's message", err)
	}
}

func TestCloudHistoryPage_NoUUID(t *testing.T) {
	if _, err := newPlexTVClient(closedServerURL(), closedServerURL(), secretToken).CloudHistoryPage("", ""); err == nil {
		t.Error("read a history with no user id")
	}
}

// Only a reply of true is a removal: an error, false, null, a missing field
// or another shape is a failure.
func TestRemoveActivity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     func(*fakePlexTV)
		wantErr string
	}{
		{"true", func(*fakePlexTV) {}, ""},
		{"false", func(f *fakePlexTV) { f.removeResult = false }, "didn't remove it"},
		{"null", func(f *fakePlexTV) { f.removeResult = nil }, "doesn't say it was removed"},
		{"object", func(f *fakePlexTV) { f.removeResult = map[string]any{} }, "decode removeActivity"},
		{"error", func(f *fakePlexTV) { f.failRemove = map[string]bool{"e1": true} }, "removeActivity: boom"},
		{"error with 500", func(f *fakePlexTV) {
			f.failRemove, f.removeHTTP = map[string]bool{"e1": true}, http.StatusInternalServerError
		}, "unexpected status 500 Internal Server Error: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePlexTV(t)
			tc.set(f)
			err := f.client().RemoveActivity("e1")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v; want %q", err, tc.wantErr)
			}
		})
	}
}

// A RATE_LIMITED error is a rate limit whatever the status, and whatever
// errors come with it.
func TestRemoveActivity_RateLimited(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after float64
		http  int
		want  time.Duration
	}{
		{"with a wait", 42, 0, 42 * time.Second},
		{"under a 400", 7, http.StatusBadRequest, 7 * time.Second},
		{"no wait", 0, 0, 0},
		{"past a Duration", 1e300, 0, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePlexTV(t)
			f.failRemove, f.removeHTTP = map[string]bool{"e1": true}, tc.http
			var limit graphQLError
			limit.Extensions.Code, limit.Extensions.RetryAfter = "RATE_LIMITED", tc.after
			f.removeExtra = []graphQLError{limit}
			var limited *rateLimitError
			err := f.client().RemoveActivity("e1")
			if !errors.As(err, &limited) || limited.After != tc.want {
				t.Errorf("err = %v; want a %s rate limit", err, tc.want)
			}
		})
	}
}

func TestRateLimitError_NoWait(t *testing.T) {
	if got := (&rateLimitError{}).Error(); strings.Contains(got, "0s") || !strings.Contains(got, "no wait given") {
		t.Errorf("Error() = %q; want it to say no wait was given", got)
	}
}

// An HTTP 429 is a rate limit too, with its wait in Retry-After.
func TestRemoveActivity_HTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := newPlexTVClient(srv.URL, srv.URL, secretToken)
	var limited *rateLimitError
	if err := c.RemoveActivity("e1"); !errors.As(err, &limited) || limited.After != 30*time.Second {
		t.Errorf("err = %v; want a 30s rate limit", err)
	}
}
