package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptedPrompter answers each prompt in turn, and fails once out of
// answers.
type scriptedPrompter struct {
	answers []string
	asked   []string // every prompt, answered or not
}

func (p *scriptedPrompter) ask(prompt string) (string, error) {
	p.asked = append(p.asked, prompt)
	if len(p.asked) > len(p.answers) {
		return "", fmt.Errorf("prompter: queue exhausted after %d answers", len(p.answers))
	}
	return p.answers[len(p.asked)-1], nil
}

var recordTime = time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)

type clearHistoryRun struct {
	pms       *fakePMS
	tv        *fakePlexTV // plex.tv; the owner is QuienNZ, with no history
	prompter  *scriptedPrompter
	recordDir string
	out       bytes.Buffer
	slept     []time.Duration
	onSleep   func() // runs after each wait is recorded
}

// newClearHistoryRun has an owner "QuienNZ" (id 1) and "Stephanie" (id 5),
// each with a server watch history, behind real HTTP clients.
func newClearHistoryRun(t *testing.T, answers ...string) *clearHistoryRun {
	t.Helper()
	return &clearHistoryRun{
		pms: &fakePMS{
			t:        t,
			accounts: []plexAccount{{ID: 1, Name: "QuienNZ"}, {ID: 5, Name: "Stephanie"}, {ID: 6, Name: ""}},
			history: []historyEntry{
				moviePlay(1, 1, "Heat", "1995-12-15"),
				moviePlay(2, 1, "Heat", "1995-12-15"),
				moviePlay(3, 1, "Alien", "1979-05-25"),
				moviePlay(4, 1, "Mission: Impossible", "1996-05-22"),
				moviePlay(5, 1, "Mission: Impossible – Fallout", "2018-07-27"),
				// A track with the matched movie's exact title: only the
				// movie filter keeps it.
				{HistoryKey: historyKeyPrefix + "7", Type: "track", Title: "Heat", OriginallyAvailableAt: "1995-12-15", AccountID: 1},
				moviePlay(8, 5, "Alien", "1979-05-25"),
				moviePlay(9, 5, "Heat", "1995-12-15"),
				moviePlay(10, 1, "Heat", "1986-03-14"),
				moviePlay(11, 1, "The Thing", ""),
			},
		},
		tv:        newFakePlexTV(t),
		prompter:  &scriptedPrompter{answers: answers},
		recordDir: filepath.Join(t.TempDir(), "mkv-movie"),
	}
}

