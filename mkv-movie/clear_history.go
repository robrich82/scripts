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
	cloud     CloudHistoryClient
	prompt    func(string) (string, error)
	recordDir string // where the record of deleted entries is written
	out       io.Writer
	now       func() time.Time
	sleep     func(time.Duration) // waits out plex.tv's rate limit, and for it to show removals
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
	// to see which took, or its history then couldn't be trusted. Only the
	// first read after the deletes sets it; a failed second read leaves the
	// first standing.
	PlexTVUnchecked string `json:"plexTVUnchecked,omitempty"`
	// Complete is false until the run finishes and the record is rewritten,
	// so a record left false is from an interrupted run (some of Server and
	// PlexTV may remain) or one whose final rewrite failed.
	Complete bool `json:"complete"`
	// Failed lists the historyKeys of the server entries not deleted: those
	// whose delete failed, and those not tried after a run of failures.
	// FailedPlexTV lists the plex.tv ids not deleted: those the last read
	// after the deletes still lists, those whose removal failed with an error
	// or wasn't tried, and those it didn't reply true for that the last read
	// can't show went. When PlexTVUnchecked is set, it's by the replies alone:
	// every id not replied true for.
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
// movies, and the PLEX_TOKEN owner's plex.tv history when that account is
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
	ans, err := d.prompt(fmt.Sprintf("\nDelete these %s for %s (account %d)? This can't be undone. [y/N]: ", what, acct.Name, acct.ID))
	if err != nil {
		return fmt.Errorf("reading the answer: %w; nothing was deleted", err)
	}
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Fprintln(d.out, "Aborted; no changes made.")
		return nil
	}

	record := historyRecord{Account: acct.Name, AccountID: acct.ID, PlexTVSkipped: p.cloudSkipped,
		Server: append([]historyEntry{}, server.matched...)} // [], not null, when only plex.tv matched
	if p.cloudSkipped == "" {
		record.PlexTV = append([]cloudEntry{}, cloud.matched...) // [], not null, when nothing matched
	}
	for _, t := range targets {
		record.Targets = append(record.Targets, t.raw)
	}
	recordPath := historyRecordPath(d.recordDir, d.now())
	if rmErr, err := writeNewHistoryRecord(recordPath, record); err != nil {
		return fmt.Errorf("saving %s: %v\n%sNothing was deleted", recordPath, withoutPaths(err), leftBehindNote(rmErr))
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
		problems = append(problems, fmt.Errorf("%s were deleted, but %s couldn't be updated to say so: %v\nIts \"complete\" stays false",
			done, recordPath, withoutPaths(updateErr)))
	}
	if failed := len(record.Failed) + len(record.FailedPlexTV); failed > 0 {
		where := fmt.Sprintf("%s lists them under %s", recordPath, failedFields(record))
		if updateErr != nil {
			// The record can't list them, and untried ones haven't been named.
			printNotDeleted(d.out, record, server.matched, cloud)
			where = "the output above names them"
		}
		problems = append(problems, fmt.Errorf("%d history entries were not deleted; %s", failed, where))
	}
	if record.PlexTVUnchecked != "" {
		problems = append(problems, errors.New("plex.tv's deletes couldn't be checked; run again to clear any it still lists"))
	}
	if added > 0 {
		problems = append(problems, fmt.Errorf("plex.tv now lists %d more matching play(s); run again to clear them", added))
	}
	return errors.Join(problems...)
}

