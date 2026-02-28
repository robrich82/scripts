package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MKVSyncer updates the internal MKV header and/or writes tags via mkvpropedit.
type MKVSyncer interface {
	// SyncHeader updates the MKV date header and/or writes tags from tagsPath.
	// Pass an empty string for dateStr to skip the date update, or an empty
	// string for tagsPath to skip tag writing.
	// Both operations are combined into a single mkvpropedit invocation.
	SyncHeader(filePath, dateStr, tagsPath string) error
}

type realMKVSyncer struct {
	path string
}

func newRealMKVSyncer() (*realMKVSyncer, error) {
	path, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, err
	}
	return &realMKVSyncer{path: path}, nil
}

func (s *realMKVSyncer) SyncHeader(filePath, dateStr, tagsPath string) error {
	args := []string{filePath}
	if dateStr != "" {
		formatted := dateStr
		if len(dateStr) == 4 {
			formatted = dateStr + "-01-01"
		}
		args = append(args, "--edit", "info", "--set", "date="+formatted+"T00:00:00Z")
	}
	if tagsPath != "" {
		args = append(args, "--tags", "all:"+tagsPath)
	}
	if len(args) == 1 {
		return nil // nothing to do
	}
	return exec.Command(s.path, args...).Run()
}

// Stats counts processing outcomes.
type Stats struct {
	Updated int
	OK      int
	Skipped int
	Errors  int
}

// FileResult is the outcome of processing a single file.
type FileResult int

const (
	ResultUpdated FileResult = iota
	ResultOK
	ResultSkipped
	ResultError
)

// MovieRatingResolver resolves an NZ content rating for a movie, trying
// external sources, cache, and finally a user prompt.
type MovieRatingResolver struct {
	sources         []RatingSource
	cache           *MovieRatingCache
	overwrite       bool
	plex            PlexMovieClient // nil if not configured
	prompter        func(string) (string, error)
	sessionPrompted map[string]NZRating // key → rating already prompted this run
}

func newMovieRatingResolver(
	sources []RatingSource,
	cache *MovieRatingCache,
	overwrite bool,
	plex PlexMovieClient,
	prompter func(string) (string, error),
) *MovieRatingResolver {
	return &MovieRatingResolver{
		sources:         sources,
		cache:           cache,
		overwrite:       overwrite,
		plex:            plex,
		prompter:        prompter,
		sessionPrompted: make(map[string]NZRating),
	}
}

// ResolveMovieRating returns the best NZ rating for the given movie. key is
// the cache key (TMDB ID when known, "title:YEAR" otherwise). title is the
// human-readable movie name used for Classification Office and user prompts.
func (r *MovieRatingResolver) ResolveMovieRating(key, title string) NZRating {
	if r.cache == nil {
		return RatingNone
	}

	// 1. Cache hit (skip when --overwrite-ratings).
	if !r.overwrite {
		if rating, ok := r.cache.Get(key); ok {
			return rating
		}
	}

	// 2. External sources.
	for _, src := range r.sources {
		rating, err := src.FetchRating(key, title)
		if err != nil {
			fmt.Printf("  [!] %s rating error: %v\n", src.Name(), err)
			continue
		}
		if rating != RatingNone {
			r.cache.Set(key, rating)
			return rating
		}
	}

	// 3. Cache fallback — use any previously-saved rating rather than re-prompting,
	//    even under --overwrite-ratings (external sources failed/have no data).
	if rating, ok := r.cache.Get(key); ok && rating != RatingNone {
		return rating
	}

	// 4. Session prompt dedup — at most once per key per run.
	if rating, already := r.sessionPrompted[key]; already {
		return rating
	}

	// Check Plex for an existing rating to use as the default on blank input.
	var plexDefault NZRating
	if r.plex != nil {
		// Extract year from "title:YEAR" key.
		year := ""
		if idx := strings.LastIndex(key, ":"); idx >= 0 {
			year = key[idx+1:]
		}
		if rk, err := r.plex.FindMovieByTitle(title, year); err == nil {
			plexDefault, _ = r.plex.GetContentRating(rk)
		}
		// Silently ignore errors (Plex unavailable or movie not found).
	}

	// Build the initial prompt message.
	const validRatings = "G/PG/M/13/RP13/R13/15/RP15/R15/16/RP16/R16/R/18/RP18/R18"
	var promptMsg string
	if plexDefault != RatingNone {
		promptMsg = fmt.Sprintf(
			"No NZ rating found for %q.\nPlex currently has: %s\nEnter rating (%s) to override, or blank to use Plex's value: ",
			title, plexDefault, validRatings,
		)
	} else {
		promptMsg = fmt.Sprintf(
			"No NZ rating found for %q.\nEnter rating (%s)\nTip: check Netflix, or classificationoffice.govt.nz\nLeave blank to skip: ",
			title, validRatings,
		)
	}
	var rating NZRating
	for {
		ans, _ := r.prompter(promptMsg)
		ans = strings.TrimSpace(ans)
		if ans == "" {
			if plexDefault != RatingNone {
				rating = plexDefault
				r.cache.Set(key, rating)
			}
			break // user skipped (or accepted Plex default)
		}
		parsed, ok := ParseNZRating(ans)
		if ok {
			rating = parsed
			r.cache.Set(key, rating)
			break
		}
		promptMsg = fmt.Sprintf("  %q is not a valid rating. Enter (%s) or blank to skip: ", ans, validRatings)
	}
	r.sessionPrompted[key] = rating
	return rating
}