func (r *clearHistoryRun) run(t *testing.T, account string, movies ...string) error {
	t.Helper()
	plexTV := r.tv.client()
	return clearHistory(movies, account, clearHistoryDeps{
		server:    newHTTPPlexMovieClient(r.pms.serve().URL, secretToken),
		owner:     plexTV,
		cloud:     plexTV,
		prompt:    r.prompter.ask,
		recordDir: r.recordDir,
		out:       &r.out,
		now:       func() time.Time { return recordTime },
		sleep: func(d time.Duration) {
			r.slept = append(r.slept, d)
			if r.onSleep != nil {
				r.onSleep()
			}
		},
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

// assertUntouched checks that nothing was deleted, on the server or
// plex.tv, or recorded.
func (r *clearHistoryRun) assertUntouched(t *testing.T) {
	t.Helper()
	if len(r.pms.deleted) != 0 || len(r.tv.removed) != 0 {
		t.Errorf("server deleted %v, plex.tv removed %v", r.pms.deleted, r.tv.removed)
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
		"Heat (1995)":          {"Heat", 1995},
		"Heat(1995) ":          {"Heat", 1995},
		"Heat":                 {"Heat", 0},
		" Blade Runner 2049":   {"Blade Runner 2049", 0},
		"1917 (2019)":          {"1917", 2019},
		"Movie (Extended)":     {"Movie (Extended)", 0},
		"Movie (2018) (US)":    {"Movie (2018) (US)", 0},
		"千と千尋の神隠し（2001）":       {"千と千尋の神隠し", 2001},
		"Heat (0000)":          {"Heat (0000)", 0},
		"千と千尋の神隠し\u3000(2001)": {"千と千尋の神隠し", 2001},
	} {
		title, year := splitTitleYear(in)
		if title != want.title || year != want.year {
			t.Errorf("splitTitleYear(%q) = %q, %d; want %q, %d", in, title, year, want.title, want.year)
		}
	}
}

// planTitles is the history titles target matches among undated plays of
// titles.
func planTitles(t *testing.T, target string, titles ...string) []string {
	t.Helper()
	targets, err := parseHistoryTargets([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	var entries []historyEntry
	for i, title := range titles {
		entries = append(entries, moviePlay(i, 1, title, ""))
	}
	var got []string
	for _, e := range planHistory(entries, targets).matched {
		got = append(got, e.Title)
	}
	return got
}

func TestPlanHistory_TitleMatch(t *testing.T) {
	for _, tc := range []struct {
		target, title string
		want          bool
	}{
		{"Heat (1995)", "Heat (1995)", true},
		{"Heat (1995)", "Heat", true},
		{"Heat", "Heat (1995)", true},
		{"heat", "HEAT", true},
		{"Heat (1995)", "Heat (1986)", false},
		{"Heat (0000)", "Heat (1976)", false},
		{"Alien", "Aliens", false},
		{"Mission Impossible", "Mission: Impossible", true},
		{"Mission: Impossible", "Mission: Impossible – Fallout", false},
		{"Hawaiʻi (2021)", "Hawai'i", true},
		{"千と千尋の神隠し", "千と千尋の神隠し", true},
		{"千と千尋の神隠し", "千と千尋の神隠し（2001）", true},
		{"【推しの子】", "【推しの子】", true},
		{"ＨＥＡＴ", "HEAT", true},
		{"Ｍｏｖｉｅ ２", "Movie 2", true},
		{"Toy Story 2", "Toy Story 3", false},
		{"1917", "1941", false},
		// Numbers that aren't decimal digits still tell sequels apart.
		{"Rocky", "Rocky Ⅱ", false},
		{"Movie", "Movie ②", false},
		{"Movie", "Movie²", false},
		// Symbols tell titles apart; tildes and minus signs stand in for dashes.
		{"Gundam", "∀ Gundam", false},
		{"C", "C++", false},
		{"Hunter Hunter", "Hunter × Hunter", false},
		{"Gintama", "Gintama°", false},
		{"Prisma Illya", "Prisma☆Illya", false},
		{"かぐや様 ～Ultra Romantic～", "かぐや様 〜Ultra Romantic〜", true},
		{"Re:ゼロ − 始める", "Re:ゼロ - 始める", true},
		{"A-B", "A─B", true},
		{"A-B", "A━B", true},
		{"A-B", "A➖B", true},
		{"A-B", "A⁻B", true},
		{"A-B", "A₋B", true},
		{"A-B", "A⁓B", true},
		{"Hawai`i", "Hawai'i", true},
		// Full-width punctuation folds to ASCII, then drops.
		{"Ｍｒ．Ｎｏｂｏｄｙ", "Mr. Nobody", true},
		// Vowel signs are marks, not letters, and tell these titles apart.
		{"काल", "कल", false},
		// Titles alike only in their ASCII, which the Plex lookup's
		// normalizePlexTitle would match.
		{"Bleach", "BLEACH 千年血戦篇", false},
		{"千と千尋の神隠し（2001）", "もののけ姫（2001）", false},
		{"Love Live! サンシャイン!!", "Love Live! スーパースター!!", false},
	} {
		got := len(planTitles(t, tc.target, tc.title)) == 1
		if got != tc.want {
			t.Errorf("%q matches %q = %v; want %v", tc.target, tc.title, got, tc.want)
		}
	}
}

// planMovies plans target against one play of each movie, dated January 1
// of its year (undated for year 0).
func planMovies(t *testing.T, target string, movies ...historyMovie) historyPlan {
	t.Helper()
	targets, err := parseHistoryTargets([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	var entries []historyEntry
	for i, m := range movies {
		date := ""
		if m.year != 0 {
			date = fmt.Sprintf("%04d-01-01", m.year)
		}
		e := moviePlay(i, 1, m.title, date)
		e.Key = m.key
		entries = append(entries, e)
	}
	return planHistory(entries, targets)
}

// A year matches within one of the release date's, as FindMovieByTitle
// does: a release date can be a year off the year a movie is known by.
func TestPlanHistory_YearWithinOne(t *testing.T) {
	for _, tc := range []struct {
		target string
		year   int
		want   bool
	}{
		{"Heat (1995)", 1995, true},
		{"Heat (1996)", 1995, true},
		{"Heat (1994)", 1995, true},
		{"Heat (1997)", 1995, false},
		{"Heat (1993)", 1995, false},
		{"Heat", 1995, true},
	} {
		got := len(planMovies(t, tc.target, historyMovie{title: "Heat", year: tc.year}).matched) == 1
		if got != tc.want {
			t.Errorf("%q matches Heat released %d = %v; want %v", tc.target, tc.year, got, tc.want)
		}
	}
}

// The year a release date starts with gives an entry's year; without one, a
// "(Year)" its title ends in does. A date with no year in front isn't no
// date: that would match on title alone.
func TestEntryYear(t *testing.T) {
	for _, tc := range []struct {
		title, date string
		want        int
	}{
		{"Heat", "1995-12-15", 1995},
		{"Heat (1995)", "", 1995},
		{"Heat (1994)", "1995-12-15", 1995},
		{"Heat", "", 0},
		{"Heat", "1986", 1986},
		{"Heat", "1986-12", 1986},
		{"Heat", "1995-12-15T00:00:00Z", 1995},
		{"Heat", "1995-02-30", 1995},
		{"Heat (1994)", "0000-01-01", 1994},
		{"Heat (1994)", "not a date", 1994},
		{"Heat", "not a date", unreadableYear},
		{"Heat", "0000-00-00", unreadableYear},
	} {
		if got := entryYear(historyEntry{Title: tc.title, OriginallyAvailableAt: tc.date}); got != tc.want {
			t.Errorf("entryYear(%q, %q) = %d; want %d", tc.title, tc.date, got, tc.want)
		}
	}
}

// With the target's year in the history, a year-less entry is another movie.
func TestPlanHistory_YearlessIsAnotherMovieWhenTheYearIsThere(t *testing.T) {
	titles := func(plan historyPlan) []string {
		var out []string
		for _, e := range plan.matched {
			out = append(out, movieLabel(movieOf(e)))
		}
		return out
	}
	plan := planMovies(t, "Solaris (2002)", historyMovie{title: "Solaris", year: 2002}, historyMovie{title: "Solaris"})
	if got := titles(plan); !slices.Equal(got, []string{"Solaris (2002)"}) {
		t.Errorf("matched %v; want only Solaris (2002)", got)
	}
	if sum := plan.kept[historyMovie{title: "Solaris"}]; len(plan.kept) != 1 || sum == nil || sum.plays != 1 {
		t.Errorf("kept %v; want the year-less Solaris, listed as not deleted", plan.kept)
	}
	plan = planMovies(t, "Solaris (2002)", historyMovie{title: "Solaris", year: 1972}, historyMovie{title: "Solaris"})
	if got := titles(plan); !slices.Equal(got, []string{"Solaris"}) {
		t.Errorf("matched %v; want only the year-less Solaris", got)
	}
	if len(plan.kept) != 0 {
		t.Errorf("kept %v; another year's movie isn't listed", plan.kept)
	}
	plan = planMovies(t, "Solaris", historyMovie{title: "Solaris", year: 2002}, historyMovie{title: "Solaris"})
	if got := titles(plan); len(got) != 2 {
		t.Errorf("matched %v; want both", got)
	}
	if len(plan.yearUnchecked) != 0 || len(plan.kept) != 0 {
		t.Errorf("year unchecked %v, kept %v; a target without a year checks none", plan.yearUnchecked, plan.kept)
	}
}

// A movie one target keeps isn't listed as kept when another target takes
// it, whichever comes first.
func TestPlanHistory_KeptOnlyWhenNoTargetTakesIt(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"Solaris (2002)", "Solaris"})
	if err != nil {
		t.Fatal(err)
	}
	entries := []historyEntry{moviePlay(1, 1, "Solaris", "2002-01-01"), moviePlay(2, 1, "Solaris", "")}
	for _, order := range [][]historyTarget{targets, {targets[1], targets[0]}} {
		plan := planHistory(entries, order)
		if got := keysOf(plan.matched); !slices.Equal(got, keys("1", "2")) || len(plan.kept) != 0 {
			t.Errorf("targets %v: matched %v, kept %v; want both matched and none kept", order, got, plan.kept)
		}
	}
}

// localDay is the plan's date for a play at sec: the local day, so tests
// pass in any time zone.
func localDay(sec int64) string { return time.Unix(sec, 0).Format("2006-01-02") }

// An entry whose release date can't be read may be another year's movie: a
// dated target keeps it and lists it, and one without a year takes it.
func TestPlanHistory_UnreadableDate(t *testing.T) {
	entry := moviePlay(1, 1, "Heat", "not a date")
	entry.Key = "/library/metadata/1" // a key no dated entry shares
	entries := []historyEntry{entry}
	for _, tc := range []struct {
		target        string
		matched, kept int
	}{
		{"Heat (1995)", 0, 1},
		{"Heat", 1, 0},
	} {
		targets, err := parseHistoryTargets([]string{tc.target})
		if err != nil {
			t.Fatal(err)
		}
		plan := planHistory(entries, targets)
		if len(plan.matched) != tc.matched || len(plan.kept) != tc.kept || len(plan.yearUnchecked) != 0 {
			t.Errorf("%s: matched %v, kept %v, year unchecked %v; want %d matched, %d kept", tc.target, keysOf(plan.matched), plan.kept, plan.yearUnchecked, tc.matched, tc.kept)
		}
		var out strings.Builder
		printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, plan)
		if got := strings.Contains(out.String(), "Not deleting these, which may be other movies (give the title without its year to include them):\n  Heat                                     1 play(s), last watched "+localDay(1_700_000_000)+", with a release date that can't be read\n"); got != (tc.kept == 1) {
			t.Errorf("%s: plan lists the kept entry: %v; want %v:\n%s", tc.target, got, tc.kept == 1, out.String())
		}
	}
}

// The plan lists a year-less entry it keeps, saying why.
func TestPrintHistoryPlan_ListsKept(t *testing.T) {
	plan := planMovies(t, "X (2019)", historyMovie{"X", 2019, "/library/metadata/7"}, historyMovie{title: "X"})
	var out strings.Builder
	printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, plan)
	want := "Not deleting these, which may be other movies (give the title without its year to include them):\n  X                                        1 play(s), last watched " + localDay(1_700_000_000) + ", with no release date\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Errorf("plan:\n%s\nwant it to end with:\n%s", out.String(), want)
	}
}

// A dated target whose range takes movies of two years says so: they may be
// two movies. One year, or another year tied by key, doesn't.
func TestPlanHistory_SeveralYears(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		movies []historyMovie
		want   bool
	}{
		{"two years in range", "Godzilla (1999)", []historyMovie{{"Godzilla", 1998, "/a"}, {"Godzilla", 1999, "/b"}}, true},
		{"one year, two keys", "Heat (1995)", []historyMovie{{"Heat", 1995, "/a"}, {"Heat", 1995, ""}}, false},
		{"other year by key", "X (2019)", []historyMovie{{"X", 2019, "/7"}, {"X", 2023, "/7"}}, false},
		{"no year given", "Heat", []historyMovie{{"Heat", 1986, "/a"}, {"Heat", 1995, "/b"}}, false},
	} {
		plan := planMovies(t, tc.target, tc.movies...)
		if got := slices.Equal(plan.severalYears, []string{tc.target}); got != tc.want || (!tc.want && len(plan.severalYears) != 0) {
			t.Errorf("%s: several years %v; want flagged %v", tc.name, plan.severalYears, tc.want)
		}
		var out strings.Builder
		printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, plan)
		if got := strings.Contains(out.String(), "These matched movies of more than one year, which may be different movies: check each before answering: "+tc.target+"\n"); got != tc.want {
			t.Errorf("%s: plan has the notice: %v; want %v:\n%s", tc.name, got, tc.want, out.String())
		}
	}
}

