package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// stringList is a flag that can be given more than once.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }

func (l *stringList) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// historyTarget is a show to clear from the watch history.
type historyTarget struct {
	raw  string
	key  string // historyTitleKey of the title
	year int    // 0 when not given
}

// \p{Z} as well as \s, and full-width brackets: a CJK title may use an
// ideographic space and （） before its year.
var trailingYear = regexp.MustCompile(`^(.*?)[\s\p{Z}]*[(（](\d{4})[)）][\s\p{Z}]*$`)

// splitTitleYear splits "Title (2018)" into "Title" and 2018. A title
// without a trailing year comes back whole, with year 0.
func splitTitleYear(s string) (string, int) {
	s = strings.TrimSpace(s)
	m := trailingYear.FindStringSubmatch(s)
	if m == nil {
		return s, 0
	}
	year, _ := strconv.Atoi(m[2])
	if year == 0 {
		return s, 0 // "(0000)" isn't a year, and year 0 would match any
	}
	return m[1], year
}

// historyTitleKey folds a show title for comparison: lower case, with the
// letters, numbers (Ⅱ and ② too), marks (Thai and Indic vowel signs) and
// symbols (Gintama° is another series) of every script kept, and punctuation
// and spaces dropped. titleMatcher keeps only ASCII, which is too loose here:
// whatever matches gets deleted.
func historyTitleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= '！' && r <= '～' {
			r -= '！' - '!' // full width mirrors ASCII: ＢＬＥＡＣＨ is BLEACH
		}
		// Spacing modifiers stand in for apostrophes: Hawaiʻi and Hawai`i are
		// Hawai'i.
		keep := unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) ||
			unicode.In(r, unicode.Sm, unicode.Sc, unicode.So)
		if keep && (r < 0x2B0 || r > 0x2FF) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseHistoryTargets(args []string) ([]historyTarget, error) {
	var targets []historyTarget
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		title, year := splitTitleYear(raw)
		key := historyTitleKey(title)
		if key == "" {
			return nil, fmt.Errorf("--clear-history needs a show title, got %q", raw)
		}
		targets = append(targets, historyTarget{raw: raw, key: key, year: year})
	}
	return targets, nil
}

// clearHistoryDeps are the side effects of clearHistory.
type clearHistoryDeps struct {
	server    HistoryClient
	owner     OwnerLookup
	prompt    Prompter
	recordDir string // where the record of deleted entries is written
	out       io.Writer
	now       func() time.Time
}

// historyRecord is the file written before any entry is deleted: the
// server can't restore deleted history, so this is the only trace of it.
type historyRecord struct {
	Account   string         `json:"account"`
	AccountID int            `json:"accountID"`
	Targets   []string       `json:"targets"`
	Server    []historyEntry `json:"server"`
	// Complete is false until the run finishes, so a record left false is
	// from an interrupted run and some of Server may remain.
	Complete bool `json:"complete"`
	// Failed lists the entries not deleted: those whose delete failed, and
	// those not tried after the run stopped.
	Failed []string `json:"failed,omitempty"`
}

// maxConsecutiveDeleteFailures stops a run against a server that has gone
// away, where each delete would wait out the full timeout.
const maxConsecutiveDeleteFailures = 3