// IsTagsApplied reports whether filePath already has rating r written as Matroska tags.
// Returns false when --overwrite-ratings is active so tags are always rewritten.
func (r *MovieRatingResolver) IsTagsApplied(filePath string, rating NZRating) bool {
	if r.overwrite || r.cache == nil {
		return false
	}
	return r.cache.IsApplied(filePath, rating)
}

// MarkTagsApplied records that filePath has had its rating tags successfully written.
func (r *MovieRatingResolver) MarkTagsApplied(filePath string, rating NZRating) {
	if r.cache != nil {
		r.cache.MarkApplied(filePath, rating)
	}
}

// processFile reads the mediainfo date for filePath, compares it to the
// current MKV header and filesystem timestamps, and updates whatever differs.
// If rr is non-nil the NZ rating is also resolved and written as a tag.
// Returns the processing outcome.
func processFile(filePath string, mi MediaInfoReader, syncer MKVSyncer, timer FileTimer, rr *MovieRatingResolver) FileResult {
	basename := filepath.Base(filePath)

	bestDate, headerDate, miTitle, err := mi.Query(filePath)
	if err != nil {
		fmt.Printf("[ERR]     %s: mediainfo: %v\n", basename, err)
		return ResultError
	}
	if bestDate == "" {
		fmt.Printf("[SKIP]    %s (no date found in metadata)\n", basename)
		return ResultSkipped
	}

	dt, err := parseDate(bestDate)
	if err != nil {
		fmt.Printf("[ERR]     %s: parse date %q: %v\n", basename, bestDate, err)
		return ResultError
	}
	ts := dt.Unix()

	headerNeedsUpdate := bestDate != headerDate

	info, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("[ERR]     %s: stat: %v\n", basename, err)
		return ResultError
	}
	mtimeNeedsUpdate := math.Abs(float64(info.ModTime().Unix()-ts)) > 1
	ctimeNeedsUpdate := timer.CreationTimeNeedsUpdate(filePath, ts)

	// Resolve NZ rating if a resolver is configured.
	var rating NZRating
	var tagsPath string
	if rr != nil {
		year := bestDate
		if len(year) > 4 {
			year = year[:4]
		}
		title := miTitle
		if title == "" {
			title = titleFromFilename(basename, year)
		}
		// Use "title:YEAR" as the cache key (TMDB source may upgrade to numeric ID).
		cacheKey := title + ":" + year
		rating = rr.ResolveMovieRating(cacheKey, title)
		if rating != RatingNone && !rr.IsTagsApplied(filePath, rating) {
			tagsPath, err = writeTempTagsXML(rating)
			if err != nil {
				fmt.Printf("[ERR]     %s: write tags: %v\n", basename, err)
			}
		}
	}

	// Run mkvpropedit once for both date and tags (avoids double mtime churn).
	mkvRan := false
	if (headerNeedsUpdate || tagsPath != "") && syncer != nil {
		dateArg := ""
		if headerNeedsUpdate {
			dateArg = bestDate
		}
		if err := syncer.SyncHeader(filePath, dateArg, tagsPath); err != nil {
			fmt.Printf("[ERR]     %s: sync header: %v\n", basename, err)
		} else {
			mkvRan = true
			if rr != nil && rating != RatingNone && tagsPath != "" {
				rr.MarkTagsApplied(filePath, rating)
			}
		}
	}
	if tagsPath != "" {
		os.Remove(tagsPath)
	}

	// ALWAYS restore timestamps after any mkvpropedit call — it updates mtime as
	// a side effect. This also fixes a pre-existing bug: if the header needed an
	// update but mtime was already correct, mtime would get silently corrupted.
	if mkvRan || mtimeNeedsUpdate {
		if err := timer.SetTimes(filePath, dt); err != nil {
			fmt.Printf("[ERR]     %s: set mtime: %v\n", basename, err)
		}
	}
	if mkvRan || ctimeNeedsUpdate {
		if err := timer.SetCreationTime(filePath, dt); err != nil {
			fmt.Printf("[ERR]     %s: set ctime: %v\n", basename, err)
		}
	}

	if headerNeedsUpdate || mtimeNeedsUpdate || ctimeNeedsUpdate || tagsPath != "" {
		fmt.Printf("[UPDATED] %s -> %s", basename, bestDate)
		if rating != RatingNone {
			fmt.Printf(" (rating: %s)", rating)
		}
		fmt.Println()
		return ResultUpdated
	}
	fmt.Printf("[OK]      %s (no changes needed)\n", basename)
	return ResultOK
}

