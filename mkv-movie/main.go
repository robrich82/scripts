package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	overwriteRatings := flag.Bool("overwrite-ratings", false, "Re-fetch all ratings, ignoring cached values.")
	overwriteStreams := flag.Bool("overwrite-streams", false, "Re-select Plex audio/subtitle streams even if already applied for a movie.")
	force := flag.Bool("force", false, "Re-apply all metadata even if already cached.")
	cutoffDateStr := flag.String("cutoff-date", "", "Only process files modified on or after this date (YYYY-MM-DD). Defaults to today.")
	allFlag := flag.Bool("all", false, "Process all files regardless of modification date.")
	cleanFlag := flag.Bool("clean", false, "Remove cache entries for files that no longer exist on disk, then exit.")
	debandOnlyMode := flag.Bool("deband-only", false, "Update only the DEBAND tag for files configured in manual_data.json; preserves all other tags and timestamps.")
	resetRatingQuery := flag.String("reset-rating", "", "Clear the cached rating for a movie and reset its applied state (title substring or TMDB ID), then exit.")
	flag.Parse()

	if *cleanFlag {
		runClean()
		return
	}

	if *resetRatingQuery != "" {
		runResetRating(*resetRatingQuery)
		return
	}

	var cutoff time.Time
	if !*allFlag {
		if *cutoffDateStr != "" {
			var err error
			cutoff, err = time.Parse("2006-01-02", *cutoffDateStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --cutoff-date %q, expected YYYY-MM-DD\n", *cutoffDateStr)
				os.Exit(1)
			}
		} else {
			now := time.Now()
			cutoff = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		}
	}

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	cfgDir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config dir: %v\n", err)
		os.Exit(1)
	}

	manualData, err := LoadMovieManualData(filepath.Join(cfgDir, "manual_data.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
		os.Exit(1)
	}

	timer := &win32FileTimer{}

	// --deband-only mode: update the DEBAND tag for configured files only.
	if *debandOnlyMode {
		tagger := newRealMKVTagger()
		fmt.Printf("Updating DEBAND tags in %s...\n", dir)
		stats := runDebandOnly(dir, manualData, tagger, timer)
		fmt.Printf("\nDone. Updated: %d  Failed: %d\n", stats.Updated, stats.Errors)
		return
	}

	tmdbKey := os.Getenv("TMDB_API_KEY")
	if tmdbKey == "" {
		fmt.Fprintln(os.Stderr, "Error: TMDB_API_KEY environment variable is not set.")
		os.Exit(1)
	}
	tmdb := newTMDBMovieSource(tmdbKey)

	applier, err := newRealMKVApplier()
	if err != nil {
		// mkvpropedit is optional — metadata will not be written.
		fmt.Fprintln(os.Stderr, "Warning: 'mkvpropedit' not found. MKV metadata will not be updated.")
		applier = nil
	}

	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")

	cache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load rating cache: %v\n", err)
		cache, _ = LoadMovieRatingCache("/dev/null") // empty fallback
	}

	var plex PlexMovieClient
	if plexURL != "" && plexToken != "" {
		plex = newHTTPPlexMovieClient(plexURL, plexToken)
	}

	sources := []RatingSource{
		tmdb,
		newClassificationSource(),
	}
	rr := newMovieRatingResolver(sources, cache, *overwriteRatings, *overwriteStreams, plex, stdinPrompter)

	if plex != nil {
		if err := plex.RefreshLibraries(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("Plex libraries refreshed.")
		}
	}

	if cutoff.IsZero() {
		fmt.Println("--- Cutoff date: none (all files) ---")
	} else {
		fmt.Printf("--- Cutoff date: %s ---\n", cutoff.Format("2006-01-02"))
	}
	fmt.Printf("Scanning %s...\n", dir)
	stats := walkAndProcess(dir, tmdb, applier, timer, rr, cache, manualData, *force, cutoff)

	fmt.Printf("\nDone. Updated: %d  OK: %d  Skipped: %d  Skipped (old): %d  Errors: %d\n",
		stats.Updated, stats.OK, stats.Skipped, stats.SkippedOld, stats.Errors)
}
