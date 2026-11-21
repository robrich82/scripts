package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePlexTV serves plex.tv's /api/v2/user for username, and counts requests.
func fakePlexTV(t *testing.T, username string, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.URL.Path != "/api/v2/user" {
			t.Errorf("unexpected plex.tv request %s", r.URL)
		}
		if r.Header.Get("X-Plex-Token") != secretToken || r.Header.Get("X-Plex-Client-Identifier") == "" {
			t.Errorf("plex.tv request headers: token %q, client id %q", r.Header.Get("X-Plex-Token"), r.Header.Get("X-Plex-Client-Identifier"))
		}
		writeJSON(w, map[string]any{"username": username, "uuid": "u-1", "authToken": "not-for-logs"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type clearHistoryRun struct {
	pms        *fakePMS
	plexTVHits int
	prompter   *scriptedPrompter
	recordDir  string
	out        bytes.Buffer
}

// newClearHistoryRun has an owner "QuienNZ" (id 1) and "Stephanie" (id 5),
// each with a watch history, behind real HTTP clients.
func newClearHistoryRun(t *testing.T, answers ...string) *clearHistoryRun {
	t.Helper()
	return &clearHistoryRun{
		pms: &fakePMS{
			t:        t,
			accounts: []plexAccount{{ID: 1, Name: "QuienNZ"}, {ID: 5, Name: "Stephanie"}, {ID: 6, Name: ""}},
			history: []historyEntry{
				episode(1, 1, "Bluey (2018)", "Magic Xylophone"),
				episode(2, 1, "Bluey (2018)", "Hospital"),
				episode(3, 1, "Peppa Pig", "Muddy Puddles"),
				episode(4, 1, "NCIS: Hawaiʻi", "Pilot"),
				episode(5, 1, "NCIS: Los Angeles", "Identity"),
				{HistoryKey: historyKeyPrefix + "6", Type: "movie", Title: "Bluey", AccountID: 1},
				{HistoryKey: historyKeyPrefix + "7", Type: "track", Title: "Bluey", GrandparentTitle: "Bluey", AccountID: 1},
				episode(8, 5, "Chicago Fire", "Pilot"),
				episode(9, 5, "Bluey (2018)", "Keepy Uppy"),
			},
		},
		prompter:  &scriptedPrompter{answers: answers},
		recordDir: filepath.Join(t.TempDir(), "mkv-tvshow"),
	}
}

func (r *clearHistoryRun) run(t *testing.T, account string, shows ...string) error {
	t.Helper()
	return clearHistory(shows, account, clearHistoryDeps{
		server:    newHTTPPlexClient(r.pms.serve().URL, secretToken),
		owner:     newPlexTVClient(fakePlexTV(t, "QuienNZ", &r.plexTVHits).URL, secretToken),
		prompt:    r.prompter,
		recordDir: r.recordDir,
		out:       &r.out,
		now:       func() time.Time { return time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC) },
	})
}

func (r *clearHistoryRun) records(t *testing.T) []historyRecord {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.recordDir, "history-cleared-*.json"))
	var out []historyRecord
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var rec historyRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

func keysOf(entries []historyEntry) []string {
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.HistoryKey)
	}
	return keys
}

func TestSplitTitleYear(t *testing.T) {
	for in, want := range map[string]struct {
		title string
		year  int
	}{
		"Bluey (2018)":        {"Bluey", 2018},
		"Bluey":               {"Bluey", 0},
		" Hope Valley: 1874 ": {"Hope Valley: 1874", 0},
		"1883 (2021)":         {"1883", 2021},
		"Show (Extended)":     {"Show (Extended)", 0},
	} {
		title, year := splitTitleYear(in)
		if title != want.title || year != want.year {
			t.Errorf("splitTitleYear(%q) = %q, %d; want %q, %d", in, title, year, want.title, want.year)
		}
	}
}

func TestHistoryTarget_MatchesShow(t *testing.T) {
	for _, tc := range []struct {
		target, show string
		want         bool
	}{
		{"Bluey (2018)", "Bluey (2018)", true},
		{"Bluey (2018)", "Bluey", true},
		{"Bluey", "Bluey (2018)", true},
		{"Bluey (2018)", "Bluey (1992)", false},
		{"Bluey", "Bluey Minisodes", false},
		{"NCIS: Hawaiʻi (2021)", "NCIS: Hawai'i", true},
		{"NCIS", "NCIS: Los Angeles", false},
		{"進撃の巨人", "進撃の巨人", true},
	} {
		targets, err := parseHistoryTargets([]string{tc.target})
		if err != nil {
			t.Fatal(err)
		}
		if got := targets[0].matchesShow(tc.show); got != tc.want {
			t.Errorf("%q matches %q = %v; want %v", tc.target, tc.show, got, tc.want)
		}
	}
}

