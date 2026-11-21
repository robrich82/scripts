package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

var trailingYear = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)\s*$`)

// splitTitleYear splits "Title (2018)" into "Title" and 2018. A title
// without a trailing year comes back whole, with year 0.
func splitTitleYear(s string) (string, int) {
	m := trailingYear.FindStringSubmatch(s)
	if m == nil {
		return strings.TrimSpace(s), 0
	}
	year, _ := strconv.Atoi(m[2])
	return m[1], year
}

func parseHistoryTargets(args []string) ([]historyTarget, error) {
	var targets []historyTarget
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		title, year := splitTitleYear(raw)
		matches := titleMatcher(title)
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
}

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

	matched, byTarget := matchHistory(entries, targets)
	printHistoryPlan(d.out, acct.Name, targets, byTarget)
	if len(matched) == 0 {
		fmt.Fprintln(d.out, "Nothing to clear.")
		return nil
	}

	ans, err := d.prompt.Ask(fmt.Sprintf("\nDelete these %d history entries for %s? This can't be undone. [y/N]: ", len(matched), acct.Name))
	if err != nil || strings.ToLower(strings.TrimSpace(ans)) != "y" {
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

	failed := 0
	for _, e := range matched {
		if err := d.server.DeleteHistory(e.HistoryKey); err != nil {
			failed++
			fmt.Fprintf(d.out, "  failed: %s: %v\n", describeHistoryEntry(e), err)
		}
	}
	fmt.Fprintf(d.out, "Done. Deleted %d of %d history entries.\n", len(matched)-failed, len(matched))
	if failed > 0 {
		return fmt.Errorf("%d history entries could not be deleted", failed)
	}
	return nil
}

// resolveHistoryAccount finds the server account to clear: the one named
// account, or the PLEX_TOKEN owner's, found by plex.tv username.
func resolveHistoryAccount(d clearHistoryDeps, account string) (plexAccount, error) {
	accounts, err := d.server.Accounts()
	if err != nil {
		return plexAccount{}, err
	}
	name := account
	if name == "" {
		user, err := d.owner.User()
		if err != nil {
			return plexAccount{}, fmt.Errorf("finding the PLEX_TOKEN owner: %w", err)
		}
		name = user.Username
	}
	var names []string
	for _, a := range accounts {
		if strings.EqualFold(a.Name, name) {
			return a, nil
		}
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if account == "" {
		return plexAccount{}, fmt.Errorf("the PLEX_TOKEN owner %q has no account on this server; pass --account (one of: %s)", name, strings.Join(names, ", "))
	}
	return plexAccount{}, fmt.Errorf("no server account named %q; the accounts are: %s", account, strings.Join(names, ", "))
}

// matchHistory returns the episode entries whose show is a target, and
// those entries grouped by target. An entry goes to the first target it
// matches, so it is never deleted twice.
func matchHistory(entries []historyEntry, targets []historyTarget) ([]historyEntry, [][]historyEntry) {
	var matched []historyEntry
	byTarget := make([][]historyEntry, len(targets))
	for _, e := range entries {
		if e.Type != "episode" {
			continue
		}
		for i, t := range targets {
			if t.matchesShow(e.GrandparentTitle) {
				matched = append(matched, e)
				byTarget[i] = append(byTarget[i], e)
				break
			}
		}
	}
	return matched, byTarget
}

func printHistoryPlan(out io.Writer, account string, targets []historyTarget, byTarget [][]historyEntry) {
	fmt.Fprintf(out, "Watch history for %s on the Plex server:\n", account)
	var missing []string
	for i, t := range targets {
		if len(byTarget[i]) == 0 {
			missing = append(missing, t.raw)
			continue
		}
		// A target without a year can match more than one show title.
		counts := make(map[string]int)
		for _, e := range byTarget[i] {
			counts[e.GrandparentTitle]++
		}
		shows := make([]string, 0, len(counts))
		for s := range counts {
			shows = append(shows, s)
		}
		sort.Strings(shows)
		for _, s := range shows {
			fmt.Fprintf(out, "  %-50s %d episode(s)\n", s, counts[s])
		}
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

func writeHistoryRecord(dir string, now time.Time, record historyRecord) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "history-cleared-"+now.UTC().Format("20060102T150405Z")+".json")
	if _, err := os.Stat(path); err == nil {
		return "", errors.New(path + " already exists")
	}
	if err := atomicWriteFile(path, data); err != nil {
		return "", err
	}
	return path, nil
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
