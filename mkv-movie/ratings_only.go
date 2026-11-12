package main

import (
	"errors"
	"fmt"
	"path/filepath"
)

// processRatingOnly writes the movie's NZ rating into filePath's existing tags,
// restoring its timestamps, and pushes the rating to Plex. The tags are left
// alone when the cache records this rating as written to them (unless
// --overwrite-ratings), and Plex when it was the last rating pushed. tagger is
// nil when MKVToolNix isn't installed: only Plex is updated then. rr must not
// be nil.
//
// TMDB details are fetched only when the rating must be looked up, or pushed:
// Plex's title lookup needs TMDB's title and release year, not the filename's.
func processRatingOnly(filePath string, tmdb TMDBMovieClient, tagger MKVTagger, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache) FileResult {
	basename := filepath.Base(filePath)
	year := extractYear(basename)
	title := titleFromFilename(basename, year)
	if title == "" {
		fmt.Printf("[SKIP]    %s (cannot derive title from filename)\n", basename)
		return ResultSkipped
	}

	tmdbID, details, result, ok := lookupMovie(filePath, title, year, tmdb, rr, cache)
	if !ok {
		return result
	}
	// fetchDetails sets title and year from TMDB, keeping the filename's year
	// when TMDB has no release date.
	fetchDetails := func() error {
		if details == nil {
			d, err := tmdb.GetMovieDetails(tmdbID)
			if err != nil {
				return err
			}
			details = &d
		}
		title = details.Title
		if y := releaseYear(details.ReleaseDate); y != "" {
			year = y
		}
		return nil
	}

	if details != nil || !rr.settled(tmdbID) {
		if err := fetchDetails(); err != nil {
			fmt.Printf("[ERR]     %s: TMDB details: %v\n", basename, err)
			return ResultError
		}
	}
	rating := rr.ResolveMovieRating(tmdbID, title, year)
	if rating == RatingNone {
		fmt.Printf("[SKIP]    %s (no NZ rating)\n", basename)
		return ResultSkipped
	}

	var failed []string
	var tagged, tagsFailed bool
	if tagger != nil && (rr.overwrite || !cache.IsApplied(filePath, rating)) {
		failed, tagged = rewriteTags(filePath, tagger, timer, func(xml string) (string, error) {
			return updateRatingInXML(xml, rating)
		})
		if tagged {
			cache.MarkApplied(filePath, rating)
		} else {
			tagsFailed = true
		}
	}

	pushed := false
	if plexRatingDue(rr.plex, cache, tmdbID, rating) {
		if err := fetchDetails(); err != nil {
			fmt.Printf("  [!] TMDB details failed – Plex rating not pushed: %v\n", err)
			failed = append(failed, "TMDB details")
		} else {
			var pushFailed string
			pushed, pushFailed = pushPlexRating(rr.plex, cache, tmdbID, title, year, rating)
			if pushFailed != "" {
				failed = append(failed, pushFailed)
			}
		}
	}

	tag, result := "[SKIP]   ", ResultSkipped
	if tagged || pushed {
		tag, result = "[UPDATED]", ResultUpdated
	}
	if tagsFailed || len(failed) > 0 {
		tag, result = "[ERR]    ", ResultError
	}
	fmt.Printf("%s %s", tag, basename)
	switch {
	case tagger == nil:
		fmt.Print(" (mkvpropedit/mkvextract not found; file not changed)")
	case tagsFailed:
		fmt.Print(": tags not written")
	case tagged:
		fmt.Printf(" (rating: %s)", rating)
	default:
		fmt.Printf(" (rating: %s, written earlier)", rating)
	}
	if pushed {
		fmt.Printf(" (Plex rating: %s)", rating)
	}
	printFailed(failed)
	return result
}

// plexRatingDue reports whether rating needs pushing to Plex: it is set and
// isn't the last rating pushed.
func plexRatingDue(plex PlexMovieClient, cache *MovieRatingCache, tmdbID string, rating NZRating) bool {
	if plex == nil || rating == RatingNone {
		return false
	}
	last, ok := cache.GetPlexRating(tmdbID)
	return !ok || last != rating
}

// pushPlexRating sets rating on the movie in Plex and records it. It reports
// whether it pushed, or names the step that failed. A movie Plex doesn't have
// is a warning, and nothing is recorded, so the next --ratings-only run retries.
func pushPlexRating(plex PlexMovieClient, cache *MovieRatingCache, tmdbID, title, year string, rating NZRating) (pushed bool, failed string) {
	rk, err := findPlexMovie(plex, tmdbID, title, year)
	if errors.Is(err, ErrPlexMovieNotFound) {
		fmt.Printf("  [!] Plex: %q not found (tried TMDB GUID, then title and year) – rating not pushed; rerun with --ratings-only once Plex has it\n", title)
		return false, ""
	}
	if err != nil {
		fmt.Printf("  [!] Plex lookup failed – rating not pushed: %v\n", err)
		return false, "Plex lookup"
	}
	if err := plex.SetContentRating(rk, rating); err != nil {
		fmt.Printf("  [!] Plex rating update failed: %v\n", err)
		return false, "Plex rating"
	}
	cache.SetPlexRating(tmdbID, rating)
	return true, ""
}
