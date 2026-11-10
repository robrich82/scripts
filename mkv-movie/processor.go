package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TMDBMovieClient fetches movie details from TMDB.
type TMDBMovieClient interface {
	// SearchAndFetch searches for title+year and returns the TMDB ID and full
	// details. Returns ("", MovieDetails{}, nil) when no match is found.
	SearchAndFetch(title, year string) (tmdbID string, details MovieDetails, err error)
	// GetMovieDetails fetches full details for a known TMDB ID.
	GetMovieDetails(tmdbID string) (MovieDetails, error)
}

// MKVApplier applies metadata to an MKV file via mkvpropedit in a single call.
type MKVApplier interface {
	// Apply sets the title, date header, tags, and poster on filePath.
	// Pass empty strings to skip individual fields.
	// posterPath causes existing jpeg/png attachments to be deleted and
	// posterPath to be attached in their place.
	Apply(filePath, title, dateStr, tagsPath, posterPath string) error
}

type realMKVApplier struct {
	path        string
	extractTags func(filePath string) (string, error)
}

// newRealMKVApplier needs mkvextract as well as mkvpropedit: writing tags
// without reading the existing ones first would delete them.
func newRealMKVApplier() (*realMKVApplier, error) {
	tagger := newRealMKVTagger()
	if !tagger.Available() {
		return nil, errors.New("mkvpropedit or mkvextract not found in PATH")
	}
	return &realMKVApplier{path: tagger.mkvpropedit, extractTags: tagger.ExtractTags}, nil
}

// mergedTagsFile writes the tags at tagsPath, merged into filePath's existing
// tags, to a new temp file and returns its path.
func (a *realMKVApplier) mergedTagsFile(filePath, tagsPath string) (string, error) {
	fresh, err := os.ReadFile(tagsPath)
	if err != nil {
		return "", err
	}
	existing, err := a.extractTags(filePath)
	if err != nil {
		return "", err
	}
	merged, err := mergeMovieTags(existing, string(fresh))
	if err != nil {
		return "", err
	}
	return writeTempTagFile(merged)
}

