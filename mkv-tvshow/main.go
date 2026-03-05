package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	cutoffDateStr := flag.String("cutoff-date", "", "Only process files modified on or after this date (YYYY-MM-DD). Defaults to today.")
	processAll := flag.Bool("all", false, "Process all cached shows without prompting.")
	ratingsOnly := flag.Bool("ratings-only", false, "Only update NZ content ratings; skip all other metadata processing.")
	overwriteRatings := flag.Bool("overwrite-ratings", false, "Re-fetch all ratings, ignoring cached values.")
	flag.Parse()

	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: TMDB_API_KEY environment variable not set")
		os.Exit(1)
	}

	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")

	var cutoff time.Time
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

	prompter := newStdinPrompter()

	shouldProcessAll := *processAll
	if !shouldProcessAll {
		ans, err := prompter.Ask("Process all cached shows without prompting? [y/n]: ")
		if err == nil && ans == "y" {
			shouldProcessAll = true
		}
	}

	mediaDir, err := os.Getwd()
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

	cache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading cache: %v\n", err)
		os.Exit(1)
	}

	manualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
		os.Exit(1)
	}

	retryList, err := LoadRetryList(filepath.Join(cfgDir, "pending_dates.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading pending_dates.json: %v\n", err)
		os.Exit(1)
	}

	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading rating_cache.json: %v\n", err)
		os.Exit(1)
	}

	cfg := Config{
		MediaDir:         mediaDir,
		CutoffDate:       cutoff,
		ProcessAll:       shouldProcessAll,
		RatingsOnly:      *ratingsOnly,
		OverwriteRatings: *overwriteRatings,
	}

	tmdb := newHTTPTMDBClient(apiKey, "https://api.themoviedb.org/3")
	mkv := newRealMKVRunner()
	timer := &win32FileTimer{}

	// tmdb also implements RatingSource; Classification Office is the second source.
	sources := []RatingSource{tmdb, newClassificationSource()}

	var plexClient PlexClient
	if plexURL != "" && plexToken != "" {
		plexClient = newHTTPPlexClient(plexURL, plexToken)
	}

	fmt.Printf("Scanning %s...\n", mediaDir)

	processor := NewProcessor(cfg, cache, manualData, tmdb, mkv, prompter, timer, retryList, ratingCache, sources, plexClient)
	stats := processor.Run()

	fmt.Printf("\nDone. Success: %d  Failed: %d  Skipped (old): %d\n",
		stats.Success, stats.Failed, stats.SkippedOld)
}
