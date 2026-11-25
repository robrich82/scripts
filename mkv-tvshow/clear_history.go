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
	"sync"
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
// symbols (Gintama°, ∀ Gundam and C++ are other series) of every script kept.
// Punctuation, spaces, dash lookalikes, modifier symbols and the spacing
// modifier letters (U+02B0–02FF) are dropped. titleMatcher keeps only ASCII,
// which is too loose here: whatever matches gets deleted.
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
	cloud     CloudHistoryClient
	prompt    Prompter
	recordDir string // where the record of deleted entries is written
	out       io.Writer
	now       func() time.Time
	sleep     func(time.Duration) // waits out plex.tv's rate limit
}

// historyRecord is the file written before any entry is deleted: neither
// the server nor plex.tv can restore deleted history, so this is the only
// trace of it.
type historyRecord struct {
	Account   string         `json:"account"`
	AccountID int            `json:"accountID"`
	Targets   []string       `json:"targets"`
	Server    []historyEntry `json:"server"`
	// PlexTV is null when plex.tv wasn't read, and PlexTVSkipped says why.
	PlexTV        []cloudEntry `json:"plexTV"`
	PlexTVSkipped string       `json:"plexTVSkipped,omitempty"`
	// PlexTVUnchecked is why plex.tv couldn't be read again after the deletes
	// to see which took, or its history then couldn't be trusted.
	PlexTVUnchecked string `json:"plexTVUnchecked,omitempty"`
	// Complete is false until the run finishes and the record is rewritten,
	// so a record left false is from an interrupted run (some of Server and
	// PlexTV may remain) or one whose final rewrite failed.
	Complete bool `json:"complete"`
	// Failed lists the server keys of the entries not deleted: those whose
	// delete failed, and those not tried after a run of failures.
	// FailedPlexTV lists the plex.tv ids that plex.tv still lists after the
	// deletes, or whose removal failed with an error; when PlexTVUnchecked is
	// set, those whose removal wasn't tried or didn't reply true.
	Failed       []string `json:"failed,omitempty"`
	FailedPlexTV []string `json:"failedPlexTV,omitempty"`
}

// maxConsecutiveDeleteFailures stops deleting from the server, or from
// plex.tv, once it seems to have gone away, where each delete would wait out
// the full timeout.
const maxConsecutiveDeleteFailures = 3

// maxRateLimitTries is how often one plex.tv request is tried while plex.tv
// rate-limits it, and maxRateLimitWait the longest it waits between tries.
const (
	maxRateLimitTries = 5
	maxRateLimitWait  = 5 * time.Minute
)

// ownerFunc is an OwnerLookup from a function.
type ownerFunc func() (plexTVUser, error)

func (f ownerFunc) User() (plexTVUser, error) { return f() }

// clearPlan is what a run found to clear: the server account and its matched
// history, and the PLEX_TOKEN owner's matched plex.tv history when the account
// is theirs. cloudSkipped says why plex.tv wasn't read, when it wasn't.
type clearPlan struct {
	acct         plexAccount
	server       historyPlan
	owner        plexTVUser
	cloudSkipped string
	cloudRead    []cloudEntry // the owner's whole plex.tv history
	cloud        cloudPlan
}

// planClear reads both histories and matches the targets in them. It changes
// nothing.
func planClear(targets []historyTarget, account string, d clearHistoryDeps, retry func(func() error) error) (clearPlan, error) {
	// resolveHistoryAccount and plexTVAccount both need the owner: ask once.
	owner := ownerFunc(sync.OnceValues(d.owner.User))
	acct, err := resolveHistoryAccount(d.server, owner, account)
	if err != nil {
		return clearPlan{}, err
	}
	entries, err := d.server.WatchHistory(acct.ID)
	if err != nil {
		return clearPlan{}, err
	}
	for _, e := range entries {
		// Deletes are irreversible, so don't rely on the server's filter alone.
		if e.AccountID != acct.ID {
			return clearPlan{}, fmt.Errorf("the server returned account %d's history when asked for %s's (%d)", e.AccountID, acct.Name, acct.ID)
		}
	}
	p := clearPlan{acct: acct, server: planHistory(entries, targets)}
	p.owner, p.cloudSkipped, err = plexTVAccount(owner, acct)
	if err != nil {
		return clearPlan{}, err
	}
	if p.cloudSkipped != "" {
		return p, nil
	}
	p.cloudRead, err = readCloudHistory(d.cloud, p.owner.UUID, retry)
	if err != nil {
		return clearPlan{}, err
	}
	p.cloud = planCloud(p.cloudRead, targets)
	return p, nil
}

