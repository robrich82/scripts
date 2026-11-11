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
		fmt.Fprintf(os.Stderr, "Error loading rating cache: %v\n", err)
		os.Exit(1)
	}

	paths, movies := cache.CleanStalePaths()
	total := paths + movies

	if total == 0 {
		fmt.Println("Cache is clean; nothing to remove.")
		return
	}

	if paths > 0 {
		fmt.Printf("Removed %d stale file entr%s.\n", paths, es(paths))
	}
	if movies > 0 {
		fmt.Printf("Removed %d entr%s for movies no longer on disk.\n", movies, es(movies))
	}

	if err := cache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving cache: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Cache updated (%d total entr%s removed).\n", total, es(total))
}

func es(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
