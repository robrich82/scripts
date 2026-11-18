package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// runClean removes cache entries for shows and files that no longer exist.
// Ratings and ignores are removed only for the shows whose folders it removes
// from show_cache.json and their manual_data season override IDs, or with
// allIDs for every show ID that neither show_cache.json nor an override has.
func runClean(allIDs bool) {
	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}
	path := func(file string) string { return filepath.Join(cfgDir, file) }

	total := 0
	failed := false
	// idsFailed is set when a cache pruned by show ID fails to save;
	// show_cache.json is then left as it was, so a rerun finds the same shows.
	idsFailed := false
	// saved saves a cache whose stale entries were removed in memory, and
	// reports whether the removal reached disk.
	saved := func(file string, c interface{ Save() error }) bool {
		if err := c.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving %s: %v\n%sNothing was removed from %s.\n",
				path(file), withoutPaths(err), hintLine(err), file)
			failed = true
			return false
		}
		return true
	}

	// show_cache.json: remove entries for show folders that no longer exist.
	// It is saved last, below.
	showCache, err := LoadCache(path("show_cache.json"))
	if err != nil {
		exitOnCacheLoadError(path("show_cache.json"), err)
	}
	// A manual_data.json season override keeps its ratings and ignores under
	// its own show ID.
	manualData, err := LoadManualData(path("manual_data.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\nNothing was removed.\n", err)
		os.Exit(1)
	}
	hadShows := len(showCache.AllEntries()) > 0
	removed := showCache.CleanStaleFolders()
	knownIDs := make(map[string]bool)
	kept := make(map[string]bool) // folder names still in show_cache.json
	for folder, id := range showCache.AllEntries() {
		knownIDs[id] = true
		kept[filepath.Base(folder)] = true
	}
	removedNames := make(map[string]bool)
	for folder := range removed {
		if name := filepath.Base(folder); !kept[name] {
			removedNames[name] = true
		}
	}
	// Override IDs stay known unless their show's folder is the one removed.
	for name, entry := range manualData {
		if removedNames[name] {
			continue
		}
		for _, id := range overrideIDs(entry) {
			knownIDs[id] = true
		}
	}
	// The shows whose folders are gone, with their season override IDs. A
	// show missing from show_cache.json for another reason (a rebuilt cache)
	// keeps its ratings and ignores.
	gone := make(map[string]bool)
	for folder, id := range removed {
		for _, id := range append([]string{id}, overrideIDs(manualData[filepath.Base(folder)])...) {
			if !knownIDs[id] { // another show still uses it
				gone[id] = true
			}
		}
	}

	// stale picks the show IDs whose ratings and ignores are removed; nil
	// removes none.
	var stale func(showID string) bool
	switch {
	case allIDs && len(kept) == 0:
		// Every show ID would look stale, so only the gone shows' are removed.
		if hadShows {
			fmt.Println("Every show folder in show_cache.json is gone, so --all-ids removes only those shows' IDs from rating_cache.json and missing_ignore.json.")
		} else {
			fmt.Println("show_cache.json is empty or missing, so --all-ids keeps the show IDs in rating_cache.json and missing_ignore.json.")
		}
		if len(gone) > 0 {
			stale = func(id string) bool { return gone[id] }
		}
	case allIDs:
		stale = func(id string) bool { return !knownIDs[id] }
	case len(gone) > 0:
		stale = func(id string) bool { return gone[id] }
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
	if stale != nil {
		nIDs = ratingCache.CleanStaleShowIDs(stale)
	}
	if n := nPaths + nIDs; n > 0 {
		if !saved("rating_cache.json", ratingCache) {
			idsFailed = nIDs > 0
		} else {
			fmt.Printf("rating_cache.json:    removed %d stale entr%s (%d file paths, %d show IDs)\n", n, es(n), nPaths, nIDs)
			total += n
		}
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
	if stale != nil {
		if n := ignoreList.CleanStaleShowIDs(stale); n > 0 {
			if !saved("missing_ignore.json", ignoreList) {
				idsFailed = true
			} else {
				fmt.Printf("missing_ignore.json:  removed %d stale entr%s\n", n, es(n))
				total += n
			}
		}
	}

	// show_cache.json goes last: while it still lists the gone folders, a
	// rerun finds the same shows and redoes a show ID removal that failed to
	// save. The file-path caches don't depend on it.
	if n := len(removed); n > 0 {
		switch {
		case idsFailed:
			fmt.Fprintf(os.Stderr, "show_cache.json was left unchanged, so running the same --clean again redoes the removals that failed.\n")
		case saved("show_cache.json", showCache):
			fmt.Printf("show_cache.json:      removed %d stale entr%s\n", n, es(n))
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

// overrideIDs returns the show IDs entry's season overrides point at.
func overrideIDs(entry ManualEntry) []string {
	var ids []string
	for _, o := range entry.Overrides {
		if o.ID != "" {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func es(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
