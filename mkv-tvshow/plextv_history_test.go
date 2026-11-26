package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	history           []cloudEntry // a removal takes its item out
	pageSize          int          // 0: the size asked for
	staleCursor       bool         // every page's endCursor is "c1"
	emptyCursor       bool         // every page's endCursor is ""
	endless           bool         // every page is empty, with more to come at a new cursor
	historyErr        string       // a GraphQL error for GetWatchHistoryHub
	historyStatus     int          // with historyErr, the HTTP status; 0 is 200
	historyRateLimits int          // GetWatchHistoryHub calls refused with RATE_LIMITED before the rest
	pagesRead         []int        // the start of each page served
	retryAfter        float64      // seconds, in RATE_LIMITED refusals

	removed       []string
	ignoreRemove  bool            // removals reply as asked but leave the history as it was
	keepListed    map[string]bool // these items' removals reply as asked but leave them listed
	failReadAfter bool            // once something is removed, GetWatchHistoryHub fails
	rateLimits    int             // removeActivity calls refused with RATE_LIMITED before the rest
	failRemove    map[string]bool
	removeResult  any             // removeActivity's value
	onRemove      func()          // runs before each removal is answered
	removeHTTP    int             // with failRemove, the HTTP status; 0 is 200
	retryHeader   string          // with failRemove, a Retry-After header
	removeExtra   []graphQLError  // more errors beside failRemove's
	cutRemove     map[string]bool // these items' removals take, but the reply is cut off
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
	body, _ := io.ReadAll(r.Body)
	if r.URL.RawQuery != "" || strings.Contains(string(body), secretToken) {
		f.t.Errorf("the token or a query string went outside the header: %s?%s %s", r.URL.Path, r.URL.RawQuery, body)
	}
	var req struct {
		OperationName string         `json:"operationName"`
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("GraphQL body: %v", err)
		return
	}
	gqlErr := func(status int, msg, code string, retry float64, extra ...graphQLError) {
		errs := []any{map[string]any{"message": msg, "extensions": map[string]any{"code": code, "retryAfter": retry}}}
		for _, e := range extra {
			errs = append(errs, e)
		}
		w.Header().Set("Content-Type", "application/json")
		if f.retryHeader != "" {
			w.Header().Set("Retry-After", f.retryHeader)
		}
		w.WriteHeader(max(status, http.StatusOK))
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs})
	}
	switch req.OperationName {
	case "GetWatchHistoryHub":
		// The fake sends every field, so check the query asks for each one
		// plex.tv would otherwise leave out.
		query := strings.Join(strings.Fields(req.Query), " ")
		for _, field := range []string{
			"watchHistory(first: $first, after: $after)", "nodes { id date metadataItem { id title type year index ",
			"parent { id title year index }", "grandparent { id title year }", "pageInfo { hasNextPage endCursor }",
		} {
			if !strings.Contains(query, field) {
				f.t.Errorf("history query lacks %q: %q", field, req.Query)
			}
		}
		if req.Variables["uuid"] != f.uuid {
			f.t.Errorf("history asked for user %v; want %s", req.Variables["uuid"], f.uuid)
		}
		if f.historyRateLimits > 0 {
			f.historyRateLimits--
			gqlErr(0, "Too many requests", "RATE_LIMITED", f.retryAfter)
			return
		}
		if f.failReadAfter && len(f.removed) > 0 {
			gqlErr(0, "read after removal failed", "", 0)
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
		if len(f.pagesRead) > 1000 { // a paging guard gone: fail, don't hang
			f.t.Error("the history was read past 1000 pages")
			gqlErr(http.StatusInternalServerError, "too many pages", "", 0)
			return
		}
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
		case f.staleCursor:
			cursor = "c1"
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
			if !f.ignoreRemove && !f.keepListed[id] {
				f.history = slices.DeleteFunc(f.history, func(e cloudEntry) bool { return e.ID == id })
			}
			if f.cutRemove[id] {
				cutReply(w, http.StatusOK, `{"data":{"remo`)
				return
			}
			writeJSON(w, map[string]any{"data": map[string]any{"removeActivity": f.removeResult}})
		}
	default:
		f.t.Errorf("unexpected GraphQL operation %q", req.OperationName)
	}
}

