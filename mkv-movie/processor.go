package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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

// MKVApplier applies metadata to an MKV file via mkvpropedit in a single call,
// reading the existing tags with mkvextract first so they are kept.
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
	Skipped    int
	SkippedOld int
	Errors     int
}

// FileResult is the outcome of processing a single file.
type FileResult int

const (
	ResultUpdated FileResult = iota
	ResultSkipped
	ResultError
)

// MovieRatingResolver resolves an NZ content rating for a movie: from the
// cache, else from external sources (confirmed by the user), else by prompting.
type MovieRatingResolver struct {
	sources          []RatingSource
	cache            *MovieRatingCache
	overwrite        bool
	overwriteStreams bool
	plex             PlexMovieClient // nil if not configured
	prompter         func(string) (string, error)
	sessionPrompted  map[string]NZRating // tmdbID → rating already settled this run
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

// ResolveMovieRating returns the best NZ rating for the movie tmdbID. title is
// used for Classification Office lookups and prompts; title and year (the
// release year) find the movie in Plex when its TMDB GUID doesn't.
//
// A cached rating is returned silently. Otherwise a rating found by a source
// is shown for the user to confirm or override, as mkv-tvshow does; with no
// source data the user is prompted, with Plex's rating as the default.
// --overwrite-ratings skips the cache, but keeps the cached rating when no
// source has one.
func (r *MovieRatingResolver) ResolveMovieRating(tmdbID, title, year string) NZRating {
	if r.cache == nil {
		return RatingNone
	}
	// At most one prompt per movie per run.
	if rating, already := r.sessionPrompted[tmdbID]; already {
		return rating
	}

	cached, haveCached := r.cache.Get(tmdbID)
	if haveCached && !r.overwrite {
		return cached
	}

	var suggested NZRating
	sourceFailed := false
	for _, src := range r.sources {
		rating, err := src.FetchRating(tmdbID, title)
		if err != nil {
			fmt.Printf("  [!] %s rating error: %v\n", src.Name(), err)
			sourceFailed = true
			continue
		}
		if rating != RatingNone {
			suggested = rating
			break
		}
	}

	// --overwrite-ratings: keep the cached rating when no source disagrees with it.
	if haveCached && (suggested == RatingNone || suggested == cached) {
		switch {
		case suggested == cached:
		case sourceFailed:
			fmt.Printf("  [i] No rating source answered for %q; keeping cached %s\n", title, cached)
		default:
			fmt.Printf("  [i] No source has a rating for %q; keeping cached %s\n", title, cached)
		}
		r.sessionPrompted[tmdbID] = cached
		return cached
	}

	var plexDefault NZRating
	if suggested == RatingNone {
		plexDefault = r.plexRatingDefault(tmdbID, title, year)
	}

	var promptMsg string
	blankDoes := "skip" // what a blank answer does, for the invalid-input re-prompt
	switch {
	case suggested != RatingNone:
		was := ""
		if haveCached {
			was = fmt.Sprintf(" (cached: %s)", cached)
		}
		promptMsg = fmt.Sprintf(
			"NZ rating for %q: %s%s — press enter to confirm, or enter a new rating (%s): ",
			title, suggested, was, validRatings,
		)
		blankDoes = "accept " + string(suggested)
	case plexDefault != RatingNone:
		promptMsg = fmt.Sprintf(
			"No NZ rating found for %q.\nPlex currently has: %s\nEnter rating (%s) to override, or blank to use Plex's value: ",
			title, plexDefault, validRatings,
		)
		blankDoes = "use Plex's " + string(plexDefault)
	default:
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
			if suggested != RatingNone {
				rating = suggested
			} else {
				rating = plexDefault
			}
			if rating != RatingNone {
				r.cache.Set(tmdbID, rating)
			}
			break
		}
		parsed, ok := ParseNZRating(ans)
		if ok {
			rating = parsed
			r.cache.Set(tmdbID, rating)
			break
		}
		promptMsg = fmt.Sprintf("  %q is not a valid rating. Enter (%s) or blank to %s: ", ans, validRatings, blankDoes)
	}
	r.sessionPrompted[tmdbID] = rating
	return rating
}

// plexRatingDefault returns the rating Plex already has for the movie, offered
// as the prompt's default. It looks the movie up by TMDB GUID, then by title
// and release year, which keeps a remake from picking up the other film's
// rating. It falls back only when the GUID lookup found nothing; a failed
// lookup is reported and gives no default.
func (r *MovieRatingResolver) plexRatingDefault(tmdbID, title, year string) NZRating {
	if r.plex == nil {
		return RatingNone
	}
	rk, err := r.plex.FindMovieByTMDBID(tmdbID)
	if errors.Is(err, ErrPlexMovieNotFound) || (err == nil && rk == "") {
		rk, err = r.plex.FindMovieByTitle(title, year)
	}
	if errors.Is(err, ErrPlexMovieNotFound) || (err == nil && rk == "") {
		fmt.Printf("  [i] Plex: %q not found (tried TMDB GUID, then title and year)\n", title)
		return RatingNone
	}
	if err != nil {
		fmt.Printf("  [!] Plex: lookup failed for %q: %v\n", title, err)
		return RatingNone
	}
	rating, err := r.plex.GetContentRating(rk)
	if err != nil {
		fmt.Printf("  [!] Plex: rating fetch failed for %q: %v\n", title, err)
		return RatingNone
	}
	if rating == RatingNone {
		fmt.Printf("  [i] Plex: %q found but no NZ content rating is set\n", title)
	}
	return rating
}

