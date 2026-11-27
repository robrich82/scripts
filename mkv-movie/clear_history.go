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

// historyTarget is a movie to clear from the watch history.
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

// historyTitleKey folds a movie title for comparison: lower case, with the
// letters, numbers (Ⅱ and ② too), marks (Thai and Indic vowel signs) and
// symbols (°, ∀ and + tell titles apart) of every script kept. Punctuation,
// spaces, dash lookalikes, modifier symbols and the spacing modifier letters
// (U+02B0–02FF) are dropped. normalizePlexTitle keeps only ASCII, which is
// too loose here: whatever matches gets deleted.
func historyTitleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= '！' && r <= '～' {
			r -= '！' - '!' // full width mirrors ASCII: ＢＬＥＡＣＨ is BLEACH
		}
		// Modifier letters (U+02B0–02FF) and symbols (Sk) stand in for
		// apostrophes, as in Hawaiʻi and Hawai`i.
		modifier := (r >= 0x2B0 && r <= 0x2FF) || unicode.Is(unicode.Sk, r)
		symbol := unicode.IsSymbol(r) && !strings.ContainsRune(dashLookalikes, r)
		if !modifier && (unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || symbol) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dashLookalikes are the symbols that stand in for a dash in titles: tildes,
// minus signs and box-drawing lines. Dashes proper are punctuation.
const dashLookalikes = "~∼−─━➖⁻₋"

func parseHistoryTargets(args []string) ([]historyTarget, error) {
	var targets []historyTarget
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		title, year := splitTitleYear(raw)
		key := historyTitleKey(title)
		if key == "" {
			return nil, fmt.Errorf("--clear-history needs a movie title, got %q", raw)
		}
		targets = append(targets, historyTarget{raw: raw, key: key, year: year})
	}
	return targets, nil
}

// clearHistoryDeps are the side effects of clearHistory.
type clearHistoryDeps struct {
	server    HistoryClient
	owner     OwnerLookup
	prompt    func(string) (string, error)
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
	// Complete is false until the run finishes and the record is rewritten,
	// so a record left false is from an interrupted run (some of Server may
	// remain) or one whose final rewrite failed.
	Complete bool `json:"complete"`
	// Failed lists the entries not deleted: those whose delete failed, and
	// those not tried after the run stopped.
	Failed []string `json:"failed,omitempty"`
}

// maxConsecutiveDeleteFailures stops a run against a server that has gone
// away, where each delete would wait out the full timeout.
const maxConsecutiveDeleteFailures = 3

// clearHistory deletes an account's server watch history for the target
// movies, after listing the matches and asking. account is a server account
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

	ans, err := d.prompt(fmt.Sprintf("\nDelete these %d history entries for %s (account %d)? This can't be undone. [y/N]: ", len(plan.matched), acct.Name, acct.ID))
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
	if rmErr, err := writeNewHistoryRecord(recordPath, record); err != nil {
		return fmt.Errorf("saving %s: %v\n%sNothing was deleted", recordPath, withoutPaths(err), leftBehindNote(rmErr))
	}
	fmt.Fprintf(d.out, "Recorded the entries in %s\n", recordPath)

	record.Failed = deleteHistoryEntries(d.server, d.out, plan.matched)
	record.Complete = true
	deleted, total := len(plan.matched)-len(record.Failed), len(plan.matched)
	fmt.Fprintf(d.out, "Deleted %d of %d history entries.\n", deleted, total)

	var problems []error
	updateErr := updateHistoryRecord(recordPath, record)
	if updateErr != nil {
		problems = append(problems, fmt.Errorf("%d of %d history entries were deleted, but %s couldn't be updated to say so: %v\nIts \"complete\" stays false",
			deleted, total, recordPath, withoutPaths(updateErr)))
	}
	if len(record.Failed) > 0 {
		where := fmt.Sprintf("%s lists them under \"failed\"", recordPath)
		if updateErr != nil {
			// The record can't list them, and untried ones haven't been named.
			printNotDeleted(d.out, record, plan.matched)
			where = "the output above names them"
		}
		problems = append(problems, fmt.Errorf("%d history entries were not deleted; %s", len(record.Failed), where))
	}
	return errors.Join(problems...)
}

