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
	path := func(file string) string { return filepath.Join(cfgDir, file) }

	total := 0
	failed := false
	// saved saves a cache whose stale entries were removed in memory, and
	// reports whether the removal reached disk.
	saved := func(file string, c interface{ Save() error }) bool {
		if err := c.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving %s: %v\n%sNothing was removed from %s.\n", path(file), err, hintLine(err), file)
			failed = true
			return false
		}
		return true
	}

	// show_cache.json: remove entries for show folders that no longer exist.
	showCache, err := LoadCache(path("show_cache.json"))
	if err != nil {
		exitOnCacheLoadError(path("show_cache.json"), err)
	}
	// With no shows to compare against, every show ID in the other caches
	// would look stale, so prune them only when the show cache has entries.
	pruneIDs := len(showCache.AllEntries()) > 0
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
	if !pruneIDs {
		fmt.Println("show_cache.json is empty or missing, so show IDs in rating_cache.json and missing_ignore.json are kept.")
	}

	// Build the set of TMDB IDs that remain after cleaning show_cache so that
	// dependent caches can be pruned to match.
	knownIDs := make(map[string]bool)
	for _, id := range showCache.AllEntries() {
		knownIDs[id] = true
	}

	// title_cache.json: remove entries for files that no longer exist.
	titleCache, err := LoadTitleCache(path("title_cache.json"))
	if err != nil {
		exitOnCacheLoadError(path("title_cache.json"), err)
	}
	if n := titleCache.CleanStalePaths(); n > 0 && saved("title_cache.json", titleCache) {
		fmt.Printf("title_cache.json:     removed %d stale entr%s\n", n, es(n))
		total += n
	}

	// rating_cache.json: remove stale file paths and orphaned show IDs.
	ratingCache, err := LoadRatingCache(path("rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(path("rating_cache.json"), err)
	}
	nPaths := ratingCache.CleanStalePaths()
	nIDs := 0
	if pruneIDs {
		nIDs = ratingCache.CleanStaleShowIDs(knownIDs)
	}
	if n := nPaths + nIDs; n > 0 && saved("rating_cache.json", ratingCache) {
		fmt.Printf("rating_cache.json:    removed %d stale entr%s (%d file paths, %d show IDs)\n", n, es(n), nPaths, nIDs)
		total += n
	}

	// pending_dates.json: remove entries for files that no longer exist.
	retryList, err := LoadRetryList(path("pending_dates.json"))
	if err != nil {
		exitOnCacheLoadError(path("pending_dates.json"), err)
	}
	if n := retryList.CleanStalePaths(); n > 0 && saved("pending_dates.json", retryList) {
		fmt.Printf("pending_dates.json:   removed %d stale entr%s\n", n, es(n))
		total += n
	}

	// missing_ignore.json: remove entries for shows no longer in show_cache.
	ignoreList, err := LoadMissingIgnoreList(path("missing_ignore.json"))
	if err != nil {
		exitOnCacheLoadError(path("missing_ignore.json"), err)
	}
	if pruneIDs {
		if n := ignoreList.CleanStaleShowIDs(knownIDs); n > 0 && saved("missing_ignore.json", ignoreList) {
			fmt.Printf("missing_ignore.json:  removed %d stale entr%s\n", n, es(n))
			total += n
		}
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
