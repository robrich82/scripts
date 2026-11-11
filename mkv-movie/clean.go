package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runClean() {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	cache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading movie_rating_cache.json: %v\n%s\n", err, cacheRecoveryHint)
		os.Exit(1)
	}

	files, movies := cache.CleanStalePaths()
	if len(files) == 0 && len(movies) == 0 {
		fmt.Println("Cache is clean; nothing to remove.")
		return
	}

	if n := len(files); n > 0 {
		fmt.Printf("Removed %d file%s no longer on disk.\n", n, plural(n))
	}
	if n := len(movies); n > 0 {
		fmt.Printf("Removed the cached rating, TMDB ID and Plex state of %d movie%s no file on disk maps to:\n", n, plural(n))
		for _, m := range movies {
			fmt.Printf("  %s\n", m)
		}
	}

	if err := cache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving cache: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Cache updated.")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
