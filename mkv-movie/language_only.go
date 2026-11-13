package main

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// walkLanguageOnly sets the Plex audio and subtitle streams of every movie the
// cache maps a file to, without reading the files, so an offline drive doesn't
// matter. Movies go in order of their first file name, and the cache is saved
// after each one. plex must not be nil.
func walkLanguageOnly(tmdb TMDBMovieClient, plex PlexMovieClient, cache *MovieRatingCache, manualData map[string]MovieManualEntry, overwrite bool) Stats {
	movies := make(map[string][]string) // TMDB ID → its files' stems, sorted
	for id, paths := range cache.FilesByMovie() {
		for _, p := range paths {
			base := filepath.Base(p)
			movies[id] = append(movies[id], strings.TrimSuffix(base, filepath.Ext(base)))
		}
		slices.Sort(movies[id])
	}
	ids := make([]string, 0, len(movies))
	for id := range movies {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(cmp.Compare(movies[a][0], movies[b][0]), cmp.Compare(a, b))
	})

	var stats Stats
	saver := cacheSaver{cache: cache}
	for _, id := range ids {
		stats.add(processLanguageOnly(id, movies[id], tmdb, plex, cache, manualData, overwrite))
		saver.save(movies[id][0])
	}
	saver.finish()
	return stats
}

// processLanguageOnly sets the streams of the movie tmdbID, whose files have
// the given stems, and prints its result line, named by the first stem. Its
// languages come from the first stem whose manual_data.json entry sets one.
// TMDB is called only when that entry has no "language", or the streams are due.
func processLanguageOnly(tmdbID string, stems []string, tmdb TMDBMovieClient, plex PlexMovieClient, cache *MovieRatingCache, manualData map[string]MovieManualEntry, overwrite bool) FileResult {
	name := stems[0]
	entryStem := name
	var entry MovieManualEntry
	for _, s := range stems {
		if e := manualData[s]; e.Language != "" || e.AltLanguage != "" || e.Alt2Language != "" {
			entry, entryStem = e, s
			break
		}
	}

	var details *MovieDetails
	fetchDetails := func() bool {
		if details != nil {
			return true
		}
		d, err := tmdb.GetMovieDetails(tmdbID)
		if err != nil {
			fmt.Printf("[ERR]     %s: TMDB details: %v\n", name, err)
			return false
		}
		details = &d
		return true
	}

	tmdbLang := ""
	if entry.Language == "" {
		if !fetchDetails() {
			return ResultError
		}
		tmdbLang = details.OriginalLanguage
	}
	langs := entry.audioLangs(tmdbLang)
	if langs.Default == "" {
		fmt.Printf("[SKIP]    %s (TMDB has no original language; set \"language\" for %q in manual_data.json)\n", name, entryStem)
		return ResultSkipped
	}
	if !streamsDue(plex, cache, tmdbID, langs, overwrite) {
		fmt.Printf("[SKIP]    %s (streams set earlier)\n", name)
		return ResultSkipped
	}

	// Plex is searched by TMDB's title, so a remake can't match.
	if !fetchDetails() {
		return ResultError
	}
	year := releaseYear(details.ReleaseDate)
	if year == "" {
		year = extractYear(name)
	}
	rk, failed := lookupPlexMovie(plex, tmdbID, details.Title, year, "streams not set", "--language-only")
	if failed != "" {
		fmt.Printf("[ERR]     %s (failed: %s)\n", name, failed)
		return ResultError
	}
	if rk == "" {
		fmt.Printf("[SKIP]    %s (not in Plex)\n", name)
		return ResultSkipped
	}

	langSet, altSuffix, ok := pushPlexStreams(plex, cache, rk, tmdbID, entryStem, langs, overwrite)
	if !ok {
		fmt.Printf("[ERR]     %s (failed: Plex streams)\n", name)
		return ResultError
	}
	if langSet == "" {
		fmt.Printf("[SKIP]    %s (lang: unchanged%s)\n", name, altSuffix)
		return ResultSkipped
	}
	fmt.Printf("[UPDATED] %s (lang: %s%s)\n", name, langSet, altSuffix)
	return ResultUpdated
}
