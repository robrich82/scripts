package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// windowsMaxPath is the effective maximum full path length (MAX_PATH - 1).
const windowsMaxPath = 259

// multiEpTitleSep is the SMALL PLUS SIGN (U+FE62) used to separate episode
// titles in multi-episode filenames.
const multiEpTitleSep = "﹢"

// titleEpCodeRe matches the episode code in a filename stem, covering the
// single (S01E01), dual (S01E01E02), and Plex-normalised range (S01E01-E02) forms.
var titleEpCodeRe = regexp.MustCompile(`(?i)S\d+(?:E\d+)+(?:-E\d+)?`)

// TitleChecker compares episode title(s) in filenames against TMDB and
// prompts to rename files where they differ.
type TitleChecker struct {
	cfg        Config
	cache      *ShowCache
	manualData map[string]ManualEntry
	tmdb       TMDBClient
	prompter   Prompter
}

// NewTitleChecker creates a TitleChecker.
func NewTitleChecker(cfg Config, cache *ShowCache, manualData map[string]ManualEntry, tmdb TMDBClient, prompter Prompter) *TitleChecker {
	return &TitleChecker{
		cfg:        cfg,
		cache:      cache,
		manualData: manualData,
		tmdb:       tmdb,
		prompter:   prompter,
	}
}

// Run scans MediaDir and compares episode titles in filenames against TMDB.
// For every mismatch it displays both titles and offers to rename the file.
func (tc *TitleChecker) Run() error {
	showFiles := map[string][]string{}
	var showOrder []string

	err := filepath.WalkDir(tc.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		rel, err := filepath.Rel(tc.cfg.MediaDir, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) < 2 {
			return nil
		}
		showFolder := parts[0]
		if _, ok := tc.cache.Get(showFolder); !ok {
			return nil
		}
		if _, seen := showFiles[showFolder]; !seen {
			showOrder = append(showOrder, showFolder)
		}
		showFiles[showFolder] = append(showFiles[showFolder], path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking media dir: %w", err)
	}

	for _, showFolder := range showOrder {
		showID, _ := tc.cache.Get(showFolder)
		fmt.Printf("\nChecking: %s\n", showFolder)
		seasonCache := map[string]SeasonResult{}

		for _, path := range showFiles[showFolder] {
			dir := filepath.Dir(path)
			file := filepath.Base(path)
			stem := strings.TrimSuffix(file, filepath.Ext(file))

			// Skip files explicitly listed in the extras map.
			if entry, ok := tc.manualData[showFolder]; ok {
				if _, isExtra := entry.Extras[stem]; isExtra {
					continue
				}
			}

			epMatch := epRe.FindStringSubmatch(file)
			if epMatch == nil {
				continue
			}
			sNum, _ := strconv.Atoi(epMatch[1])
			eNum, _ := strconv.Atoi(epMatch[2])

			tmdbID, tmdbSeason, tmdbEpisodeFirst := tc.resolveEpisode(showFolder, showID, sNum, eNum)

			cKey := fmt.Sprintf("%s_S%d", tmdbID, tmdbSeason)
			if _, ok := seasonCache[cKey]; !ok {
				if sr, err := tc.tmdb.GetSeason(tmdbID, tmdbSeason); err == nil {
					seasonCache[cKey] = sr
				}
			}
			sr, ok := seasonCache[cKey]
			if !ok {
				continue
			}

			// All local episode numbers this file covers.
			localEpNums := tc.episodeNumbers(stem, eNum)

			// Map each local episode number to the corresponding TMDB episode.
			offset := tmdbEpisodeFirst - eNum
			var tmdbTitles []string
			for _, n := range localEpNums {
				tmdbEp := n + offset
				for _, ep := range sr.Episodes {
					if ep.EpisodeNumber == tmdbEp {
						tmdbTitles = append(tmdbTitles, ep.Name)
						break
					}
				}
			}
			if len(tmdbTitles) == 0 {
				continue
			}

			fileTitles := extractFileTitles(stem)
			if len(fileTitles) == 0 {
				continue
			}

			if titlesMatch(fileTitles, tmdbTitles) {
				continue
			}

			fmt.Printf("\n  [!] Title mismatch: %s\n", file)
			for i := 0; i < max(len(fileTitles), len(tmdbTitles)); i++ {
				ft, tt := "", ""
				if i < len(fileTitles) {
					ft = fileTitles[i]
				}
				if i < len(tmdbTitles) {
					tt = tmdbTitles[i]
				}
				fmt.Printf("       File[%d]: %s\n", i+1, ft)
				fmt.Printf("       TMDB[%d]: %s\n", i+1, tt)
			}

			ans, _ := tc.prompter.Ask("  Rename to use TMDB title(s)? [y/n]: ")
			if strings.ToLower(strings.TrimSpace(ans)) != "y" {
				continue
			}

			newStem, truncated := buildNewStem(stem, tmdbTitles, dir)
			if truncated {
				fmt.Printf("  [i] Title truncated to fit within MAX_PATH (%d chars)\n", windowsMaxPath)
			}

			newPath := filepath.Join(dir, newStem+filepath.Ext(file))
			if err := os.Rename(path, newPath); err != nil {
				fmt.Printf("  [X] Rename failed: %v\n", err)
			} else {
				fmt.Printf("  [✓] Renamed to: %s\n", newStem+filepath.Ext(file))
			}
		}
	}

	return nil
}

// resolveEpisode applies manual overrides and mappings, mirroring the
// Processor method for title-check runs.
func (tc *TitleChecker) resolveEpisode(showFolder, showID string, sNum, eNum int) (id string, tmdbSeason, tmdbEpisode int) {
	id, tmdbSeason, tmdbEpisode = showID, sNum, eNum
	entry, ok := tc.manualData[showFolder]
	if !ok {
		return
	}
	sKey := fmt.Sprintf("S%02d", sNum)
	if ovr, ok := entry.Overrides[sKey]; ok {
		if ovr.ID != "" {
			id = ovr.ID
		}
		if ovr.MapSeason != 0 {
			tmdbSeason = ovr.MapSeason
		}
	}
	epKey := fmt.Sprintf("%sE%02d", sKey, eNum)
	if pair, ok := entry.Mapping[epKey]; ok {
		tmdbSeason = pair[0]
		tmdbEpisode = pair[1]
	}
	return
}

// episodeNumbers returns the local episode numbers covered by a filename stem.
// For single episodes it returns [firstENum]. For dual (S01E01E02) and
// Plex-normalised range (S01E01-E02) files it returns all numbers in the group.
func (tc *TitleChecker) episodeNumbers(stem string, firstENum int) []int {
	if m := multiEpRe.FindStringSubmatch(stem); m != nil {
		nums := eNumRe.FindAllStringSubmatch(m[2], -1)
		if len(nums) >= 2 {
			var result []int
			for _, n := range nums {
				v, _ := strconv.Atoi(n[1])
				result = append(result, v)
			}
			return result
		}
	}
	if m := epRangeRe.FindStringSubmatch(stem); m != nil {
		first, _ := strconv.Atoi(m[2])
		last, _ := strconv.Atoi(m[3])
		var result []int
		for i := first; i <= last; i++ {
			result = append(result, i)
		}
		return result
	}
	return []int{firstENum}
}

// extractFileTitles returns the episode title(s) from a filename stem.
// Titles are split on the SMALL PLUS SIGN separator for multi-episode files,
// and filesystem-safe lookalikes are restored to their ASCII originals.
// Returns nil when no title part can be identified.
func extractFileTitles(stem string) []string {
	m := titleEpCodeRe.FindStringIndex(stem)
	if m == nil {
		return nil
	}
	after := stem[m[1]:]
	idx := strings.Index(after, " - ")
	if idx < 0 {
		return nil
	}
	raw := after[idx+3:]
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, multiEpTitleSep)
	var titles []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		for _, rc := range reverseChars {
			p = strings.ReplaceAll(p, rc.from, rc.to)
		}
		titles = append(titles, p)
	}
	return titles
}