// Only movie entries say what years a title has: a track dated in the
// target's year doesn't make a year-less movie another one.
func TestPlanHistory_OtherTypesDontCountForYears(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"The Thing (1982)"})
	if err != nil {
		t.Fatal(err)
	}
	track := historyEntry{HistoryKey: historyKeyPrefix + "2", Type: "track", Title: "The Thing", OriginallyAvailableAt: "1982-06-25", AccountID: 1}
	plan := planHistory([]historyEntry{moviePlay(1, 1, "The Thing", ""), track}, targets)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("1")) {
		t.Errorf("matched %v; want the year-less movie", got)
	}
}

// A target given twice that matches nothing is listed as missing once.
func TestPlanHistory_RepeatedMissingTargetListedOnce(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"Ronin", "Ronin"})
	if err != nil {
		t.Fatal(err)
	}
	if plan := planHistory(nil, targets); !slices.Equal(plan.missing, []string{"Ronin"}) {
		t.Errorf("missing %v; want Ronin once", plan.missing)
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
			if err := r.run(t, "", "Heat (1995)"); err != nil {
				t.Fatal(err)
			}
			r.assertUntouched(t)
			if !strings.Contains(r.out.String(), "Aborted; no changes made.") {
				t.Errorf("output:\n%s", r.out.String())
			}
		})
	}
}

// A prompt that can't be read is an error, not a quiet "no".
func TestClearHistory_PromptError_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t) // no answers: ask fails
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want a read error saying nothing was deleted", err)
	}
	r.assertUntouched(t)
}

// Only the owner's movie entries of the named movies go: not the track with
// the same title, not other movies or years, and not Stephanie's.
func TestClearHistory_Confirmed_DeletesOnlyMatches(t *testing.T) {
	r := newClearHistoryRun(t, " Y ")
	if err := r.run(t, "", "Heat (1995)", "Mission Impossible (1996)", "The Thing (1982)", "Aliens (1986)"); err != nil {
		t.Fatal(err)
	}
	want := keys("1", "2", "4", "11")
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
	if recs[0].Account != "QuienNZ" || recs[0].AccountID != 1 || !slices.Equal(recs[0].Targets, []string{"Heat (1995)", "Mission Impossible (1996)", "The Thing (1982)", "Aliens (1986)"}) {
		t.Errorf("record account %q (%d), targets %v", recs[0].Account, recs[0].AccountID, recs[0].Targets)
	}
	if !recs[0].Complete || recs[0].Failed != nil {
		t.Errorf("record complete %v, failed %v; want complete with none failed", recs[0].Complete, recs[0].Failed)
	}
	// plex.tv was read and had none of these: [], not null, which means skipped.
	if recs[0].PlexTV == nil || len(recs[0].PlexTV) != 0 {
		t.Errorf("record plexTV %v; want empty, not null", recs[0].PlexTV)
	}
	if len(r.prompter.asked) != 1 || !strings.Contains(r.prompter.asked[0], "Delete these 4 history entries for QuienNZ (account 1)?") {
		t.Errorf("asked %q", r.prompter.asked)
	}
	out := r.out.String()
	for _, s := range []string{
		"Heat (1995)", "2 play(s)", "Mission: Impossible (1996)",
		"matched them on title alone: The Thing (1982)",
		"Not in this history:\n  Aliens (1986)", "Deleted 4 of 4",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// A target without a year takes every year's movie, each listed.
func TestClearHistory_NoYear_MatchesEveryYear(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Heat"); err != nil {
		t.Fatal(err)
	}
	if want := keys("1", "2", "10"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	out := r.out.String()
	if i, j := strings.Index(out, "Heat (1986)"), strings.Index(out, "Heat (1995)"); i < 0 || j < i {
		t.Errorf("plan should list Heat (1986) then Heat (1995):\n%s", out)
	}
}

// Targets that overlap delete each entry once, and neither is reported as
// missing.
func TestClearHistory_OverlappingTargets(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Heat", "Heat (1995)", "Heat"); err != nil {
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
	if err := r.run(t, "stephanie", "Heat (1995)", "Alien"); err != nil {
		t.Fatal(err)
	}
	if want := keys("8", "9"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	// This token reaches only its owner's plex.tv history, so plex.tv is
	// asked who that is, once.
	if r.tv.userHits != 1 || r.tv.graphHits != 0 {
		t.Errorf("asked plex.tv for the owner %d times and the history %d times; want once and never", r.tv.userHits, r.tv.graphHits)
	}
	if want := "This PLEX_TOKEN can clear only its owner's (QuienNZ) plex.tv history, so any Stephanie has there is left alone."; !strings.Contains(r.out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, r.out.String())
	}
}

// An exact name wins over one that differs only in case; two that differ
// only in case from the name given are ambiguous.
func TestClearHistory_AccountNameCase(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "stephanie"})
	if err := r.run(t, "stephanie", "Heat"); err != nil {
		t.Fatal(err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v; want none, from account 9 (\"stephanie\" exactly), which has no history", r.pms.deleted)
	}

	r = newClearHistoryRun(t)
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "stephanie"})
	if err := r.run(t, "STEPHANIE", "Heat"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("err = %v; want an ambiguity error", err)
	}
	r.assertUntouched(t)
}

