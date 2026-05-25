package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runClean(mediaDir string) {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}

	total := 0

	// show_cache.json: remove entries for show folders that no longer exist.
	showCache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading show_cache.json: %v\n", err)
		os.Exit(1)
	}
	removedIDs := showCache.CleanStaleFolders(mediaDir)
	if n := len(removedIDs); n > 0 {
		fmt.Printf("show_cache.json:      removed %d stale entr%s\n", n, es(n))
		total += n
	}
	if err := showCache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving show_cache.json: %v\n", err)
	}

	// Build the set of TMDB IDs that remain after cleaning show_cache so that
	// dependent caches can be pruned to match.
	knownIDs := make(map[string]bool)
	for _, id := range showCache.AllEntries() {
		knownIDs[id] = true
	}

	// title_cache.json: remove entries for files that no longer exist.
	titleCache, err := LoadTitleCache(filepath.Join(cfgDir, "title_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading title_cache.json: %v\n", err)
		os.Exit(1)
	}
	if n := titleCache.CleanStalePaths(); n > 0 {
		fmt.Printf("title_cache.json:     removed %d stale entr%s\n", n, es(n))
		total += n
		if err := titleCache.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving title_cache.json: %v\n", err)
		}
	}

	// rating_cache.json: remove stale file paths and orphaned show IDs.
	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading rating_cache.json: %v\n", err)
		os.Exit(1)
	}
	nPaths := ratingCache.CleanStalePaths()
	nIDs := ratingCache.CleanStaleShowIDs(knownIDs)
	if n := nPaths + nIDs; n > 0 {
		fmt.Printf("rating_cache.json:    removed %d stale entr%s (%d file paths, %d show IDs)\n", n, es(n), nPaths, nIDs)
		total += n
		if err := ratingCache.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving rating_cache.json: %v\n", err)
		}
	}

	// pending_dates.json: remove entries for files that no longer exist.
	retryList, err := LoadRetryList(filepath.Join(cfgDir, "pending_dates.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading pending_dates.json: %v\n", err)
		os.Exit(1)
	}
	if n := retryList.CleanStalePaths(); n > 0 {
		fmt.Printf("pending_dates.json:   removed %d stale entr%s\n", n, es(n))
		total += n
		if err := retryList.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving pending_dates.json: %v\n", err)
		}
	}

	// missing_ignore.json: remove entries for shows no longer in show_cache.
	ignoreList, err := LoadMissingIgnoreList(filepath.Join(cfgDir, "missing_ignore.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading missing_ignore.json: %v\n", err)
		os.Exit(1)
	}
	if n := ignoreList.CleanStaleShowIDs(knownIDs); n > 0 {
		fmt.Printf("missing_ignore.json:  removed %d stale entr%s\n", n, es(n))
		total += n
		if err := ignoreList.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving missing_ignore.json: %v\n", err)
		}
	}

	if total == 0 {
		fmt.Println("All caches are clean; nothing to remove.")
		return
	}
	fmt.Printf("\nTotal: %d stale entr%s removed.\n", total, es(total))
}

func es(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
