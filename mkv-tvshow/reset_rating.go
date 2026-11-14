package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runResetRating(query, mediaDir string) {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	showCache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		exitOnCacheLoadError(filepath.Join(cfgDir, "show_cache.json"), err)
	}

	matches := showCache.FindByQuery(query)
	switch len(matches) {
	case 0:
		fmt.Fprintf(os.Stderr, "No cached show found matching %q\n", query)
		os.Exit(1)
	case 1:
		// unambiguous
	default:
		fmt.Fprintf(os.Stderr, "Ambiguous query %q — %d matches:\n", query, len(matches))
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  TMDB ID %-10s  %s\n", m[0], filepath.Base(m[1]))
		}
		os.Exit(1)
	}

	tmdbID, folderKey := matches[0][0], matches[0][1]
	var folderPath string
	if filepath.IsAbs(folderKey) {
		folderPath = folderKey
	} else {
		folderPath = filepath.Join(mediaDir, folderKey)
	}

	fmt.Printf("Resetting rating for %q (TMDB ID: %s)...\n", filepath.Base(folderKey), tmdbID)

	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(filepath.Join(cfgDir, "rating_cache.json"), err)
	}

	epCleared := ratingCache.ResetForShowID(tmdbID)
	paths := ratingCache.ResetAppliedForFolder(folderPath)
	if err := ratingCache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving %s: %v\n%sNothing was reset.\n", ratingCache.path, withoutPaths(err), hintLine(err))
		os.Exit(1)
	}
	for _, p := range paths {
		fmt.Printf("  Reset: %s\n", filepath.Base(p))
	}
	fmt.Printf("Done. Rating cleared, %d episode rating(s) removed, %d file(s) reset.\n", epCleared, len(paths))
}