// Two accounts with the very same name can't be told apart by it.
func TestClearHistory_DuplicateAccountName(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.accounts = append(r.pms.accounts, plexAccount{ID: 9, Name: "Stephanie"})
	err := r.run(t, "Stephanie", "Heat")
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
	r.tv.username = ""
	if err := r.run(t, "", "Heat"); err == nil || !strings.Contains(err.Error(), "no username") {
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
	if err := r.run(t, "", "Heat"); err == nil || !strings.Contains(err.Error(), "listed no named accounts") {
		t.Errorf("err = %v; want a no-accounts error", err)
	}
	if r.tv.userHits != 0 {
		t.Error("asked plex.tv with no accounts to match")
	}
	r.assertUntouched(t)
}

// Two movies with one title and year are listed apart, each with its play
// count and last watched date.
func TestClearHistory_SameTitleMoviesListedApart(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	for i, p := range []struct {
		key      string
		viewedAt int64
	}{
		{"/library/metadata/100", 1_700_000_000},
		{"/library/metadata/100", 1_700_100_000},
		{"/library/metadata/200", 1_600_000_000},
	} {
		e := moviePlay(30+i, 1, "Solaris", "2002-11-27")
		e.Key, e.ViewedAt = p.key, p.viewedAt
		r.pms.history = append(r.pms.history, e)
	}
	if err := r.run(t, "", "Solaris"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	for _, want := range []string{
		"2 play(s), last watched " + localDay(1_700_100_000),
		"1 play(s), last watched " + localDay(1_600_000_000),
		"may be different movies",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/library/metadata/") {
		t.Errorf("output shows a key:\n%s", out)
	}
}

func TestClearHistory_UnknownAccount(t *testing.T) {
	r := newClearHistoryRun(t)
	err := r.run(t, "Nobody", "Heat")
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
	err := r.run(t, "", "Heat")
	if err == nil || !strings.Contains(err.Error(), "--account") {
		t.Errorf("err = %v; want one suggesting --account", err)
	}
	if len(r.pms.pageStarts) != 0 {
		t.Error("fetched history without an account")
	}
}

// Any other owner lookup failure doesn't point at --account: plex.tv is
// asked for the owner with it too, so it would fail the same way.
func TestClearHistory_OwnerLookupFails_NoAccountHint(t *testing.T) {
	r := newClearHistoryRun(t)
	r.tv.userStatus = http.StatusInternalServerError
	err := r.run(t, "", "Heat")
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "--account") {
		t.Errorf("err = %v; want the 500 without a pointer to --account", err)
	}
	if err != nil && strings.Count(err.Error(), "nothing was deleted") != 1 {
		t.Errorf("err = %v; want \"nothing was deleted\" once", err)
	}
}

// A token plex.tv rejects (a server token) points at --account.
func TestClearHistory_OwnerLookupFails(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		r := newClearHistoryRun(t)
		r.tv.userStatus = status
		err := r.run(t, "", "Heat")
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) || !strings.Contains(err.Error(), "pass --account (one of: QuienNZ, Stephanie)") {
			t.Errorf("%d: err = %v; want the status and a pointer to --account", status, err)
		}
		if len(r.pms.pageStarts) != 0 {
			t.Errorf("%d: fetched history without an account", status)
		}
	}
}

// A server that ignores accountID would hand over everyone's history.
func TestClearHistory_OtherAccountsHistory_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.ignoreAccount = true
	err := r.run(t, "", "Heat")
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v; want a refusal", err)
	}
	r.assertUntouched(t)
}

func TestClearHistory_HistoryFetchFails_DeletesNothing(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.history = append(r.pms.history, historyEntry{HistoryKey: "/library/metadata/1", Type: "movie", Title: "Heat", AccountID: 1})
	if err := r.run(t, "", "Heat"); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.prompter.asked) != 0 {
		t.Error("asked after a failed history fetch")
	}
	r.assertUntouched(t)
}

func TestClearHistory_NothingToClear_DoesNotAsk(t *testing.T) {
	r := newClearHistoryRun(t)
	if err := r.run(t, "", "Ronin"); err != nil {
		t.Fatal(err)
	}
	if len(r.prompter.asked) != 0 {
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
	err := r.run(t, "", "Heat (1995)")
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
	if len(recs) == 1 && !recs[0].Complete {
		t.Error("record not complete after a finished run with a failure")
	}
	out := r.out.String()
	if !strings.Contains(out, "Deleted 1 of 2") || !strings.Contains(out, "  failed: Heat (1995), last watched ") {
		t.Errorf("output:\n%s", out)
	}
}

// paddingtonPlays adds account 1's plays 20 to 20+n-1 of Paddington, and
// returns their keys.
func (r *clearHistoryRun) paddingtonPlays(n int) []string {
	var out []string
	for i := 20; i < 20+n; i++ {
		e := moviePlay(i, 1, "Paddington", "2014-11-28")
		r.pms.history = append(r.pms.history, e)
		out = append(out, e.HistoryKey)
	}
	return out
}

// A server that has gone away is given up on after a few failures, and the
// rest are recorded as not deleted.
func TestClearHistory_StopsAfterRepeatedFailures(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	want := r.paddingtonPlays(6)
	r.pms.failKeys = map[string]bool{}
	for _, k := range want[:3] {
		r.pms.failKeys[k] = true // the rest would succeed
	}
	if err := r.run(t, "", "Paddington"); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v after stopping", r.pms.deleted)
	}
	recs := r.records(t)
	if len(recs) != 1 || !slices.Equal(recs[0].Failed, want) || !recs[0].Complete {
		t.Errorf("records %+v; want one, complete, with all 6 failed in order", recs)
	}
	if !strings.Contains(r.out.String(), "Stopped after 3 failures in a row on the server; 3 entries there weren't tried.") || strings.Count(r.out.String(), "  failed:") != 3 {
		t.Errorf("output:\n%s", r.out.String())
	}
}

