package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	path string
}

func newRealMKVApplier() (*realMKVApplier, error) {
	path, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, err
	}
	return &realMKVApplier{path: path}, nil
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
		args = append(args, "--tags", "all:"+tagsPath)
	}
	if posterPath != "" {
		args = append(args,
			"--delete-attachment", "mime-type:image/jpeg",
			"--delete-attachment", "mime-type:image/png",
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
func processFile(filePath string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache) FileResult {
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
			fmt.Printf("[SKIP]    %s (not found on TMDB)\n", basename)
			return ResultSkipped
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
	needsApply := !cache.IsMetaApplied(filePath, tmdbID) || (rr != nil && rr.overwrite)

	if !needsApply && !mtimeNeedsUpdate && !ctimeNeedsUpdate {
		fmt.Printf("[OK]      %s (no changes needed)\n", basename)
		return ResultOK
	}

	// 5. Resolve NZ rating.
	var rating NZRating
	if rr != nil {
		rating = rr.ResolveMovieRating(tmdbID, details.Title)
	}

	// 6. Build tags XML and download poster (only when applying metadata).
	var tagsPath, posterPath string
	if needsApply && applier != nil {
		xml := buildMovieTagXML(details.Overview, tmdbID, details.ReleaseDate, rating)
		if xml != "" {
			tagsPath, err = writeTempTagFile(xml)
			if err != nil {
				fmt.Printf("[ERR]     %s: write tags: %v\n", basename, err)
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
			// Push rating to Plex if configured.
			if rr != nil && rr.plex != nil && rating != RatingNone {
				rk, plexErr := rr.plex.FindMovieByTMDBID(tmdbID)
				if plexErr != nil {
					rk, plexErr = rr.plex.FindMovieByTitle(details.Title, year)
				}
				if plexErr == nil {
					if err := rr.plex.SetContentRating(rk, rating); err != nil {
						fmt.Printf("  [!] Plex rating update failed: %v\n", err)
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
	fmt.Println()
	return ResultUpdated
}

// buildMovieTagXML constructs the Matroska tags XML for the given movie metadata.
// Returns an empty string when there is nothing to tag.
func buildMovieTagXML(overview, tmdbID, dateStr string, rating NZRating) string {
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
	resp, err := http.Get(posterURL)
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

// extractYear returns the first 4-digit year found in filename (1900–2099),
// or "" if none.
func extractYear(filename string) string {
	m := yearRe.FindStringSubmatch(" " + filename + " ")
	if m != nil {
		return m[1]
	}
	return ""
}

// walkAndProcess walks root for .mkv files and processes each one.
func walkAndProcess(root string, tmdb TMDBMovieClient, applier MKVApplier, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache) Stats {
	var stats Stats
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		switch processFile(path, tmdb, applier, timer, rr, cache) {
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

// stdinPrompter reads a line from stdin after printing prompt.
func stdinPrompter(prompt string) (string, error) {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	return "", scanner.Err()
}