// printNotDeleted names the entries of r.Failed and r.FailedPlexTV.
func printNotDeleted(out io.Writer, r historyRecord, server []historyEntry, cloud cloudPlan) {
	fmt.Fprintln(out, "Not deleted:")
	for _, e := range server {
		if slices.Contains(r.Failed, e.HistoryKey) {
			fmt.Fprintf(out, "  %s\n", describeHistoryEntry(e))
		}
	}
	for _, e := range cloud.matched {
		if slices.Contains(r.FailedPlexTV, e.ID) {
			fmt.Fprintf(out, "  %s\n", cloud.describe(e))
		}
	}
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

// historyMovie is a movie in the history: its title as its entries recorded
// it, its year, and its key. Two movies can share a title, and an entry of a
// movie no longer in the library has no key.
type historyMovie struct {
	title string
	year  int // entryYear's: 0 for none, unreadableYear for an unreadable date
	key   string
}

// unreadableYear is entryYear's year for a release date it can't read. A
// dated target never takes such an entry on title alone.
const unreadableYear = -1

var leadingYear = regexp.MustCompile(`^\d{4}`)

// entryYear is the year e's release date starts with, else the "(Year)" its
// title ends in. With neither, it is 0 for an entry with no release date and
// unreadableYear for one whose date has no year to read.
func entryYear(e historyEntry) int {
	if y, _ := strconv.Atoi(leadingYear.FindString(e.OriginallyAvailableAt)); y > 0 {
		return y
	}
	if _, year := splitTitleYear(e.Title); year != 0 || e.OriginallyAvailableAt == "" {
		return year
	}
	return unreadableYear
}

func movieOf(e historyEntry) historyMovie {
	return historyMovie{title: e.Title, year: entryYear(e), key: e.Key}
}

// movieSummary is a movie's matched plays, and the one watched last.
type movieSummary struct {
	plays int
	last  historyEntry
}

// historyPlan is what a run would delete, what it couldn't match, and what
// it left as maybe another movie.
type historyPlan struct {
	matched       []historyEntry
	movies        map[historyMovie]*movieSummary
	missing       []string // targets that matched nothing
	yearUnchecked []string // targets that matched an entry with no year
	severalYears  []string // dated targets whose range took movies of more than one year
	// kept is the movies with a target's title and no year, or an unreadable
	// one, that no target took: maybe other movies.
	kept map[historyMovie]*movieSummary
}

// planHistory picks the movie entries a target names, each once. A target's
// year must be within one of the entry's, as for FindMovieByTitle, when both
// have one. An entry without a year matches too, unless the history also has
// the target's title with a year in range: the year-less one is then another
// movie, and is kept. An entry whose release date can't be read is kept by a
// dated target either way. An entry whose key one in range also has is that
// movie, whatever its year; a year-less or unreadable one whose key only
// another year has is that other movie, and is skipped.
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

	plan := historyPlan{movies: make(map[historyMovie]*movieSummary), kept: make(map[historyMovie]*movieSummary)}
	take, keep := make(map[historyMovie]bool), make(map[historyMovie]bool)
	listed := make(map[string]bool)
	for _, t := range targets {
		if listed[t.raw] {
			continue // a repeat: the same raw is the same target
		}
		listed[t.raw] = true
		inRange := func(year int) bool { return abs(year-t.year) <= 1 }
		// The keys of the dated movies with the target's title, in range and
		// not: an entry with an in-range one's key is that movie, and a
		// year-less or unreadable one with only another year's is that other
		// movie.
		exactKeys, otherKeys := make(map[string]bool), make(map[string]bool)
		exactYear := false
		if t.year != 0 {
			for _, m := range movies {
				if m.titleKey != t.key || m.year <= 0 {
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
		years := make(map[int]bool) // of the movies taken for being in range
		for _, m := range movies {
			if m.titleKey != t.key {
				continue
			}
			switch {
			case t.year == 0:
			case m.year > 0 && inRange(m.year):
				years[m.year] = true
			case exactKeys[m.key]: // never "": an empty key ties nothing
			case m.year > 0 || otherKeys[m.key]:
				continue
			case m.year == unreadableYear || exactYear:
				keep[m.historyMovie] = true
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
		if len(years) > 1 {
			plan.severalYears = append(plan.severalYears, t.raw)
		}
	}
	for _, e := range entries {
		m := movieOf(e)
		if e.Type != "movie" {
			continue
		}
		var sums map[historyMovie]*movieSummary
		switch {
		case take[m]:
			plan.matched = append(plan.matched, e)
			sums = plan.movies
		case keep[m]: // and no other target took it
			sums = plan.kept
		default:
			continue
		}
		sum := sums[m]
		if sum == nil {
			sum = &movieSummary{last: e}
			sums[m] = sum
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
	if _, y := splitTitleYear(m.title); m.year > 0 && y != m.year {
		return fmt.Sprintf("%s (%d)", m.title, m.year)
	}
	return m.title
}

// movieLines is a plan line per movie in sums, sorted, ending in its play
// count, when it was last watched and why's text. Lines that would read the
// same also name the key. titles is how many titles they have, folded as
// matching folds them, with no year and an unreadable one counted as one.
func movieLines(sums map[historyMovie]*movieSummary, why func(historyMovie) string) (lines []string, titles int) {
	movies := slices.SortedFunc(maps.Keys(sums), compareMovies)
	perTitle, perLine := make(map[string]bool), make(map[string]int)
	lineIDs := make([]string, len(movies))
	for i, m := range movies {
		sum := sums[m]
		t, _ := splitTitleYear(m.title)
		folded := historyTitleKey(t) + "\x00" + strconv.Itoa(max(m.year, 0))
		tail := describeLastWatched(sum.last) + why(m)
		lines = append(lines, fmt.Sprintf("  %-40s %d play(s)%s", movieLabel(m), sum.plays, tail))
		lineIDs[i] = fmt.Sprintf("%s\x00%d\x00%s", folded, sum.plays, tail)
		perTitle[folded] = true
		perLine[lineIDs[i]]++
	}
	for i := range lines {
		if perLine[lineIDs[i]] > 1 {
			lines[i] += " [" + cmp.Or(movies[i].key, "no key") + "]" // nothing else tells them apart
		}
	}
	return lines, len(perTitle)
}

func compareMovies(a, b historyMovie) int {
	return cmp.Or(strings.Compare(a.title, b.title), cmp.Compare(a.year, b.year), strings.Compare(a.key, b.key))
}

// printHistoryPlan lists each matched movie with its play count and when it
// was last watched, since a target can match more than one movie, then
// what it flagged or kept, and the targets that matched nothing.
func printHistoryPlan(out io.Writer, acct plexAccount, plan historyPlan) {
	fmt.Fprintf(out, "Watch history for %s (account %d) on the Plex server:\n", acct.Name, acct.ID)
	lines, titles := movieLines(plan.movies, func(historyMovie) string { return "" })
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	if titles < len(lines) {
		fmt.Fprintln(out, "Titles that repeat, or that matching counts as the same, may be different movies: check each before answering.")
	}
	if len(plan.severalYears) > 0 {
		fmt.Fprintf(out, "These matched movies of more than one year, which may be different movies: check each before answering: %s\n", strings.Join(plan.severalYears, ", "))
	}
	if len(plan.yearUnchecked) > 0 {
		fmt.Fprintf(out, "Some history entries record no release date, so these matched them on title alone: %s\n", strings.Join(plan.yearUnchecked, ", "))
	}
	if len(plan.kept) > 0 {
		fmt.Fprintln(out, "Not deleting these, which may be other movies (give the title without its year to include them):")
		lines, _ := movieLines(plan.kept, func(m historyMovie) string {
			if m.year == unreadableYear {
				return ", with a release date that can't be read"
			}
			return ", with no release date"
		})
		for _, line := range lines {
			fmt.Fprintln(out, line)
		}
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

// deleteResult is what deleteEach left: the ids not deleted, in order, and
// among them those never tried and those whose reply didn't confirm the
// delete (an *unconfirmedError).
type deleteResult struct {
	failed      []string
	untried     map[string]bool
	unconfirmed map[string]bool
}

// deleteEach deletes each entry by its id from where (the server or plex.tv).
// It gives up after maxConsecutiveDeleteFailures failures in a row; an
// unconfirmed reply isn't one, since plex.tv answered it, unless it was cut
// off.
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
		var u *unconfirmedError
		if errors.As(err, &u) {
			res.unconfirmed[id(e)] = true
			if !u.cut {
				fmt.Fprintf(out, "  unconfirmed: %s: %v\n", describe(e), err)
				consecutive = 0
				continue
			}
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
	plexTV := newPlexTVClient(plexTVBaseURL, plexTVCommunityURL, plexToken)
	err = clearHistory(args, account, clearHistoryDeps{
		server:    newHTTPPlexMovieClient(plexURL, plexToken),
		owner:     plexTV,
		cloud:     plexTV,
		prompt:    askStdin,
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

// errStdinClosed is askStdin's error when stdin has no more lines.
var errStdinClosed = errors.New("stdin is closed")

// askStdin is stdinPrompter, except that a closed stdin is an error, not a
// blank answer, so a run that can't ask fails rather than quietly aborting.
func askStdin(prompt string) (string, error) {
	fmt.Print(prompt)
	if stdinScanner.Scan() {
		return stdinScanner.Text(), nil
	}
	return "", cmp.Or(stdinScanner.Err(), errStdinClosed)
}
