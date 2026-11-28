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
	overwriteRatings := flag.Bool("overwrite-ratings", false, "Re-fetch all ratings, ignoring cached values.")
	overwriteStreams := flag.Bool("overwrite-streams", false, "Re-select Plex audio/subtitle streams even if already applied for a movie.")
	cutoffDateStr := flag.String("cutoff-date", "", "Only process files modified on or after this date (YYYY-MM-DD). Defaults to today.")
	allFlag := flag.Bool("all", false, "Process all files regardless of modification date.")
	ratingsOnly := flag.Bool("ratings-only", false, "Only write NZ content ratings to every file's tags (whatever its age) and to Plex; titles, dates, posters and streams are left alone.")
	languageOnly := flag.Bool("language-only", false, "Only set Plex audio/subtitle streams, for every movie the cache has a file for, without reading the files; honours --overwrite-streams.")
	cleanFlag := flag.Bool("clean", false, "Remove cache entries for files that no longer exist on disk, and the cached ratings, TMDB IDs and Plex state of movies no remaining file maps to, then exit.")
	debandOnlyMode := flag.Bool("deband-only", false, "Update only the DEBAND tag for files configured in manual_data.json; preserves all other tags and timestamps.")
	resetRatingQuery := flag.String("reset-rating", "", "Clear the cached and Plex-pushed rating for a movie (title substring or TMDB ID), then exit. Run with --ratings-only in its folder afterwards to retag it.")
	setRating := flag.String("set-rating", "", "Set this NZ rating for every movie in the current directory, overriding any cached value, then exit.")
	var clearHistoryMovies stringList
	flag.Var(&clearHistoryMovies, "clear-history", `Delete a movie's Plex watch history, and on plex.tv too when the account is the token owner's, given as "Title" or "Title (Year)"; repeat for more movies. Lists the entries and asks first, then exits.`)
	historyAccount := flag.String("account", "", "With --clear-history, the Plex server account whose history to clear, by name. Defaults to the PLEX_TOKEN owner.")
	flag.Parse()

	if names := conflictingModes([]oneShotMode{
		{"--clean", *cleanFlag}, {"--reset-rating", *resetRatingQuery != ""}, {"--set-rating", *setRating != ""},
		{"--deband-only", *debandOnlyMode}, {"--ratings-only", *ratingsOnly}, {"--language-only", *languageOnly},
		{"--clear-history", len(clearHistoryMovies) > 0},
	}); names != nil {
		fmt.Fprintf(os.Stderr, "Error: %s can't be combined; run them one at a time.\n", joinNames(names))
		os.Exit(1)
	}
	if *historyAccount != "" && len(clearHistoryMovies) == 0 {
		fmt.Fprintln(os.Stderr, "Error: --account only works with --clear-history.")
		os.Exit(1)
	}

	if len(clearHistoryMovies) > 0 {
		// flag stops at the first stray argument, which would drop a later --account.
		if flag.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "Error: unexpected argument %q: give each title its own --clear-history, quoted if it has spaces or a year, e.g. --clear-history \"Heat (1995)\" --clear-history Alien.\n", flag.Arg(0))
			os.Exit(1)
		}
		runClearHistory(clearHistoryMovies, *historyAccount)
		return
	}

	if *cleanFlag {
		runClean()
		return
	}

	if *resetRatingQuery != "" {
		runResetRating(*resetRatingQuery)
		return
	}

	if *setRating != "" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		runSetRating(*setRating, dir, stdinPrompter)
		return
	}

	var cutoff time.Time
	if !*allFlag && !*languageOnly {
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

	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")

	cache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		exitOnCacheLoadError(err)
	}

	var plex PlexMovieClient
	if plexURL != "" && plexToken != "" {
		plex = newMultiPlexMovieClient(newHTTPPlexMovieClient(plexURL, plexToken), altPlexMovieClients(plexURL)...)
	}

	sources := []RatingSource{
		tmdb,
		newClassificationSource(),
	}
	rr := newMovieRatingResolver(sources, cache, *overwriteRatings, *overwriteStreams, plex, stdinPrompter)

	// Before the library refresh: no files are read, so there is nothing new to scan.
	if *languageOnly {
		if plex == nil {
			fmt.Fprintln(os.Stderr, "Error: --language-only sets streams in Plex: set PLEX_URL and PLEX_TOKEN.")
			os.Exit(1)
		}
		fmt.Println("Setting Plex streams for every cached movie...")
		stats := walkLanguageOnly(tmdb, plex, cache, manualData, *overwriteStreams)
		fmt.Printf("\nDone. Updated: %d  Skipped: %d  Errors: %d\n", stats.Updated, stats.Skipped, stats.Errors)
		return
	}

	if plex != nil {
		if err := plex.RefreshLibraries(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("Plex libraries refreshed.")
		}
	}

	if *ratingsOnly {
		// A nil *realMKVTagger stored in the interface would compare non-nil.
		var tagger MKVTagger
		if t := newRealMKVTagger(); t.Available() {
			tagger = t
		} else if plex == nil {
			fmt.Fprintln(os.Stderr, "Error: mkvpropedit or mkvextract not found in PATH, and Plex isn't configured: --ratings-only has nothing to write.")
			os.Exit(1)
		} else {
			fmt.Fprintln(os.Stderr, "Warning: mkvpropedit or mkvextract not found in PATH. Tags will not be written (Plex ratings are still pushed).")
		}
		fmt.Printf("Updating NZ ratings in %s (all files)...\n", dir)
		stats := walkRatingsOnly(dir, tmdb, tagger, timer, rr, cache)
		fmt.Printf("\nDone. Updated: %d  Skipped: %d  Errors: %d\n", stats.Updated, stats.Skipped, stats.Errors)
		return
	}

	// A nil *realMKVApplier stored in the interface would be non-nil and panic.
	var applier MKVApplier
	if a, err := newRealMKVApplier(); err == nil {
		applier = a
	} else {
		fmt.Fprintf(os.Stderr, "Warning: %v. MKV metadata will not be updated and files are skipped, timestamps included (Plex updates still run).\n", err)
	}

	if cutoff.IsZero() {
		fmt.Println("--- Cutoff date: none (all files) ---")
	} else {
		fmt.Printf("--- Cutoff date: %s ---\n", cutoff.Format("2006-01-02"))
	}
	fmt.Printf("Scanning %s...\n", dir)
	stats := walkAndProcess(dir, tmdb, applier, timer, rr, cache, manualData, cutoff)

	fmt.Printf("\nDone. Updated: %d  Skipped: %d  Skipped (old): %d  Errors: %d\n",
		stats.Updated, stats.Skipped, stats.SkippedOld, stats.Errors)
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
