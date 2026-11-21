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
	"slices"
	"strings"
	"testing"
	"time"
)

// fakePlexTV serves plex.tv's /api/v2/user for username, or status when it
// isn't 200, and counts requests.
func fakePlexTV(t *testing.T, username string, status int, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.URL.Path != "/api/v2/user" {
			t.Errorf("unexpected plex.tv request %s", r.URL)
		}
		if r.Header.Get("X-Plex-Token") != secretToken || r.Header.Get("X-Plex-Client-Identifier") == "" {
			t.Errorf("plex.tv request headers: token %q, client id %q", r.Header.Get("X-Plex-Token"), r.Header.Get("X-Plex-Client-Identifier"))
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, map[string]any{"username": username, "uuid": "u-1", "authToken": "not-for-logs"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

var recordTime = time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)

type clearHistoryRun struct {
	pms          *fakePMS
	plexTVStatus int
	plexTVHits   int
	prompter     *scriptedPrompter
	recordDir    string
	out          bytes.Buffer
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
				// A track with the show's title in the episode's field: only
				// the episode filter keeps it.
				{HistoryKey: historyKeyPrefix + "7", Type: "track", Title: "Bluey", GrandparentTitle: "Bluey", AccountID: 1},
				episode(8, 5, "Chicago Fire", "Pilot"),
				episode(9, 5, "Bluey (2018)", "Keepy Uppy"),
				episode(10, 1, "Bluey (1992)", "Pilot"),
			},
		},
		plexTVStatus: http.StatusOK,
		prompter:     &scriptedPrompter{answers: answers},
		recordDir:    filepath.Join(t.TempDir(), "mkv-tvshow"),
	}
}

func (r *clearHistoryRun) run(t *testing.T, account string, shows ...string) error {
	t.Helper()
	return clearHistory(shows, account, clearHistoryDeps{
		server:    newHTTPPlexClient(r.pms.serve().URL, secretToken),
		owner:     newPlexTVClient(fakePlexTV(t, "QuienNZ", r.plexTVStatus, &r.plexTVHits).URL, secretToken),
		prompt:    r.prompter,
		recordDir: r.recordDir,
		out:       &r.out,
		now:       func() time.Time { return recordTime },
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

// assertUntouched checks that nothing was deleted or recorded.
func (r *clearHistoryRun) assertUntouched(t *testing.T) {
	t.Helper()
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v", r.pms.deleted)
	}
	if recs := r.records(t); len(recs) != 0 {
		t.Errorf("wrote %d record files", len(recs))
	}
}

func keys(ids ...string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, historyKeyPrefix+id)
	}
	return out
}

func keysOf(entries []historyEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.HistoryKey)
	}
	return out
}

func TestSplitTitleYear(t *testing.T) {
	for in, want := range map[string]struct {
		title string
		year  int
	}{
		"Bluey (2018)":        {"Bluey", 2018},
		"Bluey(2018) ":        {"Bluey", 2018},
		"Bluey":               {"Bluey", 0},
		" Hope Valley: 1874 ": {"Hope Valley: 1874", 0},
		"1883 (2021)":         {"1883", 2021},
		"Show (Extended)":     {"Show (Extended)", 0},
		"Show (2018) (US)":    {"Show (2018) (US)", 0},
		"進撃の巨人\u3000(2013)":   {"進撃の巨人", 2013},
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
		{"【推しの子】", "【推しの子】", true},
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

func TestClearHistory_NotYes_DeletesNothing(t *testing.T) {
	for _, ans := range []string{"n", "", "yes", "N", " "} {
		t.Run(ans, func(t *testing.T) {
			r := newClearHistoryRun(t, ans)
			if err := r.run(t, "", "Bluey (2018)"); err != nil {
				t.Fatal(err)
			}
			r.assertUntouched(t)
			if !strings.Contains(r.out.String(), "Aborted; no changes made.") {
				t.Errorf("output:\n%s", r.out.String())
			}
		})
	}
}

// A prompt that can't be read (closed stdin) is an error, not a quiet "no".
func TestClearHistory_PromptError_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t) // no answers: Ask fails
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want a read error saying nothing was deleted", err)
	}
	r.assertUntouched(t)
}

