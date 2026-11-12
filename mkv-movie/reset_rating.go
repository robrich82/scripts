package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runResetRating(query string) {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	cache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(err)
	}

	matches := cache.FindTMDBIDByQuery(query)
	switch len(matches) {
	case 0:
		fmt.Fprintf(os.Stderr, "No cached movie found matching %q\n", query)
		os.Exit(1)
	case 1:
		// unambiguous
	default:
		fmt.Fprintf(os.Stderr, "Ambiguous query %q — %d matches:\n", query, len(matches))
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  TMDB ID %-10s  %s\n", m[0], m[1])
		}
		os.Exit(1)
	}

	tmdbID, displayKey := matches[0][0], matches[0][1]
	fmt.Printf("Resetting rating for %q (TMDB ID: %s)...\n", displayKey, tmdbID)

	paths := cache.ResetForTMDBID(tmdbID)
	for _, p := range paths {
		fmt.Printf("  File: %s\n", p)
	}

	if err := cache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving cache: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Done. Rating cleared. Run 'mkv-movie --all' in the movie's folder to fetch it again, retag the file and push it to Plex.")
}