func (a *realMKVApplier) Apply(filePath, title, dateStr, tagsPath, posterPath string) error {
	args := []string{filePath}

	if title != "" || dateStr != "" {
		args = append(args, "--edit", "info")
		if title != "" {
			args = append(args, "--set", "title="+title)
		}
		if dateStr != "" {
			formatted := dateStr
			if len(formatted) == 4 {
				formatted = formatted + "-01-01"
			}
			args = append(args, "--set", "date="+formatted+"T00:00:00Z")
		}
	}
	if tagsPath != "" {
		merged, err := a.mergedTagsFile(filePath, tagsPath)
		if err != nil {
			return fmt.Errorf("tags: %w", err)
		}
		defer os.Remove(merged)
		args = append(args, "--tags", "all:"+merged)
	}
	if posterPath != "" {
		args = append(args,
			"--delete-attachment", "mime-type:image/jpeg",
			"--delete-attachment", "mime-type:image/png",
			"--attachment-name", "cover.jpg",
			"--attachment-mime-type", "image/jpeg",
			"--add-attachment", posterPath)
	}
	if len(args) == 1 {
		return nil // nothing to do
	}

	cmd := exec.Command(a.path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit code 1 = warnings only (e.g. no attachment found to delete).
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stats counts processing outcomes.
type Stats struct {
	Updated    int
	OK         int
	Skipped    int
	SkippedOld int
	Errors     int
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
	sources          []RatingSource
	cache            *MovieRatingCache
	overwrite        bool
	overwriteStreams bool
	plex             PlexMovieClient // nil if not configured
	prompter         func(string) (string, error)
	sessionPrompted  map[string]NZRating // key → rating already prompted this run
}

func newMovieRatingResolver(
	sources []RatingSource,
	cache *MovieRatingCache,
	overwrite bool,
	overwriteStreams bool,
	plex PlexMovieClient,
	prompter func(string) (string, error),
) *MovieRatingResolver {
	return &MovieRatingResolver{
		sources:          sources,
		cache:            cache,
		overwrite:        overwrite,
		overwriteStreams: overwriteStreams,
		plex:             plex,
		prompter:         prompter,
		sessionPrompted:  make(map[string]NZRating),
	}
}

// ResolveMovieRating returns the best NZ rating for the given movie.
// key is the TMDB numeric ID (preferred) or a "title:YEAR" fallback.
// title is the human-readable name used for Classification Office lookups and prompts.
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
		year := ""
		if idx := strings.LastIndex(key, ":"); idx >= 0 {
			year = key[idx+1:]
		}
		if rk, err := r.plex.FindMovieByTitle(title, year); err == nil {
			plexDefault, _ = r.plex.GetContentRating(rk)
		}
	}

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
			break
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

// processFile looks up the movie on TMDB by filename, fetches full metadata,
// and applies it to the MKV file (title, date header, tags, poster, timestamps).
func processFile(filePath string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache, manualData map[string]MovieManualEntry, force bool) FileResult {
	basename := filepath.Base(filePath)

	// 1. Derive title and year from filename.
	year := extractYear(basename)
	title := titleFromFilename(basename, year)
	if title == "" {
		fmt.Printf("[SKIP]    %s (cannot derive title from filename)\n", basename)
		return ResultSkipped
	}

	// 2. Resolve TMDB ID — cached or searched.
	cacheKey := title + ":" + year
	tmdbID := cache.GetTMDBID(cacheKey)
	var details MovieDetails
	var err error
	if tmdbID == "" {
		tmdbID, details, err = tmdb.SearchAndFetch(title, year)
		if err != nil {
			fmt.Printf("[ERR]     %s: TMDB search: %v\n", basename, err)
			return ResultError
		}
		if tmdbID == "" {
			// Automatic search failed - let the user supply the ID directly.
			var ok bool
			tmdbID, details, ok = rr.promptTMDBID(title, year, tmdb)
			if !ok {
				fmt.Printf("[SKIP]    %s (not found on TMDB)\n", basename)
				return ResultSkipped
			}
		}
		cache.SetTMDBID(cacheKey, tmdbID)
		// Migrate any existing title:YEAR rating to the numeric TMDB ID key.
		if r, ok := cache.Get(cacheKey); ok && r != RatingNone {
			if _, ok2 := cache.Get(tmdbID); !ok2 {
				cache.Set(tmdbID, r)
			}
		}
	} else {
		details, err = tmdb.GetMovieDetails(tmdbID)
		if err != nil {
			fmt.Printf("[ERR]     %s: TMDB details: %v\n", basename, err)
			return ResultError
		}
	}

	// 3. Parse release date.
	if details.ReleaseDate == "" {
		fmt.Printf("[SKIP]    %s (no release date on TMDB)\n", basename)
		return ResultSkipped
	}
	dt, err := parseDate(details.ReleaseDate)
	if err != nil {
		fmt.Printf("[ERR]     %s: parse date %q: %v\n", basename, details.ReleaseDate, err)
		return ResultError
	}
	ts := dt.Unix()

	// 4. Check what needs doing.
	info, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("[ERR]     %s: stat: %v\n", basename, err)
		return ResultError
	}
	mtimeNeedsUpdate := math.Abs(float64(info.ModTime().Unix()-ts)) > 1
	ctimeNeedsUpdate := timer.CreationTimeNeedsUpdate(filePath, ts)
	needsApply := !cache.IsMetaApplied(filePath, tmdbID) || (rr != nil && rr.overwrite) || force

	// Determine whether the Plex rating needs updating independently of needsApply,
	// so that a manually corrected cache entry is always pushed to Plex.
	plexRatingNeedsUpdate := func(rating NZRating) bool {
		if rr == nil || rr.plex == nil || rating == RatingNone {
			return false
		}
		last, ok := cache.GetPlexRating(tmdbID)
		return force || !ok || last != rating
	}

	// Plex audio/subtitle preferences per account: manual_data's "language" if
	// set, otherwise TMDB's original_language, with "alt-language" and
	// "alt2-language" overriding it for the corresponding alt accounts.
	stem := strings.TrimSuffix(basename, filepath.Ext(basename))
	entry := manualData[stem]
	langs := entry.audioLangs(details.OriginalLanguage)
	// What to cache as "already applied": the language every configured account
	// receives, so adding an account re-applies rather than being skipped.
	langSig := ""
	if rr != nil && rr.plex != nil {
		langSig = appliedLangSignature(rr.plex, langs)
	}

	plexStreamsNeedSet := func() bool {
		if rr == nil || rr.plex == nil || langs.Default == "" {
			return false
		}
		applied, ok := cache.GetStreamsApplied(tmdbID)
		return force || !ok || applied != langSig || rr.overwriteStreams
	}

	// 5. Resolve NZ rating early so we can include it in the needs-update check.
	var rating NZRating
	if rr != nil {
		rating = rr.ResolveMovieRating(tmdbID, details.Title)
	}

	if !needsApply && !mtimeNeedsUpdate && !ctimeNeedsUpdate && !plexRatingNeedsUpdate(rating) && !plexStreamsNeedSet() {
		fmt.Printf("[OK]      %s (no changes needed)\n", basename)
		return ResultOK
	}

	// 6. Build tags XML and download poster (only when applying metadata).
	var deband bool
	if entry.Deband != nil {
		deband = *entry.Deband
	}
	var tagsPath, posterPath string
	if needsApply && applier != nil {
		xml := buildMovieTagXML(details.Overview, tmdbID, details.ReleaseDate, rating, deband)
		if xml != "" {
			tagsPath, err = writeTempTagFile(xml)
			if err != nil {
				fmt.Printf("[ERR]     %s: write tags: %v\n", basename, err)
				return ResultError
			}
		}
		if url := details.PosterURL(); url != "" {
			posterPath = downloadPoster(url)
		}
	}

	// 7. Apply metadata via mkvpropedit.
	mkvRan := false
	if needsApply && applier != nil {
		if err := applier.Apply(filePath, details.Title, details.ReleaseDate, tagsPath, posterPath); err != nil {
			fmt.Printf("[ERR]     %s: apply metadata: %v\n", basename, err)
		} else {
			mkvRan = true
			cache.MarkMetaApplied(filePath, tmdbID)
		}
	}

	// Push rating and stream selection to Plex when either needs updating.
	// Both are independent of needsApply so corrected cache entries are always propagated.
	var langSet string
	var langAltSuffix string
	if plexRatingNeedsUpdate(rating) || plexStreamsNeedSet() {
		rk, plexErr := rr.plex.FindMovieByTMDBID(tmdbID)
		if plexErr != nil {
			rk, plexErr = rr.plex.FindMovieByTitle(details.Title, year)
		}
		if errors.Is(plexErr, ErrPlexMovieNotFound) {
			fmt.Printf("  [!] Not in Plex library – rating and streams not pushed\n")
			// Cache what we would have set so subsequent runs skip retrying.
			// Re-run with --force to retry after adding the movie to Plex.
			if rating != RatingNone {
				cache.SetPlexRating(tmdbID, rating)
			}
			if langs.Default != "" {
				cache.SetStreamsApplied(tmdbID, langSig)
			}
		} else if plexErr == nil {
			if plexRatingNeedsUpdate(rating) {
				if err := rr.plex.SetContentRating(rk, rating); err != nil {
					fmt.Printf("  [!] Plex rating update failed: %v\n", err)
				} else {
					cache.SetPlexRating(tmdbID, rating)
				}
			}
			if plexStreamsNeedSet() {
				audioSet, subtitleSet, err := rr.plex.SetMovieStreams(rk, langs)
				if err != nil {
					fmt.Printf("  [!] Plex stream update failed (key=%s lang=%s): %v\n", rk, langSig, err)
				} else {
					cache.SetStreamsApplied(tmdbID, langSig)
					if audioSet || subtitleSet {
						langSet = langSig
					}
					if r, ok := rr.plex.(multiAccountReporter); ok {
						langAltSuffix = r.altErrSuffix()
					}
				}
			}
		}
	}

	if tagsPath != "" {
		os.Remove(tagsPath)
	}
	if posterPath != "" {
		os.Remove(posterPath)
	}

	// 8. Restore timestamps — mkvpropedit updates mtime as a side effect.
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

	fmt.Printf("[UPDATED] %s -> %s", basename, details.ReleaseDate)
	if rating != RatingNone {
		fmt.Printf(" (rating: %s)", rating)
	}
	if langSet != "" {
		fmt.Printf(" (lang: %s%s)", langSet, langAltSuffix)
	}
	fmt.Println()
	return ResultUpdated
}

