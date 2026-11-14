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

// RunLanguageOnly sets the Plex audio language of every show in the show
// cache without scanning any files, printing a result line per show. Shows go
// in case-insensitive order of their first folder name, and the rating cache
// is saved after each one. plex must not be nil.
func (p *Processor) RunLanguageOnly() LanguageOnlyStats {
	shows := make(map[string][]string) // TMDB ID → its folders' base names
	for folder, id := range p.cache.AllEntries() {
		if id != "" {
			shows[id] = append(shows[id], filepath.Base(folder))
		}
	}
	byName := func(a, b string) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a), strings.ToLower(b)), cmp.Compare(a, b))
	}
	ids := make([]string, 0, len(shows))
	for id, names := range shows {
		slices.SortFunc(names, byName)
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(byName(shows[a][0], shows[b][0]), cmp.Compare(a, b))
	})

	var stats LanguageOnlyStats
	saver := newRatingSaver(p.ratingCache, "Plex keys and audio languages")
	for _, id := range ids {
		switch p.languageOnlyShow(id, shows[id]) {
		case languageUpdated:
			stats.Updated++
		case languageSkipped:
			stats.Skipped++
		default:
			stats.Errors++
		}
		saver.save(shows[id][0])
	}
	saver.finish()
	return stats
}

// mergedLanguageEntry takes each language key from the first folder whose
// manual_data.json entry sets it.
func mergedLanguageEntry(folders []string, manualData map[string]ManualEntry) ManualEntry {
	var entry ManualEntry
	for _, f := range folders {
		e := manualData[f]
		entry.Language = cmp.Or(entry.Language, e.Language)
		entry.AltLanguage = cmp.Or(entry.AltLanguage, e.AltLanguage)
		entry.Alt2Language = cmp.Or(entry.Alt2Language, e.Alt2Language)
	}
	return entry
}

// languageOnlyShow sets the audio language of the show tmdbID, cached under
// the given sorted folder names, and prints its result line, named by the
// first folder. Plex is searched only when the language is due.
func (p *Processor) languageOnlyShow(tmdbID string, folders []string) languageOnlyResult {
	name := folders[0]
	langs, err := p.showAudioLangs(tmdbID, mergedLanguageEntry(folders, p.manualData))
	if err != nil {
		fmt.Printf("[ERR]     %s: TMDB show: %v\n", name, err)
		return languageError
	}
	if langs.Default == "" {
		fmt.Printf("[SKIP]    %s (TMDB has no original language; set \"language\" for %q in manual_data.json)\n", name, name)
		return languageSkipped
	}
	applied, due := p.languageDue(tmdbID, langs)
	if !due {
		fmt.Printf("[SKIP]    %s (language set earlier)\n", name)
		return languageSkipped
	}

	showKey, _ := p.ratingCache.GetPlexKey(tmdbID)
	if showKey == "" {
		showKey, err = p.findPlexShow(tmdbID, name)
		if err != nil {
			fmt.Printf("[ERR]     %s (failed: Plex lookup: %v)\n", name, err)
			return languageError
		}
		if showKey == "" {
			fmt.Printf("[SKIP]    %s (not in Plex; rerun with --language-only once Plex has it)\n", name)
			return languageSkipped
		}
		p.ratingCache.SetPlexKey(tmdbID, showKey)
	}

	set, altSuffix, err := p.setAudioLanguage(showKey, tmdbID, langs, applied)
	switch {
	case errors.Is(err, ErrPlexNotFound):
		fmt.Printf("[SKIP]    %s (Plex no longer has show %s; key cleared, rerun with --language-only to look it up again)\n", name, showKey)
		return languageSkipped
	case err != nil:
		fmt.Printf("[ERR]     %s (failed: Plex language: %v)\n", name, err)
		return languageError
	case set == "":
		fmt.Printf("[SKIP]    %s (lang: unchanged%s)\n", name, altSuffix)
		return languageSkipped
	}
	fmt.Printf("[UPDATED] %s (lang: %s%s)\n", name, set, altSuffix)
	return languageUpdated
}

// findPlexShow returns the show's Plex key, found by TMDB GUID or else by its
// folder name, or "" when Plex doesn't have it.
func (p *Processor) findPlexShow(tmdbID, showName string) (string, error) {
	key, err := p.plex.FindShowByTMDBID(tmdbID)
	if errors.Is(err, ErrPlexNotFound) || (err == nil && key == "") {
		// Plex display titles don't include years, so the folder name loses its
		// year suffix and Unicode lookalikes first.
		key, err = p.plex.FindShowByTitle(cleanSearchQuery(showName))
	}
	if errors.Is(err, ErrPlexNotFound) {
		return "", nil
	}
	return key, err
}
