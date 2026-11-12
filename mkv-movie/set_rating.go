package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// setRatingPlan is one movie that --set-rating will update.
type setRatingPlan struct {
	// TMDBID is the movie's cached TMDB ID, used as the rating cache key.
	TMDBID string
	// Display is the "title:YEAR" key the ID was resolved from, shown to the user.
	Display string
	// Current is the rating already cached for the movie, or RatingNone.
	Current NZRating
}

// planSetRating walks dir for .mkv files and resolves each one to its cached
// TMDB ID. It returns the movies that can be updated (deduplicated by ID, in
// display order) and the filenames whose TMDB ID is not cached — those have
// never been processed, so there is no key to store a rating under.
func planSetRating(dir string, cache *MovieRatingCache) (plans []setRatingPlan, unknown []string) {
	byID := make(map[string]bool)
	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("[!] Skipping unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		basename := d.Name()
		if !strings.HasSuffix(strings.ToLower(basename), ".mkv") {
			return nil
		}
		year := extractYear(basename)
		title := titleFromFilename(basename, year)
		if title == "" {
			unknown = append(unknown, basename)
			return nil
		}
		cacheKey := title + ":" + year
		tmdbID := cache.GetTMDBID(cacheKey)
		if tmdbID == "" {
			unknown = append(unknown, basename)
			return nil
		}
		// Record the mapping so --clean keeps this override; saved only if the user confirms.
		cache.SetFileTMDBID(path, tmdbID)
		if byID[tmdbID] {
			return nil
		}
		byID[tmdbID] = true
		current, _ := cache.Get(tmdbID)
		plans = append(plans, setRatingPlan{TMDBID: tmdbID, Display: cacheKey, Current: current})
		return nil
	})

	sort.Slice(plans, func(i, j int) bool { return plans[i].Display < plans[j].Display })
	sort.Strings(unknown)
	return plans, unknown
}

// applySetRating writes r as the cached rating for every planned movie, and
// clears the Plex-pushed rating so the next run that reaches the movie's files
// re-pushes it. Returns the number of files mapped to those movies. The cache
// is not saved — the caller does that.
func applySetRating(cache *MovieRatingCache, plans []setRatingPlan, r NZRating) int {
	filesReset := 0
	for _, plan := range plans {
		filesReset += len(cache.ResetForTMDBID(plan.TMDBID))
		cache.Set(plan.TMDBID, r)
	}
	return filesReset
}

// runSetRating applies one NZ rating to every movie under dir, replacing any
// cached value, then exits. It makes no TMDB, Plex, or network calls — the
// ratings are written and pushed on the next normal run.
func runSetRating(ratingStr, dir string, prompter func(string) (string, error)) {
	rating, ok := ParseNZRating(ratingStr)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: %q is not a valid NZ rating. Expected one of: %s\n", ratingStr, validRatings)
		os.Exit(1)
	}

	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	cache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(err)
	}

	plans, unknown := planSetRating(dir, cache)
	if len(plans) == 0 {
		fmt.Fprintf(os.Stderr, "No cached movies found under %s\n", dir)
		if len(unknown) > 0 {
			fmt.Fprintf(os.Stderr, "%d file(s) have no cached TMDB ID — run mkv-movie normally first.\n", len(unknown))
		}
		os.Exit(1)
	}

	fmt.Printf("Setting NZ rating %s for %d movie(s) in %s:\n", rating, len(plans), dir)
	for _, plan := range plans {
		from := string(plan.Current)
		if plan.Current == RatingNone {
			from = "(none)"
		}
		fmt.Printf("  %-50s %-8s → %s\n", plan.Display, from, rating)
	}
	for _, name := range unknown {
		fmt.Printf("  [skip] %s (no cached TMDB ID)\n", name)
	}

	ans, _ := prompter(fmt.Sprintf("\nApply %s to these %d movie(s)? [y/N]: ", rating, len(plans)))
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Println("Aborted; no changes made.")
		return
	}

	filesReset := applySetRating(cache, plans, rating)
	if err := cache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving cache: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done. %d movie(s) (%d file(s)) set to %s.\n", len(plans), filesReset, rating)
	fmt.Println("Run 'mkv-movie --all' in this folder to write the tags and push to Plex.")
}