// cutReply sends the start of body with status, and closes the connection
// before the rest its Content-Length promises.
func cutReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
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
// as all of it. Each guard stops it at the first page that shows it; the
// pages before each stop added new items, so only that guard could.
func TestReadCloudHistory_StuckPaging(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stick func(*fakePlexTV)
		pages int
	}{
		{"no cursor", func(f *fakePlexTV) { f.emptyCursor = true }, 1},
		{"repeated cursor", func(f *fakePlexTV) { f.staleCursor = true }, 2},
		{"nothing new", func(f *fakePlexTV) { f.endless = true }, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePlexTV(t)
			f.pageSize = 1
			f.history = []cloudEntry{cloudShowItem("a", "A", 0, ""), cloudShowItem("b", "B", 0, ""), cloudShowItem("c", "C", 0, "")}
			tc.stick(f)
			if _, err := readCloudHistory(f.client(), "u-1", noRetry); err == nil || !strings.Contains(err.Error(), "can't be read whole") {
				t.Errorf("err = %v; want a stuck-paging error", err)
			}
			if f.graphHits != tc.pages {
				t.Errorf("%d requests; want %d", f.graphHits, tc.pages)
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

// Replies missing what the run needs stop it, rather than reading as an
// empty history or passing an empty id on to a removal.
func TestCloudHistoryPage_MalformedReplies(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"data":null}`, "no data in the reply"},
		{`{}`, "no data in the reply"},
		{`{"data":{"user":null}}`, "no watch history in the reply"},
		{`{"data":{"user":{"watchHistory":null}}}`, "no watch history in the reply"},
		{`{"data":{"user":{"watchHistory":{"nodes":[{"id":"","date":"","metadataItem":null}],"pageInfo":{}}}}}`, "has no id"},
		{`not json`, "decode GetWatchHistoryHub"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
		_, err := newPlexTVClient(srv.URL, srv.URL, secretToken).CloudHistoryPage("u-1", "")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v; want %q", tc.body, err, tc.want)
		}
	}
}

func TestRetryAfterHeader(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30": 30 * time.Second, " 30 ": 30 * time.Second, "": 0, "-5": 0, "30abc": 0, "1.5": 0,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0, "99999999999999": 24 * time.Hour,
	} {
		if got := retryAfterHeader(in); got != want {
			t.Errorf("retryAfterHeader(%q) = %s; want %s", in, got, want)
		}
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
		{"false", func(f *fakePlexTV) { f.removeResult = false }, "replied false"},
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
			// Only an explicit error is a failure the history can't overrule.
			if want := f.failRemove == nil; isUnconfirmed(err) != want {
				t.Errorf("isUnconfirmed(%v) = %v; want %v", err, !want, want)
			}
		})
	}
}

// A RATE_LIMITED error is a rate limit whatever the status, and whatever
// errors come with it.
func TestRemoveActivity_RateLimited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		after  float64
		http   int
		header string
		want   time.Duration
	}{
		{"with a wait", 42, 0, "", 42 * time.Second},
		{"under a 400", 7, http.StatusBadRequest, "", 7 * time.Second},
		{"no wait", 0, 0, "", 0},
		{"negative wait", -5, 0, "", 0},
		{"past a Duration", 1e300, 0, "", 24 * time.Hour},
		{"wait in the 429's header", 0, http.StatusTooManyRequests, "120", 2 * time.Minute},
		{"body's wait over the header's", 9, http.StatusTooManyRequests, "120", 9 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePlexTV(t)
			f.failRemove, f.removeHTTP, f.retryHeader = map[string]bool{"e1": true}, tc.http, tc.header
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

// A request still rate-limited after maxRateLimitTries says it gave up,
// so the error doesn't read like a single refusal.
func TestWaitOutRateLimit_GivesUp(t *testing.T) {
	tries := 0
	err := waitOutRateLimit(io.Discard, func(time.Duration) {}, func() error { tries++; return &rateLimitError{After: time.Second} })
	var limited *rateLimitError
	if tries != maxRateLimitTries || !errors.As(err, &limited) || !strings.Contains(err.Error(), fmt.Sprintf("gave up after %d tries", maxRateLimitTries)) {
		t.Errorf("%d tries, err = %v; want %d and a gave-up rate limit", tries, err, maxRateLimitTries)
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

// A 2xx removal reply cut off, or that isn't the JSON asked for, is
// unconfirmed, and only the cut-off one counts toward a run of failures. One
// with a failing status is an error named by it, cut off or not.
func TestRemoveActivity_UnreadableReplies(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		cut         bool
		unconfirmed bool
		want        string // in the error
	}{
		{"not json", `not json`, 200, false, true, "decode removeActivity"},
		{"empty object", `{}`, 200, false, true, "no data"},
		{"null data", `{"data":null}`, 200, false, true, "no data"},
		{"500 not json", `not json`, 500, false, false, "500"},
		{"cut off", `{"data":{"remo`, 200, true, true, "cut off"},
		{"500 cut off", `{"errors":[{"mess`, 500, true, false, "status 500 Internal Server Error (reading the reply"},
		{"401 cut off", `{"err`, 401, true, false, "status 401 Unauthorized (reading the reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.cut {
					cutReply(w, tc.status, tc.body)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			err := newPlexTVClient(srv.URL, srv.URL, secretToken).RemoveActivity("e1")
			var u *unconfirmedError
			if err == nil || errors.As(err, &u) != tc.unconfirmed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, unconfirmed %v; want unconfirmed %v, naming %q", err, u != nil, tc.unconfirmed, tc.want)
			}
			if u != nil && u.cut != tc.cut {
				t.Errorf("cut = %v; want %v", u.cut, tc.cut)
			}
		})
	}
}

// A 429 is a rate limit though its body is cut off.
func TestRemoveActivity_HTTP429CutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		cutReply(w, http.StatusTooManyRequests, `{"errors":[`)
	}))
	defer srv.Close()
	var limited *rateLimitError
	if err := newPlexTVClient(srv.URL, srv.URL, secretToken).RemoveActivity("e1"); !errors.As(err, &limited) || limited.After != 30*time.Second {
		t.Errorf("err = %v; want a 30s rate limit", err)
	}
}

// Anything after the reply's JSON is ignored, as plex.tv's replies were read
// before.
func TestRemoveActivity_TrailingData(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"removed", `{"data":{"removeActivity":true}}` + "\n<junk>", ""},
		{"rate limited", `{"errors":[{"message":"slow","extensions":{"code":"RATE_LIMITED","retryAfter":7}}]} junk`, "retry after 7s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			err := newPlexTVClient(srv.URL, srv.URL, secretToken).RemoveActivity("e1")
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
				t.Errorf("err = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestQueryless(t *testing.T) {
	for in, want := range map[string]string{
		"https://plex.tv/api?X-Plex-Token=t": "https://plex.tv/api",
		"https://user:pw@plex.tv/api#frag":   "https://plex.tv/api",
		"/relative/path?t=1":                 "/relative/path",
		"https://plex.tv/%zz?X-Plex-Token=t": "an address that can't be read",
		"https://[::1]:8080/p?t=1":           "https://[::1]:8080/p",
		"":                                   "",
	} {
		if got := queryless(in); got != want {
			t.Errorf("queryless(%q) = %q; want %q", in, got, want)
		}
	}
}