// clearHistory deletes an account's server watch history for the target
// shows, and the PLEX_TOKEN owner's plex.tv history when that account is
// theirs, after listing the matches and asking. account is a server account
// name; "" means the PLEX_TOKEN owner.
func clearHistory(args []string, account string, d clearHistoryDeps) error {
	targets, err := parseHistoryTargets(args)
	if err != nil {
		return err
	}
	retry := func(f func() error) error { return waitOutRateLimit(d.out, d.sleep, f) }
	p, err := planClear(targets, account, d, retry)
	if err != nil {
		return fmt.Errorf("%w; nothing was deleted", err)
	}
	acct, server, cloud := p.acct, p.server, p.cloud
	printHistoryPlan(d.out, acct, server)
	if p.cloudSkipped != "" {
		fmt.Fprintln(d.out, p.cloudSkipped)
	} else {
		printCloudPlan(d.out, p.owner.Username, cloud)
	}
	if len(server.matched)+len(cloud.matched) == 0 {
		fmt.Fprintln(d.out, "Nothing to clear.")
		return nil
	}

	what := fmt.Sprintf("%d history entries", len(server.matched))
	if len(cloud.matched) > 0 {
		what = fmt.Sprintf("%d server and %d plex.tv history entries", len(server.matched), len(cloud.matched))
	}
	ans, err := d.prompt.Ask(fmt.Sprintf("\nDelete these %s for %s (account %d)? This can't be undone. [y/N]: ", what, acct.Name, acct.ID))
	if err != nil {
		return fmt.Errorf("reading the answer: %w; nothing was deleted", err)
	}
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Fprintln(d.out, "Aborted; no changes made.")
		return nil
	}

	record := historyRecord{Account: acct.Name, AccountID: acct.ID, Server: server.matched, PlexTVSkipped: p.cloudSkipped}
	if p.cloudSkipped == "" {
		record.PlexTV = append([]cloudEntry{}, cloud.matched...) // [], not null, when nothing matched
	}
	for _, t := range targets {
		record.Targets = append(record.Targets, t.raw)
	}
	recordPath := historyRecordPath(d.recordDir, d.now())
	if rmErr, err := writeNewHistoryRecord(recordPath, record); err != nil {
		return fmt.Errorf("saving %s: %v\n%s%sNothing was deleted", recordPath, withoutPaths(err), hintLine(err), leftBehindNote(err, rmErr))
	}
	fmt.Fprintf(d.out, "Recorded the entries in %s\n", recordPath)

	record.Failed = deleteEach(d.out, "the server", server.matched, func(e historyEntry) string { return e.HistoryKey },
		describeHistoryEntry, d.server.DeleteHistory).failed
	removeCloud := func(id string) error { return retry(func() error { return d.cloud.RemoveActivity(id) }) }
	removed := deleteEach(d.out, "plex.tv", cloud.matched, func(e cloudEntry) string { return e.ID }, cloud.describe, removeCloud)
	record.FailedPlexTV = removed.failed
	added := 0
	if len(cloud.matched) > 0 {
		check := checkCloudRemoved(d, retry, p, targets, removed)
		record.FailedPlexTV, record.PlexTVUnchecked, added = check.failed, check.unchecked, check.added
	}
	record.Complete = true
	done := deletedCounts(len(server.matched), len(record.Failed), len(cloud.matched), len(record.FailedPlexTV))
	if record.PlexTVUnchecked != "" {
		done += " (plex.tv's by its replies, not checked)"
	}
	fmt.Fprintf(d.out, "Deleted %s.\n", done)

	var problems []error
	updateErr := updateHistoryRecord(recordPath, record)
	if updateErr != nil {
		problems = append(problems, fmt.Errorf("%s were deleted, but %s couldn't be updated to say so: %v\n%sIts \"complete\" stays false",
			done, recordPath, withoutPaths(updateErr), hintLine(updateErr)))
	}
	if failed := len(record.Failed) + len(record.FailedPlexTV); failed > 0 {
		where := fmt.Sprintf("%s lists them under %s", recordPath, failedFields(record))
		if updateErr != nil {
			where = "the lines above name them"
		}
		problems = append(problems, fmt.Errorf("%d history entries were not deleted; %s", failed, where))
	}
	if record.PlexTVUnchecked != "" {
		problems = append(problems, errors.New("plex.tv's deletes couldn't be checked; run again to clear any it still lists"))
	}
	if added > 0 {
		problems = append(problems, fmt.Errorf("plex.tv now lists %d more matching item(s); run again to clear them", added))
	}
	return errors.Join(problems...)
}