// printNotDeleted names the entries of r.Failed.
func printNotDeleted(out io.Writer, r historyRecord, entries []historyEntry) {
	fmt.Fprintln(out, "Not deleted:")
	for _, e := range entries {
		if slices.Contains(r.Failed, e.HistoryKey) {
			fmt.Fprintf(out, "  %s\n", describeHistoryEntry(e))
		}
	}
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

// historyMovie is a movie in the history: its title as its entries recorded
// it, its year, and its key. Two movies can share a title, and an entry of a
// movie no longer in the library has no key.
type historyMovie struct {
	title string
	year  int // entryYear's
	key   string
}

// entryYear is the year of e's release date, or of a "(Year)" its title
// ends in, or 0 when it has neither.
func entryYear(e historyEntry) int {
	if d, err := time.Parse("2006-01-02", e.OriginallyAvailableAt); err == nil && d.Year() > 0 {
		return d.Year()
	}
	_, year := splitTitleYear(e.Title)
	return year
}

func movieOf(e historyEntry) historyMovie {
	return historyMovie{title: e.Title, year: entryYear(e), key: e.Key}
}

// movieSummary is a movie's matched plays, and the one watched last.
type movieSummary struct {
	plays int
	last  historyEntry
}

// historyPlan is what a run would delete, and what it couldn't match.
type historyPlan struct {
	matched       []historyEntry
	movies        map[historyMovie]*movieSummary
	missing       []string // targets that matched nothing
	yearUnchecked []string // targets that matched an entry with no year
}

// planHistory picks the movie entries a target names, each once. A target's
// year must be within one of the entry's, as for FindMovieByTitle, when both
// have one. An entry without a year matches too, unless the history also has
// the target's title with a year in range: the year-less one is then another
// movie. A year-less entry whose key a dated one also has is that movie: it
// matches when that one does.
func planHistory(entries []historyEntry, targets []historyTarget) historyPlan {
	type movieInfo struct {
		historyMovie
		titleKey string // historyTitleKey of the title
	}
	var movies []movieInfo
	seen := make(map[historyMovie]bool)
	for _, e := range entries {
		if e.Type != "movie" {
			continue
		}
		if m := movieOf(e); !seen[m] {
			seen[m] = true
			title, _ := splitTitleYear(m.title)
			movies = append(movies, movieInfo{m, historyTitleKey(title)})
		}
	}

	plan := historyPlan{movies: make(map[historyMovie]*movieSummary)}
	take := make(map[historyMovie]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue // a repeat: the same raw is the same target
		}
		listed[t.raw] = true
		inRange := func(year int) bool { return abs(year-t.year) <= 1 }
		// The keys of the dated movies with the target's title, in range and
		// not: a year-less entry with one of them is that movie.
		exactKeys, otherKeys := make(map[string]bool), make(map[string]bool)
		exactYear := false
		if t.year != 0 {
			for _, m := range movies {
				if m.titleKey != t.key || m.year == 0 {
					continue
				}
				keys := otherKeys
				if inRange(m.year) {
					exactYear, keys = true, exactKeys
				}
				if m.key != "" {
					keys[m.key] = true
				}
			}
		}
		found, unchecked := false, false
		for _, m := range movies {
			if m.titleKey != t.key {
				continue
			}
			switch {
			case t.year == 0 || (m.year != 0 && inRange(m.year)):
			case m.year != 0:
				continue
			case exactKeys[m.key]:
			case otherKeys[m.key] || exactYear:
				continue
			default:
				unchecked = true
			}
			take[m.historyMovie] = true
			found = true
		}
		if !found {
			plan.missing = append(plan.missing, t.raw)
		} else if unchecked {
			plan.yearUnchecked = append(plan.yearUnchecked, t.raw)
		}
	}
	for _, e := range entries {
		m := movieOf(e)
		if e.Type != "movie" || !take[m] {
			continue
		}
		plan.matched = append(plan.matched, e)
		sum := plan.movies[m]
		if sum == nil {
			sum = &movieSummary{last: e}
			plan.movies[m] = sum
		}
		sum.plays++
		if e.ViewedAt > sum.last.ViewedAt {
			sum.last = e
		}
	}
	return plan
}

// movieLabel is m's title, with its year when the title doesn't end in it.
func movieLabel(m historyMovie) string {
	if _, y := splitTitleYear(m.title); m.year != 0 && y != m.year {
		return fmt.Sprintf("%s (%d)", m.title, m.year)
	}
	return m.title
}