// Only the owner's episode entries of the named shows go: not the track
// with the same title, not other shows or years, and not Stephanie's.
func TestClearHistory_Confirmed_DeletesOnlyMatches(t *testing.T) {
	r := newClearHistoryRun(t, " Y ")
	if err := r.run(t, "", "Bluey (2018)", "NCIS: Hawai'i (2021)", "Chicago Fire (2012)"); err != nil {
		t.Fatal(err)
	}
	want := keys("1", "2", "4")
	if !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 {
		t.Fatalf("wrote %d record files; want 1", len(recs))
	}
	if got := keysOf(recs[0].Server); !slices.Equal(got, want) {
		t.Errorf("record lists %v; want %v", got, want)
	}
	if recs[0].Account != "QuienNZ" || !slices.Equal(recs[0].Targets, []string{"Bluey (2018)", "NCIS: Hawai'i (2021)", "Chicago Fire (2012)"}) {
		t.Errorf("record account %q, targets %v", recs[0].Account, recs[0].Targets)
	}
	if recs[0].Failed != nil {
		t.Errorf("record failed = %v; want none", recs[0].Failed)
	}
	out := r.out.String()
	for _, s := range []string{
		"Bluey (2018)", "2 episode(s)", "NCIS: Hawaiʻi",
		"matched on title alone: NCIS: Hawai'i (2021)",
		"Not in this history:\n  Chicago Fire (2012)", "Deleted 3 of 3",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// A target without a year takes every year's show, each listed.
func TestClearHistory_NoYear_MatchesEveryYear(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Bluey"); err != nil {
		t.Fatal(err)
	}
	if want := keys("1", "2", "10"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	out := r.out.String()
	if i, j := strings.Index(out, "Bluey (1992)"), strings.Index(out, "Bluey (2018)"); i < 0 || j < i {
		t.Errorf("plan should list Bluey (1992) then Bluey (2018):\n%s", out)
	}
}

// Targets that overlap delete each entry once, and neither is reported as
// missing.
func TestClearHistory_OverlappingTargets(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Bluey", "Bluey (2018)", "Bluey"); err != nil {
		t.Fatal(err)
	}
	if want := keys("1", "2", "10"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if strings.Contains(r.out.String(), "Not in this history") {
		t.Errorf("an overlapping target was reported missing:\n%s", r.out.String())
	}
}

func TestClearHistory_NamedAccount(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "stephanie", "Bluey (2018)", "Chicago Fire"); err != nil {
		t.Fatal(err)
	}
	if want := keys("8", "9"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if r.plexTVHits != 0 {
		t.Errorf("asked plex.tv %d times; a named account doesn't need the owner", r.plexTVHits)
	}
}

// An exact name wins over one that differs only in case; two that differ
// only in case from the name given are ambiguous.
func TestClearHistory_AccountNameCase(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "stephanie"})
	if err := r.run(t, "stephanie", "Bluey"); err != nil {
		t.Fatal(err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v from account 9, which has no history; Stephanie's was used", r.pms.deleted)
	}

	r = newClearHistoryRun(t)
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "stephanie"})
	if err := r.run(t, "STEPHANIE", "Bluey"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("err = %v; want an ambiguity error", err)
	}
	r.assertUntouched(t)
}