// clearHistory deletes an account's server watch history for the target
// shows, after listing the matches and asking. account is a server account
// name; "" means the PLEX_TOKEN owner.
func clearHistory(args []string, account string, d clearHistoryDeps) error {
	targets, err := parseHistoryTargets(args)
	if err != nil {
		return err
	}
	acct, err := resolveHistoryAccount(d.server, d.owner, account)
	if err != nil {
		return fmt.Errorf("%w; nothing was deleted", err)
	}
	entries, err := d.server.WatchHistory(acct.ID)
	if err != nil {
		return fmt.Errorf("%w; nothing was deleted", err)
	}
	for _, e := range entries {
		// Deletes are irreversible, so don't rely on the server's filter alone.
		if e.AccountID != acct.ID {
			return fmt.Errorf("the server returned account %d's history when asked for %s's (%d); nothing was deleted", e.AccountID, acct.Name, acct.ID)
		}
	}

	plan := planHistory(entries, targets)
	printHistoryPlan(d.out, acct, plan)
	if len(plan.matched) == 0 {
		fmt.Fprintln(d.out, "Nothing to clear.")
		return nil
	}

	ans, err := d.prompt.Ask(fmt.Sprintf("\nDelete these %d history entries for %s (account %d)? This can't be undone. [y/N]: ", len(plan.matched), acct.Name, acct.ID))
	if err != nil {
		return fmt.Errorf("reading the answer: %w; nothing was deleted", err)
	}
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Fprintln(d.out, "Aborted; no changes made.")
		return nil
	}

	record := historyRecord{Account: acct.Name, AccountID: acct.ID, Server: plan.matched}
	for _, t := range targets {
		record.Targets = append(record.Targets, t.raw)
	}
	recordPath := historyRecordPath(d.recordDir, d.now())
	if err := writeNewHistoryRecord(recordPath, record); err != nil {
		return fmt.Errorf("saving %s: %v\n%sNothing was deleted", recordPath, withoutPaths(err), hintLine(err))
	}
	fmt.Fprintf(d.out, "Recorded the entries in %s\n", recordPath)

	record.Failed = deleteHistoryEntries(d.server, d.out, plan.matched)
	record.Complete = true
	deleted, total := len(plan.matched)-len(record.Failed), len(plan.matched)
	fmt.Fprintf(d.out, "Deleted %d of %d history entries.\n", deleted, total)
	if err := updateHistoryRecord(recordPath, record); err != nil {
		return fmt.Errorf("%d of %d history entries were deleted, but %s couldn't be updated to say so: %v\n%sIts \"complete\" stays false",
			deleted, total, recordPath, withoutPaths(err), hintLine(err))
	}
	if len(record.Failed) > 0 {
		return fmt.Errorf("%d history entries were not deleted; %s lists them under \"failed\"", len(record.Failed), recordPath)
	}
	return nil
}