// processFile looks up the movie on TMDB by filename, fetches full metadata,
// and applies it to the MKV file (title, date header, tags, poster, timestamps).
func processFile(filePath string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache, manualData map[string]MovieManualEntry) FileResult {
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
		cache.SetFileTMDBID(filePath, tmdbID)
	} else {
		cache.SetFileTMDBID(filePath, tmdbID) // before the fetch, so a TMDB error keeps the mapping
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

	// 4. Every file in the --cutoff-date/--all window is retagged when MKVToolNix
	// is available (applier != nil), and its timestamps restored, as in mkv-tvshow.
	// The Plex rating is pushed when it differs from the last one pushed, so a
	// manually corrected cache entry reaches Plex.
	plexRatingNeedsUpdate := func(rating NZRating) bool {
		if rr == nil || rr.plex == nil || rating == RatingNone {
			return false
		}
		last, ok := cache.GetPlexRating(tmdbID)
		return !ok || last != rating
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
		return !ok || applied != langSig || rr.overwriteStreams
	}

	// 5. Resolve the NZ rating: it goes into the tags and to Plex.
	var rating NZRating
	if rr != nil {
		rating = rr.ResolveMovieRating(tmdbID, details.Title, releaseYear(details.ReleaseDate))
	}

	// 6. Build tags XML and download poster (only when MKVToolNix can write them).
	var deband bool
	if entry.Deband != nil {
		deband = *entry.Deband
	}
	var tagsPath, posterPath string
	var failed []string // steps that failed, named on the result line
	if applier != nil {
		xml := buildMovieTagXML(details.Overview, tmdbID, details.ReleaseDate, rating, deband)
		if xml != "" {
			tagsPath, err = writeTempTagFile(xml)
			if err != nil {
				fmt.Printf("[ERR]     %s: write tags: %v\n", basename, err)
				return ResultError
			}
		}
		if url := details.PosterURL(); url != "" {
			if posterPath, err = downloadPoster(url); err != nil {
				fmt.Printf("  [!] Poster download failed; cover unchanged: %v\n", err)
				failed = append(failed, "poster")
			}
		}
	}

	// 7. Apply metadata via mkvpropedit.
	mkvRan := false
	if applier != nil {
		if err := applier.Apply(filePath, details.Title, details.ReleaseDate, tagsPath, posterPath); err != nil {
			fmt.Printf("  [!] Writing tags failed: %v\n", err)
		} else {
			mkvRan = true
		}
	}

	// Push rating and stream selection to Plex when either needs updating.
	// Both are gated on what was last pushed, so corrected cache entries are propagated.
	var langSet string
	var langAltSuffix string
	ratingPushed := false
	if plexRatingNeedsUpdate(rating) || plexStreamsNeedSet() {
		rk, plexErr := rr.plex.FindMovieByTMDBID(tmdbID)
		if errors.Is(plexErr, ErrPlexMovieNotFound) {
			rk, plexErr = rr.plex.FindMovieByTitle(details.Title, releaseYear(details.ReleaseDate))
		}
		if errors.Is(plexErr, ErrPlexMovieNotFound) {
			// Nothing is recorded as pushed. The file is still backdated once
			// tagged, so only a later --all (or older --cutoff-date) run pushes.
			fmt.Printf("  [!] Not in Plex library – rating and streams not pushed; rerun with --all once Plex has it\n")
		} else if plexErr == nil {
			if plexRatingNeedsUpdate(rating) {
				if err := rr.plex.SetContentRating(rk, rating); err != nil {
					fmt.Printf("  [!] Plex rating update failed: %v\n", err)
					failed = append(failed, "Plex rating")
				} else {
					cache.SetPlexRating(tmdbID, rating)
					ratingPushed = true
				}
			}
			if plexStreamsNeedSet() {
				audioSet, subtitleSet, err := rr.plex.SetMovieStreams(rk, langs)
				if err != nil {
					fmt.Printf("  [!] Plex stream update failed (key=%s lang=%s): %v\n", rk, langSig, err)
					if errors.Is(err, ErrPlexNoAudioStream) {
						fmt.Printf("  [i] Set \"language\" for %q in manual_data.json to a language the file has\n", stem)
					}
					failed = append(failed, "Plex streams")
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
		} else {
			fmt.Printf("  [!] Plex lookup failed – rating and streams not pushed: %v\n", plexErr)
			failed = append(failed, "Plex lookup")
		}
	}

	if tagsPath != "" {
		os.Remove(tagsPath)
	}
	if posterPath != "" {
		os.Remove(posterPath)
	}

	// No tags were written, so don't backdate the file: that would move it out
	// of the --cutoff-date window before it is ever tagged.
	if applier == nil {
		tag, result := "[SKIP]   ", ResultSkipped
		if len(failed) > 0 {
			tag, result = "[ERR]    ", ResultError
		}
		fmt.Printf("%s %s (mkvpropedit/mkvextract not found; file not changed)", tag, basename)
		if ratingPushed {
			fmt.Printf(" (Plex rating: %s)", rating)
		}
		if langSet != "" {
			fmt.Printf(" (Plex lang: %s%s)", langSet, langAltSuffix)
		}
		printFailed(failed)
		return result
	}

	// The tags weren't written: leave the timestamps alone so the file stays in
	// the --cutoff-date window, and the next run tries again, as mkv-tvshow does.
	if !mkvRan {
		fmt.Printf("[ERR]     %s: tags not written", basename)
		if ratingPushed {
			fmt.Printf(" (Plex rating: %s)", rating)
		}
		if langSet != "" {
			fmt.Printf(" (Plex lang: %s%s)", langSet, langAltSuffix)
		}
		printFailed(failed)
		return ResultError
	}

	// 8. Restore timestamps — mkvpropedit updates mtime as a side effect.
	if err := timer.SetTimes(filePath, dt); err != nil {
		fmt.Printf("  [!] Setting the modified time failed: %v\n", err)
		failed = append(failed, "mtime")
	}
	if err := timer.SetCreationTime(filePath, dt); err != nil {
		fmt.Printf("  [!] Setting the creation time failed: %v\n", err)
		failed = append(failed, "ctime")
	}

	tag, result := "[UPDATED]", ResultUpdated
	if len(failed) > 0 {
		tag, result = "[ERR]    ", ResultError
	}
	fmt.Printf("%s %s -> %s", tag, basename, details.ReleaseDate)
	if rating != RatingNone {
		fmt.Printf(" (rating: %s)", rating)
	}
	if langSet != "" {
		fmt.Printf(" (lang: %s%s)", langSet, langAltSuffix)
	}
	printFailed(failed)
	return result
}

// printFailed ends a result line, naming the steps that failed.
func printFailed(failed []string) {
	if len(failed) > 0 {
		fmt.Printf(" (failed: %s)", strings.Join(failed, ", "))
	}
	fmt.Println()
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
// A non-200 status or an empty body is an error.
func downloadPoster(posterURL string) (string, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(posterURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "mkv-movie-poster-*.jpg")
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err == nil && n == 0 {
		err = errors.New("empty response body")
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
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

// releaseYear returns the year of a TMDB release date (YYYY-MM-DD), or "".
func releaseYear(date string) string {
	if len(date) < 4 {
		return ""
	}
	return date[:4]
}

// walkAndProcess walks root for .mkv files and processes each one.
func walkAndProcess(root string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache, manualData map[string]MovieManualEntry, cutoff time.Time) Stats {
	var stats Stats
	var saveErr error // the last save's error; only the first in a run of failures is printed
	lastSaved := ""   // the file after which the cache was last saved
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
		switch processFile(path, tmdb, applier, timer, rr, cache, manualData) {
		case ResultUpdated:
			stats.Updated++
		case ResultSkipped:
			stats.Skipped++
		case ResultError:
			stats.Errors++
		}
		// Save after each file so an interrupted run keeps the ratings and
		// TMDB IDs resolved so far.
		err = cache.Save()
		switch {
		case err != nil && saveErr == nil:
			fmt.Printf("[!] Failed to save rating cache: %v\n    Retrying after each file; until a save succeeds, changes are kept in memory only.\n", err)
		case err == nil && saveErr != nil:
			fmt.Println("[i] Rating cache saved.")
		}
		if err == nil {
			lastSaved = d.Name()
		}
		saveErr = err
		fmt.Println()
		return nil
	})
	if saveErr != nil {
		// One more try: on Windows the rename fails while another program
		// (antivirus, an indexer) briefly holds the cache open.
		beforeFinalSave()
		if err := cache.Save(); err == nil {
			fmt.Println("[i] Rating cache saved.")
		} else if lastSaved == "" {
			fmt.Printf("[!] The rating cache was never saved; this run's ratings, TMDB IDs and Plex state are lost: %v\n", err)
		} else {
			fmt.Printf("[!] The rating cache was last saved after %s; later ratings, TMDB IDs and Plex state are lost: %v\n", lastSaved, err)
		}
	}
	return stats
}

// beforeFinalSave runs before walkAndProcess's last save attempt; tests replace it.
var beforeFinalSave = func() { time.Sleep(time.Second) }

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

// stdinScanner is shared by every prompt: a scanner per prompt would read ahead
// and drop the answers to later prompts when stdin is piped.
var stdinScanner = bufio.NewScanner(os.Stdin)

// stdinPrompter reads a line from stdin after printing prompt.
func stdinPrompter(prompt string) (string, error) {
	fmt.Print(prompt)
	if stdinScanner.Scan() {
		return stdinScanner.Text(), nil
	}
	return "", stdinScanner.Err()
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
