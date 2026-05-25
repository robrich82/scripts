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

	applied, metaApplied := cache.CleanStalePaths()
	total := applied + metaApplied

	if total == 0 {
		fmt.Println("Cache is clean; nothing to remove.")
		return
	}

	if applied > 0 {
		fmt.Printf("Removed %d stale applied-rating entr%s.\n", applied, es(applied))
	}
	if metaApplied > 0 {
		fmt.Printf("Removed %d stale meta-applied entr%s.\n", metaApplied, es(metaApplied))
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