// A third failure on the last entry stops nothing, so it says nothing of it.
func TestClearHistory_LastThreeFail(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{}
	for _, k := range r.paddingtonPlays(3) {
		r.pms.failKeys[k] = true
	}
	if err := r.run(t, "", "Paddington"); err == nil {
		t.Fatal("expected an error")
	}
	if out := r.out.String(); strings.Contains(out, "Stopped") || strings.Count(out, "  failed:") != 3 {
		t.Errorf("output:\n%s", out)
	}
}

// A success between failures restarts the count, so scattered failures
// don't stop the run.
func TestClearHistory_ScatteredFailuresDontStop(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.failKeys = map[string]bool{}
	for i, k := range r.paddingtonPlays(7) {
		if i%2 == 0 {
			r.pms.failKeys[k] = true
		}
	}
	if err := r.run(t, "", "Paddington"); err == nil {
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
			if !slices.Equal(keysOf(rec.Server), keys("1", "2")) || rec.AccountID != 1 || !slices.Equal(rec.Targets, []string{"Heat (1995)"}) {
				t.Errorf("record during deletes = %+v; want every entry, the account and the target", rec)
			}
		}
	}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
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
	r.recordDir = filepath.Join(blocker, "mkv-movie")
	err := r.run(t, "", "Heat")
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
	if err := r.run(t, "", "Heat"); err == nil || !strings.Contains(err.Error(), "an earlier record has this name") {
		t.Fatalf("err = %v; want an already-exists error", err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v", r.pms.deleted)
	}
	if data, _ := os.ReadFile(earlier); string(data) != "earlier" {
		t.Errorf("earlier record overwritten: %q", data)
	}
}

