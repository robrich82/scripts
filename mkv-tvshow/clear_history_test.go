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
	ownerName    string // the PLEX_TOKEN owner's plex.tv username
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
		ownerName:    "QuienNZ",
		plexTVStatus: http.StatusOK,
		prompter:     &scriptedPrompter{answers: answers},
		recordDir:    filepath.Join(t.TempDir(), "mkv-tvshow"),
	}
}

func (r *clearHistoryRun) run(t *testing.T, account string, shows ...string) error {
	t.Helper()
	return clearHistory(shows, account, clearHistoryDeps{
		server:    newHTTPPlexClient(r.pms.serve().URL, secretToken),
		owner:     newPlexTVClient(fakePlexTV(t, r.ownerName, r.plexTVStatus, &r.plexTVHits).URL, secretToken),
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
		"進撃の巨人（2013）":         {"進撃の巨人", 2013},
		"Bluey (0000)":        {"Bluey (0000)", 0},
		"進撃の巨人\u3000(2013)":   {"進撃の巨人", 2013},
	} {
		title, year := splitTitleYear(in)
		if title != want.title || year != want.year {
			t.Errorf("splitTitleYear(%q) = %q, %d; want %q, %d", in, title, year, want.title, want.year)
		}
	}
}

// planTitles is the history titles target matches among shows.
func planTitles(t *testing.T, target string, shows ...string) []string {
	t.Helper()
	targets, err := parseHistoryTargets([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	var entries []historyEntry
	for i, show := range shows {
		entries = append(entries, episode(i, 1, show, "Ep"))
	}
	var titles []string
	for _, e := range planHistory(entries, targets).matched {
		titles = append(titles, e.GrandparentTitle)
	}
	return titles
}

func TestPlanHistory_TitleMatch(t *testing.T) {
	for _, tc := range []struct {
		target, show string
		want         bool
	}{
		{"Bluey (2018)", "Bluey (2018)", true},
		{"Bluey (2018)", "Bluey", true},
		{"Bluey", "Bluey (2018)", true},
		{"bluey", "BLUEY", true},
		{"Bluey (2018)", "Bluey (1992)", false},
		{"Bluey (0000)", "Bluey (1976)", false},
		{"Bluey", "Bluey Minisodes", false},
		{"NCIS: Hawaiʻi (2021)", "NCIS: Hawai'i", true},
		{"NCIS", "NCIS: Los Angeles", false},
		{"Doctor Who (2005) Specials", "Doctor Who (2005) Specials", true},
		{"進撃の巨人", "進撃の巨人", true},
		{"進撃の巨人", "進撃の巨人（2013）", true},
		{"【推しの子】", "【推しの子】", true},
		{"ＢＬＥＡＣＨ", "BLEACH", true},
		// Titles alike only in their ASCII, which the Plex lookup's
		// titleMatcher would match.
		{"Bleach", "BLEACH 千年血戦篇", false},
		{"Re:ゼロから始める異世界生活", "Re:ステージ！ドリームデイズ♪", false},
		{"進撃の巨人（2013）", "鬼滅の刃（2013）", false},
		{"Love Live! サンシャイン!!", "Love Live! スーパースター!!", false},
	} {
		got := len(planTitles(t, tc.target, tc.show)) == 1
		if got != tc.want {
			t.Errorf("%q matches %q = %v; want %v", tc.target, tc.show, got, tc.want)
		}
	}
}

// With the target's year in the history, a year-less title is another show.
func TestPlanHistory_YearlessTitleIsAnotherShowWhenTheYearIsThere(t *testing.T) {
	if got := planTitles(t, "Doctor Who (2005)", "Doctor Who (2005)", "Doctor Who"); !slices.Equal(got, []string{"Doctor Who (2005)"}) {
		t.Errorf("matched %v; want only Doctor Who (2005)", got)
	}
	if got := planTitles(t, "Doctor Who (2005)", "Doctor Who (1963)", "Doctor Who"); !slices.Equal(got, []string{"Doctor Who"}) {
		t.Errorf("matched %v; want only the year-less Doctor Who", got)
	}
	if got := planTitles(t, "Doctor Who", "Doctor Who (2005)", "Doctor Who"); len(got) != 2 {
		t.Errorf("matched %v; want both", got)
	}
}

func TestParseHistoryTargets_RejectsBlank(t *testing.T) {
	for _, s := range []string{"", "  ", "(2018)", "【】", "!!"} {
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
	if recs[0].Account != "QuienNZ" || recs[0].AccountID != 1 || !slices.Equal(recs[0].Targets, []string{"Bluey (2018)", "NCIS: Hawai'i (2021)", "Chicago Fire (2012)"}) {
		t.Errorf("record account %q (%d), targets %v", recs[0].Account, recs[0].AccountID, recs[0].Targets)
	}
	if !recs[0].Complete || recs[0].Failed != nil {
		t.Errorf("record complete %v, failed %v; want complete with none failed", recs[0].Complete, recs[0].Failed)
	}
	if len(r.prompter.asked) != 1 || !strings.Contains(r.prompter.asked[0], "Delete these 3 history entries for QuienNZ (account 1)?") {
		t.Errorf("asked %q", r.prompter.asked)
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
		t.Errorf("deleted %v; want none, from account 9 (\"stephanie\" exactly), which has no history", r.pms.deleted)
	}

	r = newClearHistoryRun(t)
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "stephanie"})
	if err := r.run(t, "STEPHANIE", "Bluey"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("err = %v; want an ambiguity error", err)
	}
	r.assertUntouched(t)
}

// Two accounts with the very same name can't be told apart by it.
func TestClearHistory_DuplicateAccountName(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "Stephanie"})
	err := r.run(t, "Stephanie", "Bluey")
	if err == nil || !strings.Contains(err.Error(), "ids 5, 9") {
		t.Errorf("err = %v; want one naming both ids", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history for an ambiguous account")
	}
	r.assertUntouched(t)
}

// A plex.tv reply without a username must not pick the unnamed account.
func TestClearHistory_OwnerWithoutUsername(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.ownerName = ""
	if err := r.run(t, "", "Bluey"); err == nil || !strings.Contains(err.Error(), "no username") {
		t.Errorf("err = %v; want a missing-username error", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history without an owner")
	}
	r.assertUntouched(t)
}

func TestClearHistory_NoNamedAccounts(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.accounts = []plexAccount{{ID: 0, Name: ""}}
	if err := r.run(t, "", "Bluey"); err == nil || !strings.Contains(err.Error(), "listed no named accounts") {
		t.Errorf("err = %v; want a no-accounts error", err)
	}
	if r.plexTVHits != 0 {
		t.Error("asked plex.tv with no accounts to match")
	}
	r.assertUntouched(t)
}

// Two shows with one title are listed apart, by key.
func TestClearHistory_SameTitleShowsListedApart(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	for i, key := range []string{"/library/metadata/100", "/library/metadata/100", "/library/metadata/200"} {
		e := episode(30+i, 1, "Doctor Who", "Ep")
		e.GrandparentKey = key
		r.pms.history = append(r.pms.history, e)
	}
	if err := r.run(t, "", "Doctor Who"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	for _, want := range []string{"Doctor Who [/library/metadata/100]", "Doctor Who [/library/metadata/200]", "more than one show"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
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
	if !strings.Contains(r.out.String(), "Stopped after 3 failures in a row; 3 entries weren't tried.") || strings.Count(r.out.String(), "  failed:") != 3 {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// A success between failures restarts the count, so scattered failures
// don't stop the run.
func TestClearHistory_ScatteredFailuresDontStop(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{}
	for i := 20; i < 27; i++ {
		e := episode(i, 1, "Paw Patrol", "Ep")
		r.pms.history = append(r.pms.history, e)
		if i%2 == 0 {
			r.pms.failKeys[e.HistoryKey] = true
		}
	}
	if err := r.run(t, "", "Paw Patrol"); err == nil {
		t.Fatal("expected an error")
	}
	if want := keys("21", "23", "25"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	recs := r.records(t)
	if len(recs) != 1 || !slices.Equal(recs[0].Failed, keys("20", "22", "24", "26")) {
		t.Errorf("records %+v; want 20, 22, 24 and 26 failed", recs)
	}
	if strings.Contains(r.out.String(), "Stopped") {
		t.Errorf("stopped on scattered failures:\n%s", r.out.String())
	}
}

// Until the deletes are done the record says it isn't complete, so a run
// that dies part way doesn't leave a record claiming every entry went.
func TestClearHistory_RecordIncompleteDuringDeletes(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	var during []bool
	r.pms.onDelete = func() {
		for _, rec := range r.records(t) {
			during = append(during, rec.Complete)
		}
	}
	if err := r.run(t, "", "Bluey (2018)"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(during, []bool{false, false}) {
		t.Errorf("record complete during deletes = %v; want false each time", during)
	}
	if recs := r.records(t); len(recs) != 1 || !recs[0].Complete {
		t.Errorf("records %+v; want one, complete", recs)
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
	err := r.run(t, "", "Bluey")
	// The path is named once, not again inside the OS error.
	if err == nil || !strings.HasSuffix(err.Error(), "Nothing was deleted") || strings.Count(err.Error(), blocker) != 1 {
		t.Fatalf("err = %v; want one naming the record once and saying nothing was deleted", err)
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
	if err := r.run(t, "", "Bluey"); err == nil || !strings.Contains(err.Error(), "an earlier record has this name") {
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
		env  []string
		want string
	}{
		{[]string{"--account", "Stephanie"}, nil, "Error: --account only works with --clear-history."},
		{[]string{"--clear-history", "Bluey", "--clean"}, nil, "Error: --clean and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Bluey", "--set-rating", "M"}, nil, "Error: --set-rating and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Bluey"}, nil, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		{[]string{"--clear-history", "Bluey"}, []string{"PLEX_URL=http://127.0.0.1:1"}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		{[]string{"--clear-history", "Bluey"}, []string{"PLEX_TOKEN=" + secretToken}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		// An unquoted title: "Fire" stops flag parsing, so --account would be lost.
		{[]string{"--clear-history", "Chicago", "Fire", "--account", "Stephanie"}, nil, `Error: unexpected argument "Fire": quote a title with spaces or a year, e.g. --clear-history "Bluey (2018)".`},
	} {
		t.Run(strings.Join(append(tc.env, tc.args...), " "), func(t *testing.T) {
			out, err := runMainEnv(t, t.TempDir(), t.TempDir(), tc.env, tc.args...)
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
	t.Run("no", func(t *testing.T) {
		pms := newPMS()
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken}
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "n\n", args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if len(pms.deleted) != 0 || !strings.Contains(out, "Aborted; no changes made.") {
			t.Errorf("deleted %v:\n%s", pms.deleted, out)
		}
	})
	t.Run("partial failure", func(t *testing.T) {
		pms := newPMS()
		pms.failKeys = map[string]bool{historyKeyPrefix + "8": true}
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken}
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "y\n", args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
		}
		if want := keys("9"); !slices.Equal(pms.deleted, want) || !strings.Contains(out, `under "failed"`) {
			t.Errorf("deleted %v; want %v:\n%s", pms.deleted, want, out)
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

type fixedOwner plexTVUser

func (o fixedOwner) User() (plexTVUser, error) { return plexTVUser(o), nil }

// Behind plex.tv's own check: an empty owner name never picks the unnamed
// account the server lists.
func TestResolveHistoryAccount_EmptyOwnerNameMatchesNothing(t *testing.T) {
	f := &fakePMS{t: t, accounts: []plexAccount{{ID: 0, Name: ""}, {ID: 1, Name: "QuienNZ"}}}
	acct, err := resolveHistoryAccount(newHTTPPlexClient(f.serve().URL, secretToken), fixedOwner{}, "")
	if err == nil {
		t.Errorf("resolved account %+v; want an error", acct)
	}
}