// resolveHistoryAccount finds the server account to clear: the one named
// account, or the PLEX_TOKEN owner's, found by plex.tv username.
func resolveHistoryAccount(server HistoryClient, owner OwnerLookup, account string) (plexAccount, error) {
	accounts, err := server.Accounts()
	if err != nil {
		return plexAccount{}, err
	}
	var names []string
	for _, a := range accounts {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return plexAccount{}, errors.New("the Plex server's /accounts listed no named accounts")
	}
	list := strings.Join(names, ", ")
	name := account
	if name == "" {
		user, err := owner.User()
		if err != nil {
			return plexAccount{}, fmt.Errorf("finding the PLEX_TOKEN owner: %w; pass --account (one of: %s)", err, list)
		}
		name = user.Username
	}
	var exact, folded []plexAccount
	for _, a := range accounts {
		switch {
		case name == "": // an empty owner name is no one's, not the unnamed account's
		case a.Name == name:
			exact = append(exact, a)
		case strings.EqualFold(a.Name, name):
			folded = append(folded, a)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return plexAccount{}, fmt.Errorf("%d server accounts are named %q (ids %s); rename one in Plex so they can be told apart", len(exact), name, accountIDs(exact))
	case len(folded) == 1:
		return folded[0], nil
	case len(folded) > 1:
		return plexAccount{}, fmt.Errorf("more than one server account is named %q but for case; give the name exactly (one of: %s)", name, list)
	case account == "":
		return plexAccount{}, fmt.Errorf("the PLEX_TOKEN owner %q has no account on this server; pass --account (one of: %s)", name, list)
	default:
		return plexAccount{}, fmt.Errorf("no server account named %q; the accounts are: %s", account, list)
	}
}

func accountIDs(accounts []plexAccount) string {
	var ids []string
	for _, a := range accounts {
		ids = append(ids, strconv.Itoa(a.ID))
	}
	return strings.Join(ids, ", ")
}

// historyShow is a show in the history: its title as each entry recorded it,
// and its key. Plex titles two shows alike when only their years differ, and
// a retitled show's older entries keep the old title.
type historyShow struct{ title, key string }

// showSummary is a show's matched episodes, and the one watched last.
type showSummary struct {
	episodes int
	last     historyEntry
}

// historyPlan is what a run would delete, and what it couldn't match.
type historyPlan struct {
	matched       []historyEntry
	shows         map[historyShow]*showSummary
	missing       []string // targets that matched nothing
	yearUnchecked []string // targets that matched a title with no year
}

// planHistory picks the episode entries whose show is a target, each once.
// A target's year must equal the history title's when both have one. A
// history title without a year matches too, unless the history also has the
// target's title with its year: the year-less one is then another show,
// except where its key is that show's (the show was retitled).
func planHistory(entries []historyEntry, targets []historyTarget) historyPlan {
	type titleInfo struct {
		key   string // historyTitleKey of the title
		year  int
		shows []historyShow
	}
	titles := make(map[string]*titleInfo)
	seenShow := make(map[historyShow]bool)
	for _, e := range entries {
		if e.Type != "episode" {
			continue
		}
		ti := titles[e.GrandparentTitle]
		if ti == nil {
			title, year := splitTitleYear(e.GrandparentTitle)
			ti = &titleInfo{key: historyTitleKey(title), year: year}
			titles[e.GrandparentTitle] = ti
		}
		if s := (historyShow{e.GrandparentTitle, e.GrandparentKey}); !seenShow[s] {
			seenShow[s] = true
			ti.shows = append(ti.shows, s)
		}
	}

	plan := historyPlan{shows: make(map[historyShow]*showSummary)}
	take := make(map[historyShow]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue // a repeat: the same raw is the same target
		}
		listed[t.raw] = true
		exactKeys := make(map[string]bool) // keys of the shows titled with the target's year
		exactYear := false
		if t.year != 0 {
			for _, ti := range titles {
				if ti.key == t.key && ti.year == t.year {
					exactYear = true
					for _, s := range ti.shows {
						if s.key != "" {
							exactKeys[s.key] = true
						}
					}
				}
			}
		}
		found, unchecked := false, false
		for _, ti := range titles {
			if ti.key != t.key {
				continue
			}
			for _, s := range ti.shows {
				switch {
				case t.year == 0 || ti.year == t.year:
				case ti.year == 0 && !exactYear:
					unchecked = true
				case ti.year == 0 && exactKeys[s.key]:
				default:
					continue
				}
				take[s] = true
				found = true
			}
		}
		if !found {
			plan.missing = append(plan.missing, t.raw)
		} else if unchecked {
			plan.yearUnchecked = append(plan.yearUnchecked, t.raw)
		}
	}
	for _, e := range entries {
		s := historyShow{e.GrandparentTitle, e.GrandparentKey}
		if e.Type != "episode" || !take[s] {
			continue
		}
		plan.matched = append(plan.matched, e)
		sum := plan.shows[s]
		if sum == nil {
			sum = &showSummary{last: e}
			plan.shows[s] = sum
		}
		sum.episodes++
		if e.ViewedAt > sum.last.ViewedAt {
			sum.last = e
		}
	}
	return plan
}