// runMainStdin runs main() in a child process with cwd dir, a config dir
// under cfgHome, no TMDB_API_KEY or PLEX_ settings but extraEnv's, and
// stdin, and returns its combined output and exit error.
func runMainStdin(t *testing.T, dir, cfgHome string, extraEnv []string, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestRunMainHelper$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "TMDB_API_KEY="),
			strings.HasPrefix(kv, "PLEX_"),
			strings.HasPrefix(kv, "APPDATA="),
			strings.HasPrefix(kv, "XDG_CONFIG_HOME="),
			strings.HasPrefix(kv, "HOME="):
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(append(env, "MKV_MOVIE_RUN_MAIN=1", "APPDATA="+cfgHome, "XDG_CONFIG_HOME="+cfgHome, "HOME="+cfgHome), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestMain_ClearHistoryFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  []string
		want string
	}{
		{[]string{"--account", "Stephanie"}, nil, "Error: --account only works with --clear-history."},
		{[]string{"--clear-history", "Heat", "--clean"}, nil, "Error: --clean and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Heat", "--set-rating", "M"}, nil, "Error: --set-rating and --clear-history can't be combined; run them one at a time."},
		{[]string{"--clear-history", "Heat"}, nil, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		{[]string{"--clear-history", "Heat"}, []string{"PLEX_URL=http://127.0.0.1:1"}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		{[]string{"--clear-history", "Heat"}, []string{"PLEX_TOKEN=" + secretToken}, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN."},
		// An unquoted title: "Runner" stops flag parsing, so --account would be lost.
		{[]string{"--clear-history", "Blade", "Runner", "--account", "Stephanie"}, nil, `Error: unexpected argument "Runner": give each title its own --clear-history, quoted if it has spaces or a year, e.g. --clear-history "Heat (1995)" --clear-history Alien.`},
		// A second title without its own flag.
		{[]string{"--clear-history", "Heat", "Alien"}, nil, `Error: unexpected argument "Alien": give each title its own --clear-history, quoted if it has spaces or a year, e.g. --clear-history "Heat (1995)" --clear-history Alien.`},
	} {
		t.Run(strings.Join(append(tc.env, tc.args...), " "), func(t *testing.T) {
			out, err := runMainStdin(t, t.TempDir(), t.TempDir(), tc.env, "", tc.args...)
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
// in the config dir, with no TMDB_API_KEY. Stephanie's run asks the fake
// plex.tv only who owns the token.
func TestMain_ClearHistory(t *testing.T) {
	newPMS := func() *fakePMS {
		return &fakePMS{
			t:        t,
			accounts: []plexAccount{{ID: 1, Name: "QuienNZ"}, {ID: 5, Name: "Stephanie"}},
			history:  []historyEntry{moviePlay(8, 5, "Alien", "1979-05-25"), moviePlay(9, 5, "Heat", "1995-12-15"), moviePlay(11, 5, "Ronin", "1998-09-25")},
		}
	}
	args := []string{"--clear-history", "Heat (1995)", "--clear-history", "Alien", "--account", "Stephanie"}
	envFor := func(pms *fakePMS) []string {
		return []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken, "MKV_MOVIE_TEST_PLEXTV=" + newFakePlexTV(t).serve().URL}
	}

	t.Run("yes", func(t *testing.T) {
		pms := newPMS()
		cfgHome := t.TempDir()
		env := envFor(pms)
		out, err := runMainStdin(t, t.TempDir(), cfgHome, env, "y\n", args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if want := keys("8", "9"); !slices.Equal(pms.deleted, want) {
			t.Errorf("deleted %v; want %v", pms.deleted, want)
		}
		files, _ := filepath.Glob(filepath.Join(cfgHome, "mkv-movie", "history-cleared-*.json"))
		if len(files) != 1 {
			t.Errorf("record files %v; want one in the config dir", files)
		}
	})
	t.Run("no", func(t *testing.T) {
		pms := newPMS()
		env := envFor(pms)
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
		env := envFor(pms)
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "y\n", args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("err = %v; want exit status 1:\n%s", err, out)
		}
		if want := keys("9"); !slices.Equal(pms.deleted, want) || !strings.Contains(out, `under "failed"`) {
			t.Errorf("deleted %v; want %v:\n%s", pms.deleted, want, out)
		}
	})
	// mkv-movie's prompts read a closed stdin as a blank answer, so the run
	// aborts as for any answer but y.
	t.Run("closed stdin", func(t *testing.T) {
		pms := newPMS()
		env := envFor(pms)
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "", args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if len(pms.deleted) != 0 || !strings.Contains(out, "Aborted; no changes made.") {
			t.Errorf("deleted %v:\n%s", pms.deleted, out)
		}
	})
	// Unless plex.tv refuses the token, a failure to reach it stops the run,
	// even for another account: otherwise a run for the owner by name would
	// clear the server alone without saying why.
	t.Run("plex.tv unreachable", func(t *testing.T) {
		pms := newPMS()
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken} // plex.tv at a closed port
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "y\n", args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(out, "nothing was deleted") {
			t.Fatalf("err = %v; want exit status 1 and nothing deleted:\n%s", err, out)
		}
		if len(pms.deleted) != 0 {
			t.Errorf("deleted %v", pms.deleted)
		}
	})
	// Without --account it clears the owner's history on the server and on plex.tv.
	t.Run("owner with plex.tv", func(t *testing.T) {
		pms := newPMS()
		pms.history = append(pms.history, moviePlay(12, 1, "Heat", "1995-12-15"))
		tv := newFakePlexTV(t)
		tv.history = []cloudEntry{cloudPlay("c1", "Heat", 1995, "2026-11-22T08:00:00.000Z")}
		env := []string{"PLEX_URL=" + pms.serve().URL, "PLEX_TOKEN=" + secretToken, "MKV_MOVIE_TEST_PLEXTV=" + tv.serve().URL}
		out, err := runMainStdin(t, t.TempDir(), t.TempDir(), env, "y\n", "--clear-history", "Heat (1995)")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !slices.Equal(pms.deleted, keys("12")) || !slices.Equal(tv.removed, []string{"c1"}) {
			t.Errorf("server deleted %v, plex.tv removed %v; want 12 and c1:\n%s", pms.deleted, tv.removed, out)
		}
	})
}

type fixedOwner plexTVUser

func (o fixedOwner) User() (plexTVUser, error) { return plexTVUser(o), nil }

// Behind plex.tv's own check: an empty owner name never picks the unnamed
// account the server lists.
func TestResolveHistoryAccount_EmptyOwnerNameMatchesNothing(t *testing.T) {
	f := &fakePMS{t: t, accounts: []plexAccount{{ID: 0, Name: ""}, {ID: 1, Name: "QuienNZ"}}}
	acct, err := resolveHistoryAccount(newHTTPPlexMovieClient(f.serve().URL, secretToken), fixedOwner{}, "")
	if err == nil {
		t.Errorf("resolved account %+v; want an error", acct)
	}
}

// The record keeps every field of an entry: it is all that's left of it.
func TestClearHistory_RecordKeepsEntries(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	var want []historyEntry
	for i := range r.pms.history {
		e := &r.pms.history[i]
		e.Key = "/library/metadata/" + e.Title + e.OriginallyAvailableAt // one per movie
		if e.AccountID == 1 && e.Type == "movie" && e.Title == "Heat" && e.OriginallyAvailableAt == "1995-12-15" {
			want = append(want, *e)
		}
	}
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	recs := r.records(t)
	if len(recs) != 1 || len(want) != 2 || !slices.Equal(recs[0].Server, want) {
		t.Errorf("records %+v; want one listing %+v", recs, want)
	}
}

// Deletes that went through are reported even when the record can't be
// updated to say so.
func TestClearHistory_RecordUpdateFails(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newClearHistoryRun(t, "y")
	r.blockRecordOnDelete(t) // never unblocked, so the retry fails too
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "2 of 2 history entries were deleted, but") {
		t.Errorf("err = %v; want one saying the deletes happened but the record wasn't updated", err)
	}
	if want := keys("1", "2"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if strings.Contains(r.out.String(), "Not deleted:") {
		t.Errorf("listed entries not deleted when every delete went:\n%s", r.out.String())
	}
}

// When the record can't say which entries weren't deleted, the output does,
// untried ones included.
func TestClearHistory_RecordUpdateFails_NamesNotDeleted(t *testing.T) {
	setBeforeFinalSave(t, func() {})
	r := newClearHistoryRun(t, "y")
	plays := r.paddingtonPlays(5)
	r.pms.failKeys = map[string]bool{plays[0]: true, plays[1]: true, plays[2]: true}
	r.blockRecordOnDelete(t)
	err := r.run(t, "", "Paddington")
	if err == nil || !strings.Contains(err.Error(), "5 history entries were not deleted; the output above names them") {
		t.Errorf("err = %v; want one pointing at the output", err)
	}
	out := r.out.String()
	notDeleted, ok := strings.CutPrefix(out[strings.Index(out, "Not deleted:"):], "Not deleted:\n")
	if !ok || strings.Count(notDeleted, "  Paddington (2014)") != 5 {
		t.Errorf("want all 5 plays listed as not deleted:\n%s", out)
	}
}

// blockRecordOnDelete replaces the record with a directory when the first
// delete runs, so rewriting it fails until unblock removes the directory.
func (r *clearHistoryRun) blockRecordOnDelete(t *testing.T) (unblock func()) {
	t.Helper()
	var blocked []string
	r.pms.onDelete = func() {
		files, _ := filepath.Glob(filepath.Join(r.recordDir, "history-cleared-*.json"))
		for _, f := range files {
			if fi, err := os.Stat(f); err != nil || fi.IsDir() || os.Remove(f) != nil {
				continue
			}
			if err := os.MkdirAll(filepath.Join(f, "keep"), 0o755); err != nil {
				t.Error(err)
			}
			blocked = append(blocked, f)
		}
	}
	return func() {
		for _, f := range blocked {
			if err := os.RemoveAll(f); err != nil {
				t.Error(err)
			}
		}
	}
}

// A rewrite that fails once, as when antivirus holds the file, is retried.
func TestClearHistory_RecordUpdateRetried(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	unblock := r.blockRecordOnDelete(t)
	pauses := 0
	setBeforeFinalSave(t, func() { pauses++; unblock() })
	if err := r.run(t, "", "Heat (1995)"); err != nil {
		t.Fatal(err)
	}
	if recs := r.records(t); pauses != 1 || len(recs) != 1 || !recs[0].Complete {
		t.Errorf("pauses %d, records %+v; want one retry and a complete record", pauses, recs)
	}
}

// stubWriteAndClose replaces writeAndClose for one test.
func stubWriteAndClose(t *testing.T, f func(*os.File, []byte) error) {
	old := writeAndClose
	t.Cleanup(func() { writeAndClose = old })
	writeAndClose = f
}

// writeAndClose writes and closes the file, and reports the write's error
// rather than the close's that follows it.
func TestWriteAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAndClose(f, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "data" {
		t.Errorf("file = %q, %v; want data", got, err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("second close = %v; want the file closed", err)
	}

	f, err = os.Open(path) // read-only, so the write fails
	if err != nil {
		t.Fatal(err)
	}
	err = writeAndClose(f, []byte("more"))
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "write" {
		t.Errorf("err = %v; want the write's error", err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("second close = %v; want the file closed after a failed write", err)
	}
}

// A first write that fails partway leaves no file behind, and deletes nothing.
func TestClearHistory_TornRecordRemoved(t *testing.T) {
	stubWriteAndClose(t, func(f *os.File, data []byte) error {
		_, _ = f.Write(data[:len(data)/2])
		_ = f.Close()
		return &fs.PathError{Op: "write", Path: f.Name(), Err: errors.New("disk full")}
	})
	r := newClearHistoryRun(t, "y")
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "disk full") || strings.Contains(err.Error(), "couldn't be removed") || !strings.HasSuffix(err.Error(), "Nothing was deleted") {
		t.Fatalf("err = %v; want the write error and that nothing was deleted", err)
	}
	files, _ := filepath.Glob(filepath.Join(r.recordDir, "*"))
	if len(files) != 0 {
		t.Errorf("left %v", files)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v", r.pms.deleted)
	}
}

// A year-less entry Plex links to the movie dated in the target's year is
// that movie; one linked elsewhere is another movie.
func TestPlanHistory_RetitledMovie(t *testing.T) {
	plan := planMovies(t, "Movie (2018)",
		historyMovie{"Movie", 2018, "/library/metadata/1"},
		historyMovie{"Movie", 0, "/library/metadata/1"},
		historyMovie{"Movie", 0, "/library/metadata/2"},
		historyMovie{"Movie", 0, ""},
	)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("0", "1")) {
		t.Errorf("matched %v; want the 2018 movie, dated and not", got)
	}
	if len(plan.yearUnchecked) != 0 {
		t.Errorf("year unchecked %v; the key ties the year-less entry to the movie", plan.yearUnchecked)
	}
}

// Entries linked to the target's library item all match, whatever their
// year, a year-less one included.
func TestPlanHistory_YearlessLinkedToBothYears(t *testing.T) {
	plan := planMovies(t, "Movie (2018)",
		historyMovie{"Movie", 2018, "/library/metadata/1"},
		historyMovie{"Movie", 2015, "/library/metadata/1"},
		historyMovie{"Movie", 0, "/library/metadata/1"},
	)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("0", "1", "2")) {
		t.Errorf("matched %v; want every entry of the library item", got)
	}
	if len(plan.yearUnchecked) != 0 || len(plan.kept) != 0 || len(plan.severalYears) != 0 {
		t.Errorf("year unchecked %v, kept %v, several years %v; the key ties each entry to the movie", plan.yearUnchecked, plan.kept, plan.severalYears)
	}
}

// An entry with an in-range entry's key is that library item's play,
// whatever its release date; one with that other year and a different or
// empty key isn't.
func TestPlanHistory_KeyTiesOtherYear(t *testing.T) {
	plan := planMovies(t, "X (2019)",
		historyMovie{"X", 2019, "/library/metadata/7"},
		historyMovie{"X", 2023, "/library/metadata/7"},
		historyMovie{"X", 2023, "/library/metadata/8"},
		historyMovie{"X", 2023, ""},
	)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("0", "1")) {
		t.Errorf("matched %v; want the 2019 entry and the 2023 one with its key", got)
	}
}

// An unfinished file that can't be removed is reported, though withoutPaths
// keeps only the OS error.
func TestClearHistory_TornRecordRemovalFails(t *testing.T) {
	var reason string // why the remove fails
	stubWriteAndClose(t, func(f *os.File, data []byte) error {
		_ = f.Close()
		// A directory in its place: the remove fails, as when antivirus holds it.
		if err := os.Remove(f.Name()); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.Name(), "keep"), 0o755); err != nil {
			t.Fatal(err)
		}
		reason = withoutPaths(os.Remove(f.Name())).Error()
		return &fs.PathError{Op: "write", Path: f.Name(), Err: errors.New("disk full")}
	})
	r := newClearHistoryRun(t, "y")
	err := r.run(t, "", "Heat (1995)")
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "couldn't be removed: "+reason+"\n") || !strings.Contains(err.Error(), "Delete it") || !strings.HasSuffix(err.Error(), "Nothing was deleted") {
		t.Fatalf("err = %v; want the write error, the file left behind and that nothing was deleted", err)
	}
	if len(r.pms.deleted) != 0 {
		t.Errorf("deleted %v", r.pms.deleted)
	}
}