// buildMovieTagXML constructs the Matroska tags XML for the given movie metadata.
// Returns an empty string when there is nothing to tag.
func buildMovieTagXML(overview, tmdbID, dateStr string, rating NZRating, deband bool) string {
	var elements []string

	if dateStr != "" {
		datePart := dateStr
		if len(datePart) > 10 {
			datePart = datePart[:10]
		}
		yearPart := datePart[:4]
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>DATE</Name><String>%s</String></Simple>", datePart),
			fmt.Sprintf("<Simple><Name>YEAR</Name><String>%s</String></Simple>", yearPart),
		)
	}
	if overview != "" {
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>SUMMARY</Name><String>%s</String></Simple>", xmlEscape(overview)),
		)
	}
	if tmdbID != "" {
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>TMDB_ID</Name><String>%s</String></Simple>", tmdbID),
		)
	}
	if rating != RatingNone {
		elements = append(elements, fmt.Sprintf(
			"<Simple><Name>COUNTRY</Name><String>NZ</String>"+
				"<Simple><Name>LAW_RATING</Name><String>%s</String></Simple></Simple>",
			rating))
	}
	if deband {
		elements = append(elements,
			"<Simple><Name>DEBAND</Name><String>true</String></Simple>",
		)
	}

	if len(elements) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>%s</Tag></Tags>",
		strings.Join(elements, ""),
	)
}

