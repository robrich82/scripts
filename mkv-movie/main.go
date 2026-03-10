package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	overwriteRatings := flag.Bool("overwrite-ratings", false, "Re-fetch all ratings, ignoring cached values.")
	flag.Parse()

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

	timer := &win32FileTimer{}

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
	rr := newMovieRatingResolver(sources, cache, *overwriteRatings, plex, stdinPrompter)

	fmt.Printf("Scanning %s...\n", dir)
	stats := walkAndProcess(dir, tmdb, applier, timer, rr, cache)

	if plex != nil {
		if err := plex.RefreshLibraries(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("Plex libraries refreshed.")
		}
	}

	fmt.Printf("\nDone. Updated: %d  OK: %d  Skipped: %d  Errors: %d\n",
		stats.Updated, stats.OK, stats.Skipped, stats.Errors)
}