// printHistoryPlan lists the matched entries per show, since a target can
// match more than one, and the targets that matched nothing.
func printHistoryPlan(out io.Writer, acct plexAccount, plan historyPlan) {
	fmt.Fprintf(out, "Watch history for %s (account %d) on the Plex server:\n", acct.Name, acct.ID)
	shows := slices.SortedFunc(maps.Keys(plan.shows), func(a, b historyShow) int {
		return cmp.Or(strings.Compare(a.title, b.title), strings.Compare(a.key, b.key))
	})
	perTitle := make(map[string]int)
	for _, s := range shows {
		perTitle[s.title]++
	}
	shared := false
	for _, s := range shows {
		shared = shared || perTitle[s.title] > 1
		sum := plan.shows[s]
		// The last episode watched tells apart two shows with one title.
		fmt.Fprintf(out, "  %-40s %d episode(s), last watched %s\n", s.title, sum.episodes, describeLastWatched(sum.last))
	}
	if shared {
		fmt.Fprintln(out, "A title listed more than once is more than one show in Plex (or a show added again): check each before answering.")
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "History doesn't record a show's year, so these matched on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if len(plan.missing) > 0 {
		fmt.Fprintln(out, "Not in this history:")
		for _, m := range plan.missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

func describeHistoryEntry(e historyEntry) string {
	return fmt.Sprintf("%s S%02dE%02d %q", e.GrandparentTitle, e.ParentIndex, e.Index, e.Title)
}

func describeLastWatched(e historyEntry) string {
	s := fmt.Sprintf("S%02dE%02d %q", e.ParentIndex, e.Index, e.Title)
	if e.ViewedAt > 0 {
		s += " on " + time.Unix(e.ViewedAt, 0).Format("2006-01-02")
	}
	return s
}

// deleteHistoryEntries deletes each entry and returns the keys of those it
// didn't. It gives up after maxConsecutiveDeleteFailures failures in a row.
func deleteHistoryEntries(server HistoryClient, out io.Writer, entries []historyEntry) []string {
	var failed []string
	consecutive := 0
	for i, e := range entries {
		err := server.DeleteHistory(e.HistoryKey)
		if err == nil {
			consecutive = 0
			continue
		}
		fmt.Fprintf(out, "  failed: %s: %v\n", describeHistoryEntry(e), err)
		failed = append(failed, e.HistoryKey)
		consecutive++
		if consecutive == maxConsecutiveDeleteFailures {
			rest := entries[i+1:]
			if len(rest) > 0 {
				fmt.Fprintf(out, "Stopped after %d failures in a row; %d entries weren't tried.\n", consecutive, len(rest))
			}
			for _, r := range rest {
				failed = append(failed, r.HistoryKey)
			}
			break
		}
	}
	return failed
}

func historyRecordPath(dir string, now time.Time) string {
	return filepath.Join(dir, "history-cleared-"+now.UTC().Format("20060102T150405Z")+".json")
}

// writeNewHistoryRecord writes record to path, never over an earlier one.
// It creates the file exclusively and writes in place, since a rename could
// replace a record another run wrote in between. A torn write can only
// happen before any delete.
func writeNewHistoryRecord(path string, record historyRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return errors.New("an earlier record has this name")
	}
	if err != nil {
		return err
	}
	if err := writeAndClose(f, data); err != nil {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return fmt.Errorf("%w; the unfinished file is left behind", err)
		}
		return err
	}
	return nil
}

// writeAndClose writes data to f, flushes it to disk and closes f; tests
// replace it.
var writeAndClose = func(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// updateHistoryRecord replaces the record at path, trying again once like
// cacheSaver: on Windows the rename fails while another program (antivirus,
// an indexer) briefly holds the file open.
func updateHistoryRecord(path string, record historyRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if atomicWriteFile(path, data) == nil {
		return nil
	}
	beforeFinalSave()
	return atomicWriteFile(path, data)
}

// runClearHistory is --clear-history: it exits 1 on any error.
func runClearHistory(args []string, account string) {
	plexURL, plexToken := os.Getenv("PLEX_URL"), os.Getenv("PLEX_TOKEN")
	if plexURL == "" || plexToken == "" {
		fmt.Fprintln(os.Stderr, "Error: --clear-history deletes Plex watch history: set PLEX_URL and PLEX_TOKEN.")
		os.Exit(1)
	}
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}
	err = clearHistory(args, account, clearHistoryDeps{
		server:    newHTTPPlexClient(plexURL, plexToken),
		owner:     newPlexTVClient(plexTVBaseURL, plexToken),
		prompt:    newStdinPrompter(),
		recordDir: cfgDir,
		out:       os.Stdout,
		now:       time.Now,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
