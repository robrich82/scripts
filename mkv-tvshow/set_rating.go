package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// setRatingPlan is one show that --set-rating will update.
type setRatingPlan struct {
	// Folder is the show's directory name relative to the media directory.
	Folder string
	// TMDBID is the show's cached TMDB ID, used as the rating cache key.
	TMDBID string
	// Current is the rating already cached for the show, or RatingNone.
	Current NZRating
}

// showFoldersWithMKV returns the top-level show folder names under mediaDir
// that contain at least one .mkv file, using the same layout convention as
// Run (files directly under mediaDir have no show folder and are ignored).
func showFoldersWithMKV(mediaDir string) []string {
	seen := make(map[string]bool)
	filepath.WalkDir(mediaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		rel, err := filepath.Rel(mediaDir, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) < 2 {
			return nil
		}
		seen[parts[0]] = true
		return nil
	})

	folders := make([]string, 0, len(seen))
	for f := range seen {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	return folders
}

// planSetRating resolves every show folder under mediaDir against showCache.
// It returns the shows that can be updated and the folder names that have no
// cached TMDB ID (never processed, so there is nothing to key a rating on).
func planSetRating(mediaDir string, showCache *ShowCache, ratingCache *RatingCache) (plans []setRatingPlan, unknown []string) {
	for _, folder := range showFoldersWithMKV(mediaDir) {
		// Current entries are keyed by absolute path; legacy ones by base name.
		id, ok := showCache.Get(filepath.Join(mediaDir, folder))
		if !ok {
			id, ok = showCache.Get(folder)
		}
		if !ok {
			unknown = append(unknown, folder)
			continue
		}
		current, _ := ratingCache.GetShowRating(id)
		plans = append(plans, setRatingPlan{Folder: folder, TMDBID: id, Current: current})
	}
	return plans, unknown
}

// applySetRating writes r as the show-level rating for every planned show.
// Episode-level ratings are cleared first because they take priority over the
// show rating, and applied-state entries are reset so the next run rewrites
// the LAW_RATING tags. Returns the number of episode entries cleared and files
// reset. The cache is not saved — the caller does that.
func applySetRating(ratingCache *RatingCache, mediaDir string, plans []setRatingPlan, r NZRating) (episodesCleared, filesReset int) {
	for _, plan := range plans {
		episodesCleared += ratingCache.ResetForShowID(plan.TMDBID)
		ratingCache.SetShowRating(plan.TMDBID, r)
		filesReset += len(ratingCache.ResetAppliedForFolder(filepath.Join(mediaDir, plan.Folder)))
	}
	return episodesCleared, filesReset
}

// runSetRating applies one NZ rating to every show under mediaDir, replacing
// any cached value, then exits. It makes no TMDB, Plex, or network calls — the
// ratings are pushed on the next normal (or --ratings-only) run.
func runSetRating(ratingStr, mediaDir string, prompter Prompter) {
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

	showCache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading show_cache.json: %v\n", err)
		os.Exit(1)
	}
	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading rating_cache.json: %v\n", err)
		os.Exit(1)
	}

	plans, unknown := planSetRating(mediaDir, showCache, ratingCache)
	if len(plans) == 0 {
		fmt.Fprintf(os.Stderr, "No cached shows found under %s\n", mediaDir)
		if len(unknown) > 0 {
			fmt.Fprintf(os.Stderr, "%d show folder(s) have no cached TMDB ID — run mkv-tvshow normally first.\n", len(unknown))
		}
		os.Exit(1)
	}

	fmt.Printf("Setting NZ rating %s for %d show(s) in %s:\n", rating, len(plans), mediaDir)
	for _, plan := range plans {
		from := string(plan.Current)
		if plan.Current == RatingNone {
			from = "(none)"
		}
		fmt.Printf("  %-50s %-8s → %s\n", plan.Folder, from, rating)
	}
	for _, folder := range unknown {
		fmt.Printf("  [skip] %s (no cached TMDB ID)\n", folder)
	}

	ans, _ := prompter.Ask(fmt.Sprintf("\nApply %s to these %d show(s)? [y/N]: ", rating, len(plans)))
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Println("Aborted; no changes made.")
		return
	}

	episodesCleared, filesReset := applySetRating(ratingCache, mediaDir, plans, rating)
	if err := ratingCache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving rating_cache.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done. %d show(s) set to %s, %d episode override(s) cleared, %d file(s) reset.\n",
		len(plans), rating, episodesCleared, filesReset)
	fmt.Println("Run 'mkv-tvshow --ratings-only --all' to write the tags and push to Plex.")
}