// writeTempTagsXML writes a Matroska tags XML file for the given NZ rating and
// returns its path. The caller is responsible for deleting the file.
func writeTempTagsXML(rating NZRating) (string, error) {
	f, err := os.CreateTemp("", "mkv-movie-tags-*.xml")
	if err != nil {
		return "", err
	}
	defer f.Close()
	// COUNTRY is a nesting tag; LAW_RATING must be nested inside it.
	xml := fmt.Sprintf(
		"<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>"+
			"<Simple><Name>COUNTRY</Name><String>NZ</String>"+
			"<Simple><Name>LAW_RATING</Name><String>%s</String></Simple></Simple>"+
			"</Tag></Tags>",
		rating,
	)
	if _, err := f.WriteString(xml); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// parseDate parses a date string that is either "YYYY" or "YYYY-MM-DD".
func parseDate(s string) (time.Time, error) {
	if len(s) == 4 {
		year, err := strconv.Atoi(s)
		if err != nil {
			return time.Time{}, err
		}
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.Local), nil
	}
	// Accept any string starting with YYYY-MM-DD.
	if len(s) >= 10 {
		s = s[:10]
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// walkAndProcess walks root for .mkv files and processes each one.
func walkAndProcess(root string, mi MediaInfoReader, syncer MKVSyncer, timer FileTimer, rr *MovieRatingResolver) Stats {
	var stats Stats
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		switch processFile(path, mi, syncer, timer, rr) {
		case ResultUpdated:
			stats.Updated++
		case ResultOK:
			stats.OK++
		case ResultSkipped:
			stats.Skipped++
		case ResultError:
			stats.Errors++
		}
		fmt.Println()
		return nil
	})
	if rr != nil && rr.cache != nil {
		if err := rr.cache.Save(); err != nil {
			fmt.Printf("[!] Failed to save rating cache: %v\n", err)
		}
	}
	return stats
}

// stdinPrompter reads a line from stdin after printing prompt.
func stdinPrompter(prompt string) (string, error) {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	return "", scanner.Err()
}