func TestLeftBehindNote(t *testing.T) {
	if note := leftBehindNote(nil); note != "" {
		t.Errorf("note without a remove error = %q", note)
	}
	denied := &fs.PathError{Op: "remove", Path: "secret-dir/x", Err: fs.ErrPermission}
	if note := leftBehindNote(denied); !strings.Contains(note, fs.ErrPermission.Error()) || strings.Contains(note, "secret-dir") {
		t.Errorf("note = %q; want the error without its path", note)
	}
}

// A file already gone when the write fails isn't reported as left behind.
func TestClearHistory_TornRecordAlreadyGone(t *testing.T) {
	stubWriteAndClose(t, func(f *os.File, data []byte) error {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return &fs.PathError{Op: "write", Path: f.Name(), Err: errors.New("disk full")}
	})
	r := newClearHistoryRun(t, "y")
	if err := r.run(t, "", "Heat (1995)"); err == nil || strings.Contains(err.Error(), "couldn't be removed") {
		t.Fatalf("err = %v; want the write error alone", err)
	}
}

// A year-less entry Plex links to a movie dated in another year is that
// movie, so it isn't taken on title alone.
func TestPlanHistory_YearlessLinkedToOtherYear(t *testing.T) {
	plan := planMovies(t, "Movie (2019)",
		historyMovie{"Movie", 2016, "/library/metadata/1"},
		historyMovie{"Movie", 0, "/library/metadata/1"},
		historyMovie{"Movie", 0, "/library/metadata/2"},
	)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("2")) {
		t.Errorf("matched %v; want only the unlinked year-less entry", got)
	}
	if !slices.Equal(plan.yearUnchecked, []string{"Movie (2019)"}) {
		t.Errorf("year unchecked %v; want the target", plan.yearUnchecked)
	}
}

// An empty key ties nothing: it isn't a movie's identity.
func TestPlanHistory_EmptyKeyDoesNotTie(t *testing.T) {
	plan := planMovies(t, "Movie (2018)",
		historyMovie{"Movie", 2018, ""},
		historyMovie{"Movie", 0, ""},
		historyMovie{"Movie", 0, "/library/metadata/2"},
	)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("0")) {
		t.Errorf("matched %v; want only the dated entry", got)
	}
	if len(plan.yearUnchecked) != 0 {
		t.Errorf("year unchecked %v", plan.yearUnchecked)
	}
}

// Without view times the first entry stands for the movie, with no date; a
// timed entry beats an untimed one.
func TestPlanHistory_LastWatched(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"Bar"})
	if err != nil {
		t.Fatal(err)
	}
	untimed := []historyEntry{moviePlay(1, 1, "Bar", ""), moviePlay(2, 1, "Bar", "")}
	for i := range untimed {
		untimed[i].ViewedAt = 0
	}
	last := planHistory(untimed, targets).movies[historyMovie{title: "Bar"}].last
	if last.HistoryKey != untimed[0].HistoryKey || describeLastWatched(last) != "" {
		t.Errorf("last watched %q (%q); want the first entry, undated", last.HistoryKey, describeLastWatched(last))
	}
	timed := moviePlay(3, 1, "Bar", "")
	last = planHistory(append(untimed, timed), targets).movies[historyMovie{title: "Bar"}].last
	if last.HistoryKey != timed.HistoryKey {
		t.Errorf("last watched %q; want the timed entry", last.HistoryKey)
	}
}

