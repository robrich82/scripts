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
	allFlag := flag.Bool("all", false, "Process all files regardless of modification date.")
	promptEach := flag.Bool("prompt-each", false, "Prompt before processing each cached show.")
	ratingsOnly := flag.Bool("ratings-only", false, "Only update NZ content ratings; skip all other metadata processing.")
	overwriteRatings := flag.Bool("overwrite-ratings", false, "Re-fetch all ratings, ignoring cached values.")
	overwriteLanguage := flag.Bool("overwrite-language", false, "Re-apply Plex audio language preference even if already set for a show.")
	languageOnly := flag.Bool("language-only", false, "Apply Plex audio language preference for all cached shows without scanning files.")
	missingMode := flag.Bool("missing", false, "Check for episodes that have aired on TMDB but are not present locally.")
	titleCheckMode := flag.Bool("title-check", false, "Check episode titles in filenames against TMDB and offer to rename.")
	debandOnlyMode := flag.Bool("deband-only", false, "Update only the DEBAND tag for files configured in manual_data.json; preserves all other tags and timestamps.")
	cleanFlag := flag.Bool("clean", false, "Remove stale cache entries for shows and files that no longer exist on disk, then exit.")
	resetRatingQuery := flag.String("reset-rating", "", "Clear the cached rating for a show and reset its applied state (folder substring or TMDB ID), then exit.")
	setRating := flag.String("set-rating", "", "Set this NZ rating for every show in the current directory, overriding any cached value, then exit.")
	flag.Parse()

	if *cleanFlag {
		mediaDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		runClean(mediaDir)
		return
	}

	if *resetRatingQuery != "" {
		mediaDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		runResetRating(*resetRatingQuery, mediaDir)
		return
	}

	if *setRating != "" {
		mediaDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		runSetRating(*setRating, mediaDir, newStdinPrompter())
		return
	}

	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: TMDB_API_KEY environment variable not set")
		os.Exit(1)
	}

	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")

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

	prompter := newStdinPrompter()

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

	tmdb := newHTTPTMDBClient(apiKey, "https://api.themoviedb.org/3")

	// --missing mode: check for aired episodes not present locally.
	if *missingMode {
		ignoreList, err := LoadMissingIgnoreList(filepath.Join(cfgDir, "missing_ignore.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading missing_ignore.json: %v\n", err)
			os.Exit(1)
		}
		missingManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		cfg := Config{MediaDir: mediaDir}
		checker := NewMissingChecker(cfg, cache, missingManualData, tmdb, prompter, ignoreList)
		fmt.Printf("Checking for missing episodes in %s...\n", mediaDir)
		if err := checker.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	mkv := newRealMKVRunner()
	timer := &win32FileTimer{}

	titleCache, err := LoadTitleCache(filepath.Join(cfgDir, "title_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading title_cache.json: %v\n", err)
		os.Exit(1)
	}

	// --language-only mode: apply Plex audio language preference for all cached shows.
	if *languageOnly {
		ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading rating_cache.json: %v\n", err)
			os.Exit(1)
		}
		langManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		var plexClient PlexClient
		if plexURL != "" && plexToken != "" {
			plexClient = newMultiPlexClient(newHTTPPlexClient(plexURL, plexToken), altPlexClients(plexURL)...)
		}
		cfg := Config{OverwriteLanguage: *overwriteLanguage}
		processor := NewProcessor(cfg, cache, langManualData, tmdb, nil, nil, nil, nil, ratingCache, nil, plexClient, nil)
		processor.RunLanguageOnly()
		return
	}

	// --deband-only mode: update the DEBAND tag for configured files only.
	if *debandOnlyMode {
		debandManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		cfg := Config{MediaDir: mediaDir}
		processor := NewProcessor(cfg, nil, debandManualData, nil, mkv, nil, timer, nil, nil, nil, nil, nil)
		fmt.Printf("Updating DEBAND tags in %s...\n", mediaDir)
		stats := processor.RunDebandOnly()
		fmt.Printf("\nDone. Updated: %d  Failed: %d\n", stats.Success, stats.Failed)
		return
	}

	// --title-check mode: compare filename titles against TMDB and offer to rename.
	if *titleCheckMode {
		titleManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		cfg := Config{MediaDir: mediaDir}
		checker := NewTitleChecker(cfg, cache, titleManualData, tmdb, prompter, mkv, titleCache, timer)
		fmt.Printf("Checking episode titles in %s...\n", mediaDir)
		if err := checker.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Normal tagging mode.
	shouldProcessAll := !*promptEach

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
		MediaDir:          mediaDir,
		CutoffDate:        cutoff,
		ProcessAll:        shouldProcessAll,
		RatingsOnly:       *ratingsOnly,
		OverwriteRatings:  *overwriteRatings,
		OverwriteLanguage: *overwriteLanguage,
	}

	// tmdb also implements RatingSource; Classification Office is the second source.
	sources := []RatingSource{tmdb, newClassificationSource()}

	var plexClient PlexClient
	if plexURL != "" && plexToken != "" {
		plexClient = newMultiPlexClient(newHTTPPlexClient(plexURL, plexToken), altPlexClients(plexURL)...)
	}

	if plexClient != nil {
		if err := plexClient.RefreshLibraries(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("Plex libraries refreshed.")
		}
	}

	fmt.Printf("Scanning %s...\n", mediaDir)

	processor := NewProcessor(cfg, cache, manualData, tmdb, mkv, prompter, timer, retryList, ratingCache, sources, plexClient, titleCache)
	stats := processor.Run()

	fmt.Printf("\nDone. Success: %d  Failed: %d  Skipped (old): %d\n",
		stats.Success, stats.Failed, stats.SkippedOld)
}