func TestClearHistory_UnknownAccount(t *testing.T) {
	r := newClearHistoryRun(t)
	err := r.run(t, "Nobody", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "QuienNZ, Stephanie") {
		t.Errorf("err = %v; want one listing the account names", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history for an unknown account")
	}
	r.assertUntouched(t)
}

func TestClearHistory_OwnerNotOnServer(t *testing.T) {
	r := newClearHistoryRun(t)
	r.pms.accounts = []plexAccount{{ID: 5, Name: "Stephanie"}}
	err := r.run(t, "", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "--account") {
		t.Errorf("err = %v; want one suggesting --account", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history without an account")
	}
}

// A token plex.tv rejects (a server token) points at --account.
func TestClearHistory_OwnerLookupFails(t *testing.T) {
	r := newClearHistoryRun(t)
	r.plexTVStatus = http.StatusUnauthorized
	err := r.run(t, "", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "pass --account (one of: QuienNZ, Stephanie)") {
		t.Errorf("err = %v; want the 401 and a pointer to --account", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history without an account")
	}
}

// A server that ignores accountID would hand over everyone's history.
func TestClearHistory_OtherAccountsHistory_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.ignoreAccount = true
	err := r.run(t, "", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want a refusal", err)
	}
	r.assertUntouched(t)
}

func TestClearHistory_HistoryFetchFails_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.history = append(r.pms.history, historyEntry{HistoryKey: "/library/metadata/1", Type: "episode", GrandparentTitle: "Bluey", AccountID: 1})
	if err := r.run(t, "", "Bluey"); err == nil {
		t.Fatal("expected an error")
	}
	if r.prompter.idx != 0 {
		t.Error("asked after a failed history fetch")
	}
	r.assertUntouched(t)
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

// One failed delete doesn't stop the rest; the record says which failed and
// the run reports failure.
func TestClearHistory_PartialFailure(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{historyKeyPrefix + "1": true}
	err := r.run(t, "", "Bluey (2018)")
	if err == nil || !strings.Contains(err.Error(), `under "failed"`) {
		t.Fatalf("err = %v; want one pointing at the record", err)
	}
	if want := keys("2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 || !slices.Equal(keysOf(recs[0].Server), keys("1", "2")) || !slices.Equal(recs[0].Failed, keys("1")) {
		t.Errorf("records %+v; want one listing 1 and 2, with 1 failed", recs)
	}
	if !strings.Contains(r.out.String(), "Deleted 1 of 2") {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// A server that has gone away is given up on after a few failures, and the
// rest are recorded as not deleted.
func TestClearHistory_StopsAfterRepeatedFailures(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{}
	for i := 20; i < 26; i++ {
		e := episode(i, 1, "Paw Patrol", "Ep")
		r.pms.history = append(r.pms.history, e)
		r.pms.failKeys[e.HistoryKey] = true
	}
	if err := r.run(t, "", "Paw Patrol"); err == nil {
		t.Fatal("expected an error")
	}
	recs := r.records(t)
	if len(recs) != 1 || len(recs[0].Failed) != 6 {
		t.Errorf("records %+v; want all 6 entries failed", recs)
	}
	if !strings.Contains(r.out.String(), "Stopped after 3 failures in a row.") || strings.Count(r.out.String(), "  failed:") != 3 {
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

// An earlier record with the same name is kept, and nothing is deleted.
func TestClearHistory_RecordExists_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := os.MkdirAll(r.recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	earlier := filepath.Join(r.recordDir, "history-cleared-20261122T093000Z.json")
	if err := os.WriteFile(earlier, []byte("earlier"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.run(t, "", "Bluey"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v; want an already-exists error", err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v", r.pms.deleted)
	}
	if data, _ := os.ReadFile(earlier); string(data) != "earlier" {
		t.Errorf("earlier record overwritten: %q", data)
	}
}

func TestMain_ClearHistoryFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--account", "Stephanie"}, "Error: --account only works with --clear-history."},
		{[]string{"--clear-history", "Bluey", "--clean"}, "Error: --clean and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Bluey", "--set-rating", "M"}, "Error: --set-rating and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Bluey"}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		// An unquoted title: "Fire" stops flag parsing, so --account would be lost.
		{[]string{"--clear-history", "Chicago", "Fire", "--account", "Stephanie"}, `Error: unexpected argument "Fire": quote a title with spaces or a year, e.g. --clear-history "Bluey (2018)".`},
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

// The whole mode through main(): flags, env, the stdin prompt and the record
// in the config dir. --account skips plex.tv, which a test can't stand in for.
func TestMain_ClearHistory(t *testing.T) {
	newPMS := func() *fakePMS {
		return &fakePMS{
			t:        t,
			accounts: []plexAccount{{ID: 1, Name: "QuienNZ"}, {ID: 5, Name: "Stephanie"}},
			history:  []historyEntry{episode(8, 5, "Chicago Fire", "Pilot"), episode(9, 5, "Bluey (2018)", "Keepy Uppy"), episode(11, 5, "Peppa Pig", "Ep")},
		}
	}
	args := []string{"--clear-history", "Bluey (2018)", "--clear-history", "Chicago Fire", "--account", "Stephanie"}

	t.Run("yes", func(t *testing.T) {
		pms := newPMS()
		cfgHome := t.TempDir()
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken}
		out, err := runMainStdin(t, t.TempDir(), cfgHome, env, "y\n", args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if want := keys("8", "9"); !slices.Equal(pms.deleted, want) {
			t.Errorf("deleted %v; want %v", pms.deleted, want)
		}
		files, _ := filepath.Glob(filepath.Join(cfgHome, "mkv-tvshow", "history-cleared-*.json"))
		if len(files) != 1 {
			t.Errorf("record files %v; want one in the config dir", files)
		}
	})
	t.Run("closed stdin", func(t *testing.T) {
		pms := newPMS()
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken}
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "", args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
		}
		if len(pms.deleted) != 0 {
			t.Errorf("deleted %v", pms.deleted)
		}
	})
}
