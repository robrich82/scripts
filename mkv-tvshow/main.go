package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	allIDsFlag := flag.Bool("all-ids", false, "With --clean, also remove ratings and ignores for every show ID not in show_cache.json, not only those of show folders this --clean removes. This includes the season override IDs of shows that still exist.")
	resetRatingQuery := flag.String("reset-rating", "", "Clear the cached rating for a show and reset its applied state (folder substring or TMDB ID), then exit.")
	setRating := flag.String("set-rating", "", "Set this NZ rating for every show in the current directory, overriding any cached value, then exit.")
	flag.Parse()

	if names := conflictingModes([]oneShotMode{
		{"--clean", *cleanFlag}, {"--reset-rating", *resetRatingQuery != ""}, {"--set-rating", *setRating != ""},
		{"--deband-only", *debandOnlyMode}, {"--missing", *missingMode}, {"--language-only", *languageOnly},
		{"--title-check", *titleCheckMode}, {"--ratings-only", *ratingsOnly},
	}); names != nil {
		fmt.Fprintf(os.Stderr, "Error: %s can't be combined; run them one at a time.\n", joinNames(names))
		os.Exit(1)
	}

	if *allIDsFlag && !*cleanFlag {
		fmt.Fprintln(os.Stderr, "Error: --all-ids only works with --clean.")
		os.Exit(1)
	}

	if *cleanFlag {
		runClean(*allIDsFlag)
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

	mkv := newRealMKVRunner()
	timer := &win32FileTimer{}

	// --deband-only mode: update the DEBAND tag for configured files only. No TMDB calls.
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

	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: TMDB_API_KEY environment variable not set")
		os.Exit(1)
	}

	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")
	if *languageOnly && (plexURL == "" || plexToken == "") {
		fmt.Fprintln(os.Stderr, "Error: --language-only sets the audio language in Plex: set PLEX_URL and PLEX_TOKEN.")
		os.Exit(1)
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

	prompter := newStdinPrompter()

	cache, err := LoadCache(filepath.Join(cfgDir, "show_cache.json"))
	if err != nil {
		exitOnCacheLoadError(filepath.Join(cfgDir, "show_cache.json"), err)
	}

	tmdb := newHTTPTMDBClient(apiKey, "https://api.themoviedb.org/3")

	// --missing mode: check for aired episodes not present locally.
	if *missingMode {
		ignoreList, err := LoadMissingIgnoreList(filepath.Join(cfgDir, "missing_ignore.json"))
		if err != nil {
			exitOnCacheLoadError(filepath.Join(cfgDir, "missing_ignore.json"), err)
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
		exitIfSavesLost(checker.LostSaves)
		return
	}

	titleCache, err := LoadTitleCache(filepath.Join(cfgDir, "title_cache.json"))
	if err != nil {
		exitOnCacheLoadError(filepath.Join(cfgDir, "title_cache.json"), err)
	}

	// --language-only mode: apply Plex audio language preference for all cached shows.
	if *languageOnly {
		ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
		if err != nil {
			exitOnCacheLoadError(filepath.Join(cfgDir, "rating_cache.json"), err)
		}
		langManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		plexClient := newMultiPlexClient(newHTTPPlexClient(plexURL, plexToken), altPlexClients(plexURL)...)
		cfg := Config{OverwriteLanguage: *overwriteLanguage}
		processor := NewProcessor(cfg, cache, langManualData, tmdb, nil, nil, nil, nil, ratingCache, nil, plexClient, nil)
		fmt.Println("Setting the Plex audio language for every cached show...")
		stats := processor.RunLanguageOnly()
		fmt.Printf("\nDone. Updated: %d  Skipped: %d  Errors: %d\n", stats.Updated, stats.Skipped, stats.Errors)
		exitIfSavesLost(stats.LostSaves)
		return
	}

	// --title-check mode: compare filename titles against TMDB and offer to rename.
	if *titleCheckMode {
		titleManualData, err := LoadManualData(filepath.Join(cfgDir, "manual_data.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manual_data.json: %v\n", err)
			os.Exit(1)
		}
		retryList, err := LoadRetryList(filepath.Join(cfgDir, "pending_dates.json"))
		if err != nil {
			exitOnCacheLoadError(filepath.Join(cfgDir, "pending_dates.json"), err)
		}
		cfg := Config{MediaDir: mediaDir}
		checker := NewTitleChecker(cfg, cache, titleManualData, tmdb, prompter, mkv, titleCache, timer, retryList)
		fmt.Printf("Checking episode titles in %s...\n", mediaDir)
		checker.Run()
		exitIfSavesLost(checker.LostSaves)
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
		exitOnCacheLoadError(filepath.Join(cfgDir, "pending_dates.json"), err)
	}

	ratingCache, err := LoadRatingCache(filepath.Join(cfgDir, "rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(filepath.Join(cfgDir, "rating_cache.json"), err)
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

	fmt.Printf("\nDone. Success: %d  Failed: %d  Skipped (old): %d  Plex errors: %d\n",
		stats.Success, stats.Failed, stats.SkippedOld, stats.PlexErrors)
	exitIfSavesLost(stats.LostSaves)
}

// oneShotMode is a flag that picks what a run does instead of the main pass.
type oneShotMode struct {
	name string
	set  bool
}

// conflictingModes returns the names of the set modes, in the order given,
// when more than one is set; nil otherwise.
func conflictingModes(modes []oneShotMode) []string {
	var set []string
	for _, m := range modes {
		if m.set {
			set = append(set, m.name)
		}
	}
	if len(set) < 2 {
		return nil
	}
	return set
}

// joinNames joins names as "a and b" or "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
