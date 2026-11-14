package main

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// LanguageOnlyStats counts the shows of a --language-only run.
type LanguageOnlyStats struct {
	Updated, Skipped, Errors int
}

type languageOnlyResult int

const (
	languageUpdated languageOnlyResult = iota
	languageSkipped
	languageError
)

// RunLanguageOnly sets the Plex audio language of every show in the rating
// cache without scanning any files, printing a result line per show. Shows go
// in order of their cached folder name, and the rating cache is saved after
// each show that changed it. plex must not be nil.
func (p *Processor) RunLanguageOnly() LanguageOnlyStats {
	// The show cache maps folder paths to TMDB IDs; a show cached under several
	// folders takes the first name, so the order and manual_data.json entry
	// don't depend on map order.
	names := make(map[string]string)
	for folder, id := range p.cache.AllEntries() {
		name := filepath.Base(folder)
		if cur, ok := names[id]; !ok || name < cur {
			names[id] = name
		}
	}
	ids := p.ratingCache.AllShowIDs()
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(cmp.Compare(strings.ToLower(names[a]), strings.ToLower(names[b])), cmp.Compare(a, b))
	})

	var stats LanguageOnlyStats
	for _, id := range ids {
		result, changed := p.languageOnlyShow(id, names[id])
		switch result {
		case languageUpdated:
			stats.Updated++
		case languageSkipped:
			stats.Skipped++
		default:
			stats.Errors++
		}
		if changed {
			if err := p.ratingCache.Save(); err != nil {
				fmt.Printf("  [!] Failed to save rating cache: %v\n", err)
			}
		}
	}
	return stats
}

// languageOnlyShow sets the audio language of the show tmdbID, whose cached
// folder is showName ("" when none is), and prints its result line. Plex is
// searched only when the language is due. changed reports whether the rating
// cache was written.
func (p *Processor) languageOnlyShow(tmdbID, showName string) (result languageOnlyResult, changed bool) {
	name := cmp.Or(showName, "TMDB "+tmdbID)
	langs, err := p.showAudioLangs(tmdbID, p.manualData[showName])
	if err != nil {
		fmt.Printf("[ERR]     %s: TMDB show: %v\n", name, err)
		return languageError, false
	}
	if langs.Default == "" {
		if showName == "" {
			fmt.Printf("[SKIP]    %s (TMDB has no original language, and no show folder is cached to set \"language\" for)\n", name)
		} else {
			fmt.Printf("[SKIP]    %s (TMDB has no original language; set \"language\" for %q in manual_data.json)\n", name, showName)
		}
		return languageSkipped, false
	}
	applied, due := p.languageDue(tmdbID, langs)
	if !due {
		fmt.Printf("[SKIP]    %s (language set earlier)\n", name)
		return languageSkipped, false
	}

	showKey, _ := p.ratingCache.GetPlexKey(tmdbID)
	if showKey == "" {
		showKey, err = p.findPlexShow(tmdbID, showName)
		if err != nil {
			fmt.Printf("[ERR]     %s (failed: Plex lookup: %v)\n", name, err)
			return languageError, false
		}
		if showKey == "" {
			fmt.Printf("[SKIP]    %s (not in Plex; rerun with --language-only once Plex has it)\n", name)
			return languageSkipped, false
		}
		p.ratingCache.SetPlexKey(tmdbID, showKey)
		changed = true
	}

	set, altSuffix, err := p.setAudioLanguage(showKey, tmdbID, langs, applied)
	switch {
	case errors.Is(err, ErrPlexNotFound):
		fmt.Printf("[SKIP]    %s (Plex no longer has show %s; key cleared, rerun with --language-only to look it up again)\n", name, showKey)
		return languageSkipped, true
	case err != nil:
		fmt.Printf("[ERR]     %s (failed: Plex language: %v)\n", name, err)
		return languageError, changed
	case set == "":
		fmt.Printf("[SKIP]    %s (lang: unchanged%s)\n", name, altSuffix)
		return languageSkipped, true
	}
	fmt.Printf("[UPDATED] %s (lang: %s%s)\n", name, set, altSuffix)
	return languageUpdated, true
}

// findPlexShow returns the show's Plex key, found by TMDB GUID or else by its
// folder name, or "" when Plex doesn't have it.
func (p *Processor) findPlexShow(tmdbID, showName string) (string, error) {
	key, err := p.plex.FindShowByTMDBID(tmdbID)
	if (errors.Is(err, ErrPlexNotFound) || (err == nil && key == "")) && showName != "" {
		// Plex display titles don't include years, so the folder name loses its
		// year suffix and Unicode lookalikes first.
		key, err = p.plex.FindShowByTitle(cleanSearchQuery(showName))
	}
	if errors.Is(err, ErrPlexNotFound) {
		return "", nil
	}
	return key, err
}