// Movies whose lines would read alike are told apart by key.
func TestClearHistory_IdenticalMovieLinesShowKeys(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	for i, key := range []string{"/library/metadata/1", "/library/metadata/2"} {
		e := moviePlay(40+i, 1, "Bar", "2010-01-01")
		e.Key, e.ViewedAt = key, 1_700_000_000
		r.pms.history = append(r.pms.history, e)
	}
	if err := r.run(t, "", "Bar"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	for _, want := range []string{"[/library/metadata/1]", "[/library/metadata/2]", "may be different movies"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// Movies whose titles differ, if only by year, aren't flagged as sharing one.
func TestClearHistory_DistinctTitlesNotFlagged(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	if err := r.run(t, "", "Heat"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	for _, want := range []string{"Heat (1986)", "Heat (1995)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "may be different movies") {
		t.Errorf("distinct titles flagged as shared:\n%s", out)
	}
}

// Titles that differ only in invisible characters, which matching ignores,
// are flagged as sharing a title and told apart by key.
func TestClearHistory_InvisiblyDifferentTitlesFlagged(t *testing.T) {
	r := newClearHistoryRun(t, "n")
	for i, title := range []string{"Bar", "Bar\u200b"} {
		e := moviePlay(50+i, 1, title, "2010-01-01")
		e.Key, e.ViewedAt = fmt.Sprintf("/library/metadata/%d", i+1), 1_700_000_000
		r.pms.history = append(r.pms.history, e)
	}
	if err := r.run(t, "", "Bar"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	for _, want := range []string{"[/library/metadata/1]", "[/library/metadata/2]", "may be different movies"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A title that ends in its year isn't labelled with it twice.
func TestMovieLabel(t *testing.T) {
	for _, tc := range []struct {
		m    historyMovie
		want string
	}{
		{historyMovie{title: "Heat", year: 1995}, "Heat (1995)"},
		{historyMovie{title: "Heat (1995)", year: 1995}, "Heat (1995)"},
		{historyMovie{title: "Heat"}, "Heat"},
		{historyMovie{title: "Heat (1994)", year: 1995}, "Heat (1994) (1995)"},
		{historyMovie{title: "Heat", year: unreadableYear}, "Heat"},
	} {
		if got := movieLabel(tc.m); got != tc.want {
			t.Errorf("movieLabel(%+v) = %q; want %q", tc.m, got, tc.want)
		}
	}
}

// Plan lines that read the same are told apart by key, and one with no key
// says so.
func TestPrintHistoryPlan_SameLineNamesKeys(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"Bar"})
	if err != nil {
		t.Fatal(err)
	}
	keyed, keyless := moviePlay(1, 1, "Bar", ""), moviePlay(2, 1, "Bar", "")
	keyed.Key = "/library/metadata/5"
	var out strings.Builder
	printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, planHistory([]historyEntry{keyed, keyless}, targets))
	for _, want := range []string{
		"  Bar                                      1 play(s), last watched " + localDay(1_700_000_000) + " [/library/metadata/5]\n",
		"  Bar                                      1 play(s), last watched " + localDay(1_700_000_000) + " [no key]\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}

// An unreadable date with an in-range entry's key is that movie's play; with
// only another year's key it is that movie's, so neither is kept.
func TestPlanHistory_UnreadableDateTiedByKey(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"Heat (1995)"})
	if err != nil {
		t.Fatal(err)
	}
	play := func(id int, date, key string) historyEntry {
		e := moviePlay(id, 1, "Heat", date)
		e.Key = key
		return e
	}
	plan := planHistory([]historyEntry{
		play(1, "1995-12-15", "/library/metadata/1"),
		play(2, "not a date", "/library/metadata/1"),
		play(3, "1986-03-14", "/library/metadata/2"),
		play(4, "not a date", "/library/metadata/2"),
	}, targets)
	if got := keysOf(plan.matched); !slices.Equal(got, keys("1", "2")) {
		t.Errorf("matched %v; want the 1995 entry and the unreadable one with its key", got)
	}
	if len(plan.kept) != 0 || len(plan.yearUnchecked) != 0 {
		t.Errorf("kept %v, year unchecked %v; each key says which movie", plan.kept, plan.yearUnchecked)
	}
}

// No date and an unreadable one both print as the bare title, so they fold
// together: the plan warns, and names each key.
func TestPrintHistoryPlan_UnreadableAndNoDateFoldTogether(t *testing.T) {
	targets, err := parseHistoryTargets([]string{"X"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := moviePlay(1, 1, "X", ""), moviePlay(2, 1, "X", "not a date")
	a.Key, b.Key = "/library/metadata/1", "/library/metadata/2"
	var out strings.Builder
	printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, planHistory([]historyEntry{a, b}, targets))
	for _, want := range []string{"[/library/metadata/1]", "[/library/metadata/2]", "Titles that repeat"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}

// Kept lines that would read the same name their keys too.
func TestPrintHistoryPlan_KeptLinesNameKeys(t *testing.T) {
	plan := planMovies(t, "Solaris (2002)",
		historyMovie{"Solaris", 2002, "/library/metadata/3"},
		historyMovie{"Solaris", 0, "/library/metadata/1"},
		historyMovie{"Solaris", 0, ""},
	)
	var out strings.Builder
	printHistoryPlan(&out, plexAccount{ID: 1, Name: "QuienNZ"}, plan)
	for _, want := range []string{"with no release date [/library/metadata/1]\n", "with no release date [no key]\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}

// Through a whole run, a kept play is listed but neither asked about,
// recorded nor deleted; with only kept plays, nothing is asked.
func TestClearHistory_KeptIsNotDeleted(t *testing.T) {
	r := newClearHistoryRun(t, "y")
	r.pms.history = append(r.pms.history, moviePlay(20, 1, "Solaris", "2002-01-01"), moviePlay(21, 1, "Solaris", ""))
	if err := r.run(t, "", "Solaris (2002)"); err != nil {
		t.Fatal(err)
	}
	if want := keys("20"); !slices.Equal(r.pms.deleted, want) {
		t.Errorf("deleted %v; want %v", r.pms.deleted, want)
	}
	if recs := r.records(t); len(recs) != 1 || !slices.Equal(keysOf(recs[0].Server), keys("20")) {
		t.Errorf("records %+v; want one listing only entry 20", recs)
	}
	if len(r.prompter.asked) != 1 || !strings.Contains(r.prompter.asked[0], "Delete these 1 history entries") {
		t.Errorf("asked %q", r.prompter.asked)
	}
	if !strings.Contains(r.out.String(), "Not deleting these, which may be other movies") {
		t.Errorf("output lacks the kept list:\n%s", r.out.String())
	}

	r = newClearHistoryRun(t)
	r.pms.history = []historyEntry{moviePlay(21, 1, "Solaris", "not a date")}
	if err := r.run(t, "", "Solaris (2002)"); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	if i, j := strings.Index(out, "Not deleting these"), strings.Index(out, "Nothing to clear."); i < 0 || j < i {
		t.Errorf("want the kept list, then nothing to clear:\n%s", out)
	}
	if len(r.prompter.asked) != 0 {
		t.Errorf("asked %q; want no prompt", r.prompter.asked)
	}
	r.assertUntouched(t)
}