// titlesMatch returns true when the file and TMDB title lists are equivalent
// (same count, each pair equal after normalisation).
func titlesMatch(fileTitles, tmdbTitles []string) bool {
	if len(fileTitles) != len(tmdbTitles) {
		return false
	}
	for i := range fileTitles {
		if !titleNormEqual(fileTitles[i], tmdbTitles[i]) {
			return false
		}
	}
	return true
}

// titleNormEqual compares two titles case-insensitively with whitespace normalisation.
func titleNormEqual(a, b string) bool {
	return strings.EqualFold(normalizeTitle(a), normalizeTitle(b))
}

// normalizeTitle trims and collapses internal whitespace.
func normalizeTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// toFSSafe converts a title to a filesystem-safe name by replacing characters
// that are illegal in Windows filenames with Unicode lookalikes.
func toFSSafe(title string) string {
	result := title
	for _, rc := range reverseChars {
		result = strings.ReplaceAll(result, rc.to, rc.from)
	}
	return result
}

// buildNewStem replaces the title portion of a filename stem with the TMDB
// titles (converted to filesystem-safe form), truncating if necessary to keep
// the full path within windowsMaxPath characters.
// Returns the new stem and whether truncation occurred.
func buildNewStem(oldStem string, tmdbTitles []string, dir string) (string, bool) {
	m := titleEpCodeRe.FindStringIndex(oldStem)
	if m == nil {
		return oldStem, false
	}
	prefix := oldStem[:m[1]]

	var fsTitles []string
	for _, t := range tmdbTitles {
		fsTitles = append(fsTitles, toFSSafe(t))
	}
	titlePart := strings.Join(fsTitles, " "+multiEpTitleSep+" ")
	newStem := prefix + " - " + titlePart

	if len(filepath.Join(dir, newStem+".mkv")) <= windowsMaxPath {
		return newStem, false
	}

	// Calculate how many bytes the title part may occupy.
	overhead := len(filepath.Join(dir, prefix+" - .mkv")) + 1 // +1 for null terminator
	maxTitle := windowsMaxPath - overhead
	if maxTitle <= 0 {
		return oldStem, false
	}
	return prefix + " - " + truncateTitleToBytes(titlePart, maxTitle), true
}

// truncateTitleToBytes shortens s to fit within maxBytes, preferring a word
// boundary. Any trailing separator fragments are stripped.
func truncateTitleToBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Trim to a valid UTF-8 boundary.
	limit := maxBytes
	for limit > 0 && !utf8.Valid([]byte(s[:limit])) {
		limit--
	}
	// Walk back to the nearest space for a clean word boundary.
	wordLimit := limit
	for wordLimit > 0 && s[wordLimit-1] != ' ' {
		wordLimit--
	}
	result := ""
	if wordLimit > 0 {
		result = strings.TrimRight(s[:wordLimit], " "+multiEpTitleSep)
	}
	if result == "" {
		// No word boundary: hard-truncate at the UTF-8 boundary.
		result = s[:limit]
	}
	return result
}
