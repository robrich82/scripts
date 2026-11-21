package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
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
	raw     string
	year    int // 0 when not given
	matches func(string) bool
}

// \p{Z} as well as \s: a CJK title may use an ideographic space before its year.
var trailingYear = regexp.MustCompile(`^(.*?)[\s\p{Z}]*\((\d{4})\)[\s\p{Z}]*$`)

// splitTitleYear splits "Title (2018)" into "Title" and 2018. A title
// without a trailing year comes back whole, with year 0.
func splitTitleYear(s string) (string, int) {
	s = strings.TrimSpace(s)
	m := trailingYear.FindStringSubmatch(s)
	if m == nil {
		return s, 0
	}
	year, _ := strconv.Atoi(m[2])
	return m[1], year
}

func parseHistoryTargets(args []string) ([]historyTarget, error) {
	var targets []historyTarget
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		title, year := splitTitleYear(raw)
		// titleMatcher expects a cleaned query, as FindShowByTitle's callers pass.
		matches := titleMatcher(cleanSearchQuery(title))
		if matches == nil {
			return nil, fmt.Errorf("--clear-history needs a show title, got %q", raw)
		}
		targets = append(targets, historyTarget{raw: raw, year: year, matches: matches})
	}
	return targets, nil
}

// matchesShow reports whether a history entry's show title is this target.
// History entries carry no show year, but some titles end in one, e.g.
// "Bluey (2018)"; when both sides have a year they must agree.
func (t historyTarget) matchesShow(showTitle string) bool {
	title, year := splitTitleYear(showTitle)
	if !t.matches(title) {
		return false
	}
	return t.year == 0 || year == 0 || t.year == year
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
	Account string         `json:"account"`
	Targets []string       `json:"targets"`
	Server  []historyEntry `json:"server"`
	// Failed lists the history keys still on the server after the run.
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
	acct, err := resolveHistoryAccount(d, account)
	if err != nil {
		return err
	}
	entries, err := d.server.WatchHistory(acct.ID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Deletes are irreversible, so don't rely on the server's filter alone.
		if e.AccountID != acct.ID {
			return fmt.Errorf("the server returned account %d's history when asked for %s's (%d); nothing was deleted", e.AccountID, acct.Name, acct.ID)
		}
	}

	matched := matchHistory(entries, targets)
	printHistoryPlan(d.out, acct.Name, targets, matched)
	if len(matched) == 0 {
		fmt.Fprintln(d.out, "Nothing to clear.")
		return nil
	}

	ans, err := d.prompt.Ask(fmt.Sprintf("\nDelete these %d history entries for %s? This can't be undone. [y/N]: ", len(matched), acct.Name))
	if err != nil {
		return fmt.Errorf("reading the answer: %w; nothing was deleted", err)
	}
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Fprintln(d.out, "Aborted; no changes made.")
		return nil
	}

	record := historyRecord{Account: acct.Name, Server: matched}
	for _, t := range targets {
		record.Targets = append(record.Targets, t.raw)
	}
	recordPath, err := writeHistoryRecord(d.recordDir, d.now(), record)
	if err != nil {
		return fmt.Errorf("writing the record of entries to delete: %w; nothing was deleted", err)
	}
	fmt.Fprintf(d.out, "Recorded the entries in %s\n", recordPath)

	deleted, consecutive := 0, 0
	for i, e := range matched {
		if err := d.server.DeleteHistory(e.HistoryKey); err != nil {
			fmt.Fprintf(d.out, "  failed: %s: %v\n", describeHistoryEntry(e), err)
			record.Failed = append(record.Failed, e.HistoryKey)
			if consecutive++; consecutive == maxConsecutiveDeleteFailures {
				for _, rest := range matched[i+1:] {
					record.Failed = append(record.Failed, rest.HistoryKey)
				}
				fmt.Fprintf(d.out, "Stopped after %d failures in a row.\n", consecutive)
				break
			}
			continue
		}
		deleted++
		consecutive = 0
	}
	fmt.Fprintf(d.out, "Done. Deleted %d of %d history entries.\n", deleted, len(matched))
	if len(record.Failed) == 0 {
		return nil
	}
	if err := writeRecordFile(recordPath, record); err != nil {
		return fmt.Errorf("%d history entries were not deleted, and %s couldn't be updated to say which: %w", len(record.Failed), recordPath, err)
	}
	return fmt.Errorf("%d history entries were not deleted; %s lists them under \"failed\"", len(record.Failed), recordPath)
}

// resolveHistoryAccount finds the server account to clear: the one named
// account, or the PLEX_TOKEN owner's, found by plex.tv username.
func resolveHistoryAccount(d clearHistoryDeps, account string) (plexAccount, error) {
	accounts, err := d.server.Accounts()
	if err != nil {
		return plexAccount{}, err
	}
	var names []string
	for _, a := range accounts {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	list := strings.Join(names, ", ")
	name := account
	if name == "" {
		user, err := d.owner.User()
		if err != nil {
			return plexAccount{}, fmt.Errorf("finding the PLEX_TOKEN owner: %w; pass --account (one of: %s)", err, list)
		}
		name = user.Username
	}
	var folded []plexAccount
	for _, a := range accounts {
		if a.Name == name {
			return a, nil
		}
		if strings.EqualFold(a.Name, name) {
			folded = append(folded, a)
		}
	}
	switch {
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

// matchHistory returns the episode entries whose show is a target, each once.
func matchHistory(entries []historyEntry, targets []historyTarget) []historyEntry {
	var matched []historyEntry
	for _, e := range entries {
		if e.Type != "episode" {
			continue
		}
		for _, t := range targets {
			if t.matchesShow(e.GrandparentTitle) {
				matched = append(matched, e)
				break
			}
		}
	}
	return matched
}

// printHistoryPlan lists the matched entries per show title, since a target
// can match more than one, and the targets that matched nothing.
func printHistoryPlan(out io.Writer, account string, targets []historyTarget, matched []historyEntry) {
	fmt.Fprintf(out, "Watch history for %s on the Plex server:\n", account)
	counts := make(map[string]int)
	for _, e := range matched {
		counts[e.GrandparentTitle]++
	}
	for _, show := range slices.Sorted(maps.Keys(counts)) {
		fmt.Fprintf(out, "  %-50s %d episode(s)\n", show, counts[show])
	}
	var missing, yearUnchecked []string
	for _, t := range targets {
		found := false
		for show := range counts {
			if t.matchesShow(show) {
				found = true
				if _, year := splitTitleYear(show); t.year != 0 && year == 0 {
					yearUnchecked = append(yearUnchecked, t.raw)
				}
			}
		}
		if !found {
			missing = append(missing, t.raw)
		}
	}
	if len(yearUnchecked) > 0 {
		fmt.Fprintf(out, "History doesn't record a show's year, so these matched on title alone: %s\n", strings.Join(slices.Compact(yearUnchecked), ", "))
	}
	if len(missing) > 0 {
		fmt.Fprintln(out, "Not in this history:")
		for _, m := range missing {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
}

func describeHistoryEntry(e historyEntry) string {
	return fmt.Sprintf("%s S%02dE%02d %q", e.GrandparentTitle, e.ParentIndex, e.Index, e.Title)
}

// writeHistoryRecord writes record to a new file in dir, never over an
// earlier one.
func writeHistoryRecord(dir string, now time.Time, record historyRecord) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "history-cleared-"+now.UTC().Format("20060102T150405Z")+".json")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	return path, writeRecordFile(path, record)
}

func writeRecordFile(path string, record historyRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
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