// printHistoryPlan lists each matched movie with its play count and when it
// was last watched, since a target can match more than one movie, and the
// targets that matched nothing.
func printHistoryPlan(out io.Writer, acct plexAccount, plan historyPlan) {
	fmt.Fprintf(out, "Watch history for %s (account %d) on the Plex server:\n", acct.Name, acct.ID)
	movies := slices.SortedFunc(maps.Keys(plan.movies), func(a, b historyMovie) int {
		return cmp.Or(strings.Compare(a.title, b.title), cmp.Compare(a.year, b.year), strings.Compare(a.key, b.key))
	})
	// Titles are compared as matching folds them, so ones that differ only
	// in what matching ignores count as one.
	perTitle, perLine := make(map[string]int), make(map[string]int)
	lines, lineIDs := make([]string, len(movies)), make([]string, len(movies))
	for i, m := range movies {
		sum := plan.movies[m]
		last := describeLastWatched(sum.last)
		t, _ := splitTitleYear(m.title)
		folded := historyTitleKey(t) + "\x00" + strconv.Itoa(m.year)
		lines[i] = fmt.Sprintf("  %-40s %d play(s)%s", movieLabel(m), sum.plays, last)
		lineIDs[i] = fmt.Sprintf("%s\x00%d\x00%s", folded, sum.plays, last)
		perTitle[folded]++
		perLine[lineIDs[i]]++
	}
	for i, line := range lines {
		if perLine[lineIDs[i]] > 1 {
			line += " [" + cmp.Or(movies[i].key, "no key") + "]" // nothing else tells them apart
		}
		fmt.Fprintln(out, line)
	}
	if len(perTitle) < len(movies) {
		fmt.Fprintln(out, "Titles that repeat, or that matching counts as the same, may be different movies: check each before answering.")
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "Some history entries record no release date, so these matched them on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if len(plan.missing) > 0 {
		fmt.Fprintln(out, "Not in this history:")
		for _, m := range plan.missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

func describeHistoryEntry(e historyEntry) string {
	return movieLabel(movieOf(e)) + describeLastWatched(e)
}

// describeLastWatched is ", last watched <date>", or "" for an entry with
// no date.
func describeLastWatched(e historyEntry) string {
	if e.ViewedAt <= 0 {
		return ""
	}
	return ", last watched " + time.Unix(e.ViewedAt, 0).Format("2006-01-02")
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

// withoutPaths drops the file names a failed write's error carries: the
// message names the record already.
func withoutPaths(err error) error {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// leftBehindNote is the lines saying the unfinished record couldn't be
// removed, or "" when rmErr is nil.
func leftBehindNote(rmErr error) string {
	if rmErr == nil {
		return ""
	}
	return fmt.Sprintf("The unfinished file couldn't be removed: %v\nDelete it: it's not a record of deleted entries.\n", withoutPaths(rmErr))
}

// writeNewHistoryRecord writes record to path, never over an earlier one.
// It creates the file exclusively and writes in place, since a rename could
// replace a record another run wrote in between. A torn write can only
// happen before any delete. rmErr, set only with err, is why the unfinished
// file couldn't be removed: it's returned apart from err because withoutPaths
// would drop a message wrapped around err.
func writeNewHistoryRecord(path string, record historyRecord) (rmErr, err error) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil, errors.New("an earlier record has this name")
	}
	if err != nil {
		return nil, err
	}
	if err := writeAndClose(f, data); err != nil {
		rmErr := os.Remove(path)
		if errors.Is(rmErr, fs.ErrNotExist) {
			rmErr = nil // already gone: nothing left behind
		}
		return rmErr, err
	}
	return nil, nil
}

// writeAndClose writes data to f, flushes it to disk and closes f, returning
// the first error; tests replace it.
var writeAndClose = func(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// updateHistoryRecord replaces the record at path, retrying once after a
// pause like the cache's final save (a briefly locked file on Windows).
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
		server:    newHTTPPlexMovieClient(plexURL, plexToken),
		owner:     newPlexTVClient(plexTVBaseURL, plexToken),
		prompt:    stdinPrompter,
		recordDir: cfgDir,
		out:       os.Stdout,
		now:       time.Now,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