// deletedCounts is "2 of 3 history entries", or with plex.tv items
// "2 of 3 server and 4 of 4 plex.tv history entries".
func deletedCounts(server, serverFailed, cloud, cloudFailed int) string {
	if cloud == 0 {
		return fmt.Sprintf("%d of %d history entries", server-serverFailed, server)
	}
	return fmt.Sprintf("%d of %d server and %d of %d plex.tv history entries", server-serverFailed, server, cloud-cloudFailed, cloud)
}

// failedFields names the record's fields that list entries not deleted.
func failedFields(r historyRecord) string {
	var names []string
	if len(r.Failed) > 0 {
		names = append(names, `"failed"`)
	}
	if len(r.FailedPlexTV) > 0 {
		names = append(names, `"failedPlexTV"`)
	}
	return strings.Join(names, " and ")
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
		if tokenRefused(err) != nil {
			// With --account, plexTVAccount skips plex.tv for this.
			return plexAccount{}, fmt.Errorf("finding the PLEX_TOKEN owner: %w; pass --account (one of: %s)", err, list)
		}
		if err != nil {
			return plexAccount{}, fmt.Errorf("finding the PLEX_TOKEN owner: %w", err)
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
// target's title with its year: the year-less one is then another show. A
// year-less show whose key a titled show also has is that show, retitled: it
// matches when that show does.
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
		// The keys of the shows titled with the target's year, and with
		// another year: a year-less title with one of them is that show.
		exactKeys, otherKeys := make(map[string]bool), make(map[string]bool)
		exactYear := false
		if t.year != 0 {
			for _, ti := range titles {
				if ti.key != t.key || ti.year == 0 {
					continue
				}
				keys := otherKeys
				if ti.year == t.year {
					exactYear, keys = true, exactKeys
				}
				for _, s := range ti.shows {
					if s.key != "" {
						keys[s.key] = true
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
				case ti.year != 0:
					continue
				case exactKeys[s.key]:
				case otherKeys[s.key] || exactYear:
					continue
				default:
					unchecked = true
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

// printHistoryPlan lists each matched show with its episode count and the
// episode watched last, since a target can match more than one show, and the
// targets that matched nothing.
func printHistoryPlan(out io.Writer, acct plexAccount, plan historyPlan) {
	fmt.Fprintf(out, "Watch history for %s (account %d) on the Plex server:\n", acct.Name, acct.ID)
	shows := slices.SortedFunc(maps.Keys(plan.shows), func(a, b historyShow) int {
		return cmp.Or(strings.Compare(a.title, b.title), strings.Compare(a.key, b.key))
	})
	// Titles are compared as matching folds them, so ones that differ only
	// in what matching ignores count as one.
	perTitle, perLine := make(map[string]int), make(map[string]int)
	lines, lineIDs := make([]string, len(shows)), make([]string, len(shows))
	for i, s := range shows {
		sum := plan.shows[s]
		last := describeLastWatched(sum.last)
		t, year := splitTitleYear(s.title)
		folded := historyTitleKey(t) + "\x00" + strconv.Itoa(year)
		lines[i] = fmt.Sprintf("  %-40s %d episode(s), last watched %s", s.title, sum.episodes, last)
		lineIDs[i] = fmt.Sprintf("%s\x00%d\x00%s", folded, sum.episodes, last)
		perTitle[folded]++
		perLine[lineIDs[i]]++
	}
	for i, line := range lines {
		if perLine[lineIDs[i]] > 1 {
			line += " [" + cmp.Or(shows[i].key, "no key") + "]" // nothing else tells them apart
		}
		fmt.Fprintln(out, line)
	}
	if len(perTitle) < len(shows) {
		fmt.Fprintln(out, "Titles that repeat, or that matching counts as the same, may be different shows: check each before answering.")
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

func episodeLabel(e historyEntry) string {
	return fmt.Sprintf("S%02dE%02d %q", e.ParentIndex, e.Index, e.Title)
}

func describeHistoryEntry(e historyEntry) string {
	return e.GrandparentTitle + " " + episodeLabel(e)
}

func describeLastWatched(e historyEntry) string {
	s := episodeLabel(e)
	if e.ViewedAt > 0 {
		s += " on " + time.Unix(e.ViewedAt, 0).Format("2006-01-02")
	}
	return s
}

// deleteResult is what deleteEach left: the ids not deleted, in order, and
// among them those never tried and those whose reply alone failed
// (isUnconfirmed).
type deleteResult struct {
	failed      []string
	untried     map[string]bool
	unconfirmed map[string]bool
}

// deleteEach deletes each entry by its id from where (the server or plex.tv).
// It gives up after maxConsecutiveDeleteFailures failures in a row; an
// unconfirmed reply isn't one, since plex.tv answered it.
func deleteEach[E any](out io.Writer, where string, entries []E, id func(E) string, describe func(E) string, del func(string) error) deleteResult {
	res := deleteResult{untried: make(map[string]bool), unconfirmed: make(map[string]bool)}
	consecutive := 0
	for i, e := range entries {
		err := del(id(e))
		if err == nil {
			consecutive = 0
			continue
		}
		res.failed = append(res.failed, id(e))
		if isUnconfirmed(err) {
			fmt.Fprintf(out, "  unconfirmed: %s: %v\n", describe(e), err)
			res.unconfirmed[id(e)] = true
			consecutive = 0
			continue
		}
		fmt.Fprintf(out, "  failed: %s: %v\n", describe(e), err)
		consecutive++
		if consecutive == maxConsecutiveDeleteFailures {
			rest := entries[i+1:]
			if len(rest) > 0 {
				fmt.Fprintf(out, "Stopped after %d failures in a row on %s; %d entries there weren't tried.\n", consecutive, where, len(rest))
			}
			for _, r := range rest {
				res.failed = append(res.failed, id(r))
				res.untried[id(r)] = true
			}
			break
		}
	}
	return res
}

// waitOutRateLimit runs the plex.tv request f, and again after each rate
// limit, up to maxRateLimitTries times.
func waitOutRateLimit(out io.Writer, sleep func(time.Duration), f func() error) error {
	for try := 1; ; try++ {
		err := f()
		var limited *rateLimitError
		if !errors.As(err, &limited) {
			return err
		}
		if try == maxRateLimitTries {
			return fmt.Errorf("gave up after %d tries: %w", try, err)
		}
		wait := limited.After
		if wait <= 0 {
			wait = time.Minute // what plex.tv usually asks for
		}
		wait = min(wait, maxRateLimitWait)
		fmt.Fprintf(out, "plex.tv rate limit: waiting %s\n", wait.Round(time.Second))
		sleep(wait)
	}
}

func historyRecordPath(dir string, now time.Time) string {
	return filepath.Join(dir, "history-cleared-"+now.UTC().Format("20060102T150405Z")+".json")
}

// leftBehindNote is the lines saying the unfinished record couldn't be
// removed, or "" when rmErr is nil. A hint the write's error already gave
// isn't repeated.
func leftBehindNote(err, rmErr error) string {
	if rmErr == nil {
		return ""
	}
	hint := hintLine(rmErr)
	if hint == hintLine(err) {
		hint = ""
	}
	return fmt.Sprintf("The unfinished file couldn't be removed: %v\nDelete it: it's not a record of deleted entries.\n%s",
		withoutPaths(rmErr), hint)
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
// pause like cacheSaver.finish (a briefly locked file on Windows).
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
	plexTV := newPlexTVClient(plexTVBaseURL, plexTVCommunityURL, plexToken)
	err = clearHistory(args, account, clearHistoryDeps{
		server:    newHTTPPlexClient(plexURL, plexToken),
		owner:     plexTV,
		cloud:     plexTV,
		prompt:    newStdinPrompter(),
		recordDir: cfgDir,
		out:       os.Stdout,
		now:       time.Now,
		sleep:     time.Sleep,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
