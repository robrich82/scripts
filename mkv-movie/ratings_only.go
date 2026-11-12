package main

import (
	"errors"
	"fmt"
	"path/filepath"
)

// processRatingOnly writes the movie's NZ rating into filePath's existing tags,
// restoring its timestamps, and pushes the rating to Plex. The tags are left
// alone when the cache says they already hold it (unless --overwrite-ratings),
// and Plex when it was the last rating pushed. tagger is nil when MKVToolNix
// isn't installed: only Plex is updated then.
//
// TMDB details are fetched only when the rating isn't cached; otherwise the
// filename's title and year stand in for Plex's title lookup.
func processRatingOnly(filePath string, tmdb TMDBMovieClient, tagger MKVTagger, timer FileTimer, rr *MovieRatingResolver, cache *MovieRatingCache) FileResult {
	basename := filepath.Base(filePath)
	year := extractYear(basename)
	title := titleFromFilename(basename, year)
	if title == "" {
		fmt.Printf("[SKIP]    %s (cannot derive title from filename)\n", basename)
		return ResultSkipped
	}

	tmdbID, details, result, ok := lookupMovie(filePath, title, year, tmdb, rr, cache, func(id string) bool { return !rr.settled(id) })
	if !ok {
		return result
	}
	if details != nil {
		title, year = details.Title, releaseYear(details.ReleaseDate)
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

	pushed, pushFailed := pushPlexRating(rr.plex, cache, tmdbID, title, year, rating)
	if pushFailed != "" {
		failed = append(failed, pushFailed)
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
		fmt.Printf(" (rating: %s, already in tags)", rating)
	}
	if pushed {
		fmt.Printf(" (Plex rating: %s)", rating)
	}
	printFailed(failed)
	return result
}

// pushPlexRating sets rating on the movie in Plex, found by TMDB GUID or else
// by title and year, unless it was the last rating pushed. It reports whether
// it pushed, or names the step that failed. A movie Plex doesn't have is a
// warning, and nothing is recorded, so the next --ratings-only run retries it.
func pushPlexRating(plex PlexMovieClient, cache *MovieRatingCache, tmdbID, title, year string, rating NZRating) (pushed bool, failed string) {
	if plex == nil || rating == RatingNone {
		return false, ""
	}
	if last, ok := cache.GetPlexRating(tmdbID); ok && last == rating {
		return false, ""
	}
	rk, err := plex.FindMovieByTMDBID(tmdbID)
	if errors.Is(err, ErrPlexMovieNotFound) || (err == nil && rk == "") {
		rk, err = plex.FindMovieByTitle(title, year)
	}
	if errors.Is(err, ErrPlexMovieNotFound) || (err == nil && rk == "") {
		fmt.Println("  [!] Not in Plex library – rating not pushed; rerun with --ratings-only once Plex has it")
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