// writeTempTagFile writes an XML string to a temp file and returns its path.
// The caller is responsible for deleting the file.
func writeTempTagFile(xml string) (string, error) {
	f, err := os.CreateTemp("", "mkv-movie-tags-*.xml")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(xml); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// downloadPoster fetches a poster image to a temp file and returns its path.
// Returns "" on any failure.
func downloadPoster(posterURL string) string {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(posterURL)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()

	f, err := os.CreateTemp("", "mkv-movie-poster-*.jpg")
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

// xmlEscape replaces XML special characters so they are safe inside tag values.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
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

// extractYear returns the last 4-digit year found in filename (1900–2099),
// or "" if none. The last match is used so a number in the title isn't mistaken
// for the release year (see lastYearMatch).
func extractYear(filename string) string {
	year, _ := lastYearMatch(filename)
	return year
}

// walkAndProcess walks root for .mkv files and processes each one.
func walkAndProcess(root string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache, manualData map[string]MovieManualEntry, force bool, cutoff time.Time) Stats {
	var stats Stats
	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("[!] Skipping unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		if !cutoff.IsZero() {
			info, err := d.Info()
			if err == nil && info.ModTime().Before(cutoff) {
				stats.SkippedOld++
				return nil
			}
		}
		switch processFile(path, tmdb, applier, timer, rr, cache, manualData, force) {
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
	if cache != nil {
		if err := cache.Save(); err != nil {
			fmt.Printf("[!] Failed to save rating cache: %v\n", err)
		}
	}
	return stats
}

// applyDebandOnlyFile updates only the DEBAND MKV tag for filePath, preserving
// all other existing tags and restoring the original file timestamps afterward.
func applyDebandOnlyFile(filePath string, deband bool, tagger MKVTagger, timer FileTimer) bool {
	if !tagger.Available() {
		fmt.Println("  [!] mkvpropedit or mkvextract not found in PATH")
		return false
	}

	origStat, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("  [!] Cannot read timestamps to restore, not tagging: %v\n", err)
		return false
	}

	currentXML, err := tagger.ExtractTags(filePath)
	if err != nil {
		fmt.Printf("  [!] Failed to extract tags: %v\n", err)
		return false
	}

	updatedXML, err := updateDebandInXML(currentXML, deband)
	if err != nil {
		fmt.Printf("  [!] Failed to update tags: %v\n", err)
		return false
	}

	tagsPath, err := tagger.WriteTempTags(updatedXML)
	if err != nil {
		fmt.Printf("  [!] Failed to write tags XML: %v\n", err)
		return false
	}
	defer tagger.Cleanup(tagsPath)

	if err := tagger.ApplyTags(filePath, tagsPath); err != nil {
		fmt.Printf("  [!] mkvpropedit failed: %v\n", err)
		return false
	}

	if err := timer.SetTimes(filePath, origStat.ModTime()); err != nil {
		fmt.Printf("  [!] Timestamp restore error: %v\n", err)
	}
	if err := timer.SetCreationTime(filePath, origStat.ModTime()); err != nil {
		fmt.Printf("  [!] Timestamp restore error: %v\n", err)
	}
	return true
}

// runDebandOnly scans dir and updates only the DEBAND MKV tag for files that
// have an explicit deband entry in manualData. All other tags and timestamps
// are preserved. Files not in manualData are skipped entirely.
func runDebandOnly(dir string, manualData map[string]MovieManualEntry, tagger MKVTagger, timer FileTimer) Stats {
	var stats Stats
	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("[!] Skipping unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		stem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		entry, ok := manualData[stem]
		if !ok || entry.Deband == nil {
			return nil
		}
		deband := *entry.Deband
		if applyDebandOnlyFile(path, deband, tagger, timer) {
			fmt.Printf("[✓] Deband updated: %s\n", d.Name())
			stats.Updated++
		} else {
			fmt.Printf("  [X] Deband update failed: %s\n", d.Name())
			stats.Errors++
		}
		return nil
	})
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

// tmdbIDPattern matches a bare numeric TMDB ID or one embedded in a
// themoviedb.org URL (e.g. https://www.themoviedb.org/movie/12345-some-title).
var tmdbIDPattern = regexp.MustCompile(`^(?:(?:https?://)?(?:www\.)?themoviedb\.org/movie/)?(\d+)(?:[-/?#].*)?$`)

// parseTMDBID extracts a numeric TMDB movie ID from user input, accepting
// either a bare ID or a themoviedb.org movie URL. Returns "" if none is found.
func parseTMDBID(s string) string {
	m := tmdbIDPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return m[1]
}

// promptTMDBID asks the user for a TMDB ID after an automatic search failed,
// re-prompting until the entered ID resolves to real details. Returns ok=false
// if the user leaves the prompt blank (or stdin is closed), meaning "skip".
func (r *MovieRatingResolver) promptTMDBID(title, year string, tmdb TMDBMovieClient) (string, MovieDetails, bool) {
	if r == nil || r.prompter == nil {
		return "", MovieDetails{}, false
	}
	label := title
	if year != "" {
		label = fmt.Sprintf("%s (%s)", title, year)
	}
	promptMsg := fmt.Sprintf(
		"No TMDB match for %q.\nEnter TMDB ID or themoviedb.org URL, or blank to skip: ", label)
	for {
		ans, err := r.prompter(promptMsg)
		if err != nil {
			return "", MovieDetails{}, false
		}
		ans = strings.TrimSpace(ans)
		if ans == "" {
			return "", MovieDetails{}, false
		}
		id := parseTMDBID(ans)
		if id == "" {
			promptMsg = fmt.Sprintf("  %q is not a TMDB ID or movie URL. Enter one, or blank to skip: ", ans)
			continue
		}
		details, err := tmdb.GetMovieDetails(id)
		if err != nil {
			promptMsg = fmt.Sprintf("  TMDB ID %s: %v\n  Enter another ID, or blank to skip: ", id, err)
			continue
		}
		fmt.Printf("  Using TMDB %s: %s (%s)\n", id, details.Title, details.ReleaseDate)
		return id, details, true
	}
}