func TestParseHistoryTargets_RejectsBlank(t *testing.T) {
	for _, s := range []string{"", "  ", "(2018)"} {
		if _, err := parseHistoryTargets([]string{s}); err == nil {
			t.Errorf("parseHistoryTargets(%q) succeeded; want an error", s)
		}
	}
}

func TestClearHistory_Declined_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v after a no", r.pms.deleted)
	}
	if recs := r.records(t); len(recs) != 0 {
		t.Errorf("wrote %d record files after a no", len(recs))
	}
	if !strings.Contains(r.out.String(), "Aborted; no changes made.") {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// Only the owner's episode entries of the named shows go: not the movie or
// track with the same title, not other shows, and not Stephanie's.
func TestClearHistory_Confirmed_DeletesOnlyMatches(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Bluey (2018)", "NCIS: Hawai'i (2021)", "Chicago Fire (2012)"); err != nil {
		t.Fatal(err)
	}
	want := []string{historyKeyPrefix + "1", historyKeyPrefix + "2", historyKeyPrefix + "4"}
	if strings.Join(r.pms.deleted, " ") != strings.Join(want, " ") {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 {
		t.Fatalf("wrote %d record files; want 1", len(recs))
	}
	if got := keysOf(recs[0].Server); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("record lists %v; want %v", got, want)
	}
	if recs[0].Account != "QuienNZ" {
		t.Errorf("record account %q; want QuienNZ", recs[0].Account)
	}
	out := r.out.String()
	for _, s := range []string{"Bluey (2018)", "2 episode(s)", "NCIS: Hawaiʻi", "Not in this history:\n  Chicago Fire (2012)", "Deleted 3 of 3"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func TestClearHistory_NamedAccount(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "stephanie", "Bluey (2018)", "Chicago Fire"); err != nil {
		t.Fatal(err)
	}
	want := []string{historyKeyPrefix + "8", historyKeyPrefix + "9"}
	if strings.Join(r.pms.deleted, " ") != strings.Join(want, " ") {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if r.plexTVHits != 0 {
		t.Errorf("asked plex.tv %d times; a named account doesn't need the owner", r.plexTVHits)
	}
}

func TestClearHistory_UnknownAccount(t *testing.T) {
	r := newClearHistoryRun(t)
	err := r.run(t, "Nobody", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "QuienNZ, Stephanie") {
		t.Errorf("err = %v; want one listing the account names", err)
	}
}

func TestClearHistory_OwnerNotOnServer(t *testing.T) {
	r := newClearHistoryRun(t)
	r.pms.accounts = []plexAccount{{ID: 5, Name: "Stephanie"}}
	err := r.run(t, "", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "--account") {
		t.Errorf("err = %v; want one suggesting --account", err)
	}
}

func TestClearHistory_NothingToClear_DoesNotAsk(t *testing.T) {
	r := newClearHistoryRun(t)
	if err := r.run(t, "", "Chicago Med"); err != nil {
		t.Fatal(err)
	}
	if r.prompter.idx != 0 {
		t.Error("asked with nothing to delete")
	}
	if !strings.Contains(r.out.String(), "Nothing to clear.") {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// One failed delete doesn't stop the rest, and the run reports failure.
func TestClearHistory_PartialFailure(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{historyKeyPrefix + "1": true}
	err := r.run(t, "", "Bluey")
	if err == nil {
		t.Fatal("expected an error")
	}
	if want := []string{historyKeyPrefix + "2"}; strings.Join(r.pms.deleted, " ") != strings.Join(want, " ") {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if !strings.Contains(r.out.String(), "Deleted 1 of 2") {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// Without a record of what is about to go, nothing is deleted.
func TestClearHistory_RecordFails_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.recordDir = filepath.Join(blocker, "mkv-tvshow")
	if err := r.run(t, "", "Bluey"); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v without a record", r.pms.deleted)
	}
}

func TestMain_ClearHistoryFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--account", "Stephanie"}, "Error: --account only works with --clear-history."},
		{[]string{"--clear-history", "Bluey", "--clean"}, "Error: --clean and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Bluey"}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out, err := runMain(t, t.TempDir(), t.TempDir(), tc.args...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
			}
			if got := strings.TrimSpace(out); got != tc.want {
				t.Errorf("output %q; want %q", got, tc.want)
			}
		})
	}
}
