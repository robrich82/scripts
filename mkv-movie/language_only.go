package main

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// walkLanguageOnly sets the Plex audio and subtitle streams of every movie the
// cache maps a file to, as a full run would, without reading the files, so an
// offline drive doesn't matter. Only the accounts a movie's streams are due on
// are set (see streamsDue). Movies are taken in file-name order, and the cache
// is saved after each one. plex must not be nil.
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
// languages come from the first stem with a manual_data.json entry, the
// primary's falling back to TMDB's original language. TMDB is called for that
// language, and for the title and year Plex is searched by when the streams
// are due.
func processLanguageOnly(tmdbID string, stems []string, tmdb TMDBMovieClient, plex PlexMovieClient, cache *MovieRatingCache, manualData map[string]MovieManualEntry, overwrite bool) FileResult {
	name := stems[0]
	entryStem := name
	var entry MovieManualEntry
	for _, s := range stems {
		if e, ok := manualData[s]; ok {
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

	// Plex is searched by TMDB's title, never the file's, so a remake can't
	// match; the file's year stands in when TMDB has no release date.
	if !fetchDetails() {
		return ResultError
	}
	year := releaseYear(details.ReleaseDate)
	if year == "" {
		year = extractYear(name)
	}
	rk, err := findPlexMovie(plex, tmdbID, details.Title, year)
	switch {
	case errors.Is(err, ErrPlexMovieNotFound) && year == "":
		fmt.Printf("  [!] Plex: %q not found by TMDB GUID, and with no release year it isn't searched by title – streams not set\n", details.Title)
		fmt.Printf("[SKIP]    %s (not in Plex)\n", name)
		return ResultSkipped
	case errors.Is(err, ErrPlexMovieNotFound):
		fmt.Printf("  [!] Plex: %q not found (tried TMDB GUID, then title and year) – streams not set; rerun with --language-only once Plex has it\n", details.Title)
		fmt.Printf("[SKIP]    %s (not in Plex)\n", name)
		return ResultSkipped
	case err != nil:
		fmt.Printf("  [!] Plex lookup failed – streams not set: %v\n", err)
		fmt.Printf("[ERR]     %s", name)
		printFailed([]string{"Plex lookup"})
		return ResultError
	}

	langSet, altSuffix, ok := pushPlexStreams(plex, cache, rk, tmdbID, entryStem, langs, overwrite)
	if !ok {
		fmt.Printf("[ERR]     %s", name)
		printFailed([]string{"Plex streams"})
		return ResultError
	}
	tag, result := "[UPDATED]", ResultUpdated
	note := langNote(langSet, altSuffix)
	if langSet == "" {
		tag, result = "[SKIP]   ", ResultSkipped
		if note == "" {
			note = "unchanged"
		}
	}
	fmt.Printf("%s %s (lang: %s)\n", tag, name, note)
	return result
}
