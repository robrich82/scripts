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

	total := 0
	failed := false
	// saved saves a cache whose stale entries were removed in memory, and
	// reports whether the removal reached disk.
	saved := func(file string, c interface{ Save() error }) bool {
		if err := c.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving %s: %v\nNothing was removed from %s.\n", file, err, file)
			failed = true
			return false
		}
		return true
	}

	// show_cache.json: remove entries for show folders that no longer exist.
	showCache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		exitOnCacheLoadError("show_cache.json", err)
	}
	removedIDs := showCache.CleanStaleFolders()
	if n := len(removedIDs); n > 0 {
		// The other caches are pruned against the cleaned show list, so stop
		// if it can't be saved.
		if !saved("show_cache.json", showCache) {
			os.Exit(1)
		}
		fmt.Printf("show_cache.json:      removed %d stale entr%s\n", n, es(n))
		total += n
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
		exitOnCacheLoadError("title_cache.json", err)
	}
	if n := titleCache.CleanStalePaths(); n > 0 && saved("title_cache.json", titleCache) {
		fmt.Printf("title_cache.json:     removed %d stale entr%s\n", n, es(n))
		total += n
	}

	// rating_cache.json: remove stale file paths and orphaned show IDs.
	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError("rating_cache.json", err)
	}
	nPaths := ratingCache.CleanStalePaths()
	nIDs := ratingCache.CleanStaleShowIDs(knownIDs)
	if n := nPaths + nIDs; n > 0 && saved("rating_cache.json", ratingCache) {
		fmt.Printf("rating_cache.json:    removed %d stale entr%s (%d file paths, %d show IDs)\n", n, es(n), nPaths, nIDs)
		total += n
	}

	// pending_dates.json: remove entries for files that no longer exist.
	retryList, err := LoadRetryList(filepath.Join(cfgDir, "pending_dates.json"))
	if err != nil {
		exitOnCacheLoadError("pending_dates.json", err)
	}
	if n := retryList.CleanStalePaths(); n > 0 && saved("pending_dates.json", retryList) {
		fmt.Printf("pending_dates.json:   removed %d stale entr%s\n", n, es(n))
		total += n
	}

	// missing_ignore.json: remove entries for shows no longer in show_cache.
	ignoreList, err := LoadMissingIgnoreList(filepath.Join(cfgDir, "missing_ignore.json"))
	if err != nil {
		exitOnCacheLoadError("missing_ignore.json", err)
	}
	if n := ignoreList.CleanStaleShowIDs(knownIDs); n > 0 && saved("missing_ignore.json", ignoreList) {
		fmt.Printf("missing_ignore.json:  removed %d stale entr%s\n", n, es(n))
		total += n
	}

	switch {
	case total > 0:
		fmt.Printf("\nTotal: %d stale entr%s removed.\n", total, es(total))
	case !failed:
		fmt.Println("All caches are clean; nothing to remove.")
	}
	if failed {
		os.Exit(1)
	}
}

func es(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
