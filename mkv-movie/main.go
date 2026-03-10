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

	mi, err := newRealMediaInfoReader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: 'mediainfo' CLI not found. Please install it and add to PATH.")
		os.Exit(1)
	}

	syncer, err := newRealMKVSyncer()
	if err != nil {
		// mkvpropedit is optional — header sync will be skipped.
		fmt.Fprintln(os.Stderr, "Warning: 'mkvpropedit' not found. Internal header will not be updated.")
		syncer = nil
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

	// Rating pipeline (all optional).
	tmdbKey := os.Getenv("TMDB_API_KEY")
	plexURL := os.Getenv("PLEX_URL")
	plexToken := os.Getenv("PLEX_TOKEN")

	ratingCache, err := LoadMovieRatingCache(filepath.Join(cfgDir, "movie_rating_cache.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load rating cache: %v\n", err)
		ratingCache, _ = LoadMovieRatingCache("/dev/null") // empty fallback
	}

	var plex PlexMovieClient
	if plexURL != "" && plexToken != "" {
		plex = newHTTPPlexMovieClient(plexURL, plexToken)
	}

	sources := []RatingSource{
		newTMDBMovieSource(tmdbKey),
		newClassificationSource(),
	}
	rr := newMovieRatingResolver(sources, ratingCache, *overwriteRatings, plex, stdinPrompter)

	if plex != nil {
		if err := plex.RefreshLibraries(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("Plex libraries refreshed.")
		}
	}

	fmt.Printf("Scanning %s...\n", dir)
	stats := walkAndProcess(dir, mi, syncer, timer, rr)

	fmt.Printf("\nDone. Updated: %d  OK: %d  Skipped: %d  Errors: %d\n",
		stats.Updated, stats.OK, stats.Skipped, stats.Errors)
}
