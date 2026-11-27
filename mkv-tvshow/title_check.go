package main

import (
	"errors"
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

// TitleChecker compares episode title(s) in filenames against TMDB, offers to
// rename files where they differ, and sets the MKV title metadata to the full
// (untruncated) TMDB title. The full title is cached so the normal processing
// mode does not overwrite it with a shorter filename-derived title.
type TitleChecker struct {
	cfg        Config
	cache      *ShowCache
	manualData map[string]ManualEntry
	tmdb       TMDBClient
	prompter   Prompter
	mkv        MKVRunner   // nil → MKV title setting is skipped
	titleCache *TitleCache // nil → caching/setting skipped
	timer      FileTimer
	retryList  *RetryList // nil → a rename leaves the retry list alone
	moved      bool       // a rename moved a retry entry since the last save
	LostSaves  int        // set by Run: how many caches' changes couldn't be saved
}

// NewTitleChecker creates a TitleChecker.
func NewTitleChecker(
	cfg Config,
	cache *ShowCache,
	manualData map[string]ManualEntry,
	tmdb TMDBClient,
	prompter Prompter,
	mkv MKVRunner,
	titleCache *TitleCache,
	timer FileTimer,
	retryList *RetryList,
) *TitleChecker {
	return &TitleChecker{
		cfg:        cfg,
		cache:      cache,
		manualData: manualData,
		tmdb:       tmdb,
		prompter:   prompter,
		mkv:        mkv,
		titleCache: titleCache,
		timer:      timer,
		retryList:  retryList,
	}
}

// Run scans MediaDir and for each tracked episode:
//  1. Compares filename title(s) against TMDB and offers to rename on mismatch.
//  2. Sets the MKV title metadata to the full (untruncated) TMDB title and
//     caches it so normal mode will not overwrite it with the filename title.
func (tc *TitleChecker) Run() {
	showFiles := map[string][]string{}
	var showOrder []string

	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(tc.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("[!] Skipping unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
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
		cacheKey := filepath.Join(tc.cfg.MediaDir, showFolder)
		if _, ok := tc.cache.Get(cacheKey); !ok {
			if _, ok := tc.cache.Get(showFolder); !ok {
				return nil
			}
		}
		if _, seen := showFiles[showFolder]; !seen {
			showOrder = append(showOrder, showFolder)
		}
		showFiles[showFolder] = append(showFiles[showFolder], path)
		return nil
	})

	saver := newTitleSaver(tc.titleCache)
	retrySaver := newRetrySaver(tc.retryList)
	for _, showFolder := range showOrder {
		cacheKey := filepath.Join(tc.cfg.MediaDir, showFolder)
		showID, ok := tc.cache.Get(cacheKey)
		if !ok {
			showID, _ = tc.cache.Get(showFolder)
		}
		fmt.Printf("\nChecking: %s\n", showFolder)
		seasonCache := map[string]SeasonResult{}

		for _, path := range showFiles[showFolder] {
			dir := filepath.Dir(path)
			file := filepath.Base(path)
			ext := filepath.Ext(file)
			stem := strings.TrimSuffix(file, ext)

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
			epKey := fmt.Sprintf("S%02dE%02d", sNum, eNum)

			fileTitles := extractFileTitles(stem)
			if len(fileTitles) == 0 {
				continue
			}

			// Check for a manual title override before hitting TMDB.
			// The override is stored in FS-safe form (e.g. "╱" instead of "/").
			if entry, ok := tc.manualData[showFolder]; ok {
				if override, hasOverride := entry.TitleOverrides[epKey]; hasOverride {
					decoded := decodeOverrideTitles(override)
					effectivePath := tc.checkMismatch(path, file, stem, dir, ext, fileTitles, decoded, "Override", override)
					tc.applyFullTitle(effectivePath, buildFullMKVTitle(stem, decoded))
					continue
				}
			}

			tmdbID, tmdbSeason, tmdbEpisodeFirst := tc.resolveEpisode(showFolder, showID, sNum, eNum)

			cKey := fmt.Sprintf("%s_S%d", tmdbID, tmdbSeason)
			if _, ok := seasonCache[cKey]; !ok {
				sr, err := tc.tmdb.GetSeason(tmdbID, tmdbSeason)
				switch {
				case err == nil:
					seasonCache[cKey] = sr
				case !errors.Is(err, errTMDBNotFound):
					fmt.Printf("  [!] TMDB season lookup failed, not checking %s: %v\n", file, err)
				}
			}
			sr, ok := seasonCache[cKey]
			if !ok {
				continue
			}

			// All local episode numbers this file covers.
			localEpNums := tc.episodeNumbers(stem, eNum)

			// Determine which TMDB episodes to fetch titles for.
			// A 3+ element mapping [season, ep1, ep2, ...] gives an explicit list
			// for range files where local and TMDB numbering diverge non-linearly.
			// Otherwise fall back to a linear offset from the resolved first episode.
			var tmdbEpNums []int
			if entry, ok := tc.manualData[showFolder]; ok {
				if pair, ok := entry.Mapping[epKey]; ok && len(pair) >= 3 {
					tmdbEpNums = pair[1:]
				}
			}
			if tmdbEpNums == nil {
				offset := tmdbEpisodeFirst - eNum
				for _, n := range localEpNums {
					tmdbEpNums = append(tmdbEpNums, n+offset)
				}
			}

			var tmdbTitles []string
			for _, tmdbEp := range tmdbEpNums {
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

			// If TMDB only has placeholder titles ("Episode N") but the file
			// already has a real title, treat the file title as authoritative
			// until TMDB is updated. Skip the rename prompt and use the file's
			// own title for MKV metadata.
			if allPlaceholders(tmdbTitles) && !allPlaceholders(fileTitles) {
				tc.applyFullTitle(path, buildFullMKVTitle(stem, fileTitles))
				continue
			}

			// Build the FS-safe title part from TMDB titles for display and rename.
			var fsTitles []string
			for _, t := range tmdbTitles {
				fsTitles = append(fsTitles, toFSSafe(t))
			}
			fsTitlePart := strings.Join(fsTitles, " "+multiEpTitleSep+" ")

			effectivePath := tc.checkMismatch(path, file, stem, dir, ext, fileTitles, tmdbTitles, "TMDB", fsTitlePart)
			tc.applyFullTitle(effectivePath, buildFullMKVTitle(stem, tmdbTitles))
		}

		saver.save(showFolder)
		if tc.moved {
			retrySaver.save(showFolder)
			tc.moved = false
		}
	}
	for _, lost := range []bool{saver.finish(), retrySaver.finish()} {
		if lost {
			tc.LostSaves++
		}
	}
}

// checkMismatch compares fileTitles against expectedTitles and, when they
// differ, shows both versions and the exact new filename, then prompts to rename.
// fsTitlePart is the already FS-safe combined title to write into the new stem.
// label is the source label used in display ("TMDB" or "Override").
// Returns the effective file path after any rename (unchanged if no rename).
func (tc *TitleChecker) checkMismatch(path, file, stem, dir, ext string, fileTitles, expectedTitles []string, label, fsTitlePart string) string {
	if titlesMatch(fileTitles, expectedTitles) {
		return path
	}

	newStem, truncated := buildNewStemFromFS(stem, fsTitlePart, dir)

	// If the computed new stem is identical to the current stem the file was
	// already truncated to fit MAX_PATH and no rename is needed.
	if newStem == stem {
		return path
	}

	fmt.Printf("\n  [!] Title mismatch: %s\n", file)
	for i := 0; i < max(len(fileTitles), len(expectedTitles)); i++ {
		ft, et := "", ""
		if i < len(fileTitles) {
			ft = fileTitles[i]
		}
		if i < len(expectedTitles) {
			et = expectedTitles[i]
		}
		fmt.Printf("       File[%d]: %s\n", i+1, ft)
		fmt.Printf("  %s[%d]: %s\n", label, i+1, et)
	}
	fmt.Printf("      New name: %s\n", newStem+ext)
	if truncated {
		fmt.Printf("  [i] Title truncated to fit within MAX_PATH (%d chars)\n", windowsMaxPath)
	}

	newPath := filepath.Join(dir, newStem+ext)
	switch taken, err := otherFileAt(path, newPath); {
	case err != nil:
		fmt.Printf("  [!] Not renaming: %v\n", err)
		return path
	case taken:
		fmt.Printf("  [!] Not renaming: %s already exists\n", newStem+ext)
		return path
	}
	ans, _ := tc.prompter.Ask("  Rename? [y/n]: ")
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		return path
	}

	if err := renameNoReplace(path, newPath); err != nil {
		fmt.Printf("  [X] Rename failed: %v\n", err)
		return path
	}
	fmt.Printf("  [✓] Renamed to: %s\n", newStem+ext)
	if tc.retryList != nil && tc.retryList.Move(path, newPath) {
		tc.moved = true
	}
	// Drop the old title: applyFullTitle caches the new one once it writes it.
	tc.titleCache.Delete(path) // saved with the show
	return newPath
}

// applyFullTitle sets the MKV title metadata to fullTitle and caches the result.
// The file's original timestamps are preserved so the normal processing mode's
// cutoff filter is not affected. Does nothing when the title is already
// current or mkvpropedit is absent.
func (tc *TitleChecker) applyFullTitle(path, fullTitle string) {
	if fullTitle == "" {
		return
	}
	if tc.mkv == nil || !tc.mkv.Available() {
		return
	}
	// Already up to date — skip the mkvpropedit call.
	if cached, ok := tc.titleCache.Get(path); ok && cached == fullTitle {
		return
	}

	origStat, err := os.Stat(path)
	if err != nil {
		fmt.Printf("  [!] Cannot read timestamps to restore, not setting title: %v\n", err)
		return
	}

	combined, rc, err := tc.mkv.Apply(path, []string{"--edit", "info", "--set", "title=" + fullTitle})
	if err != nil {
		fmt.Printf("  [!] mkvpropedit error: %v\n", err)
		return
	}
	if !mkvpropeditWrote(rc) {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
		return
	}

	// Restore timestamps so normal mode's cutoff filter sees the original mtime.
	failed := setFileTimes(tc.timer, path, origStat.ModTime())

	tc.titleCache.Set(path, fullTitle)
	if len(failed) > 0 {
		fmt.Printf("  [X] MKV title set: %s (failed: %s)\n", fullTitle, strings.Join(failed, ", "))
		return
	}
	fmt.Printf("  [✓] MKV title set: %s\n", fullTitle)
}

// buildFullMKVTitle constructs the full MKV title from a filename stem and
// decoded (non-FS-safe) TMDB titles. The show name and episode code prefix has
// its FS-safe lookalikes restored to ASCII. MKV metadata has no path-length
// limit so the title is never truncated.
func buildFullMKVTitle(stem string, titles []string) string {
	m := titleEpCodeRe.FindStringIndex(stem)
	if m == nil {
		return ""
	}
	prefix := stem[:m[1]]
	for _, rc := range reverseChars {
		prefix = strings.ReplaceAll(prefix, rc.from, rc.to)
	}
	return prefix + " - " + strings.Join(titles, " "+multiEpTitleSep+" ")
}

// withEpisodeCode returns the cached full title with its episode code replaced
// by stem's, so a title cached before a multi-episode rename (S01E01E02E03 →
// S01E01-E03) matches the file. The title is unchanged if either has no code.
func withEpisodeCode(title, stem string) string {
	code := titleEpCodeRe.FindString(stem)
	loc := titleEpCodeRe.FindStringIndex(title)
	if code == "" || loc == nil {
		return title
	}
	return title[:loc[0]] + code + title[loc[1]:]
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
	if pair, ok := entry.Mapping[epKey]; ok && len(pair) >= 2 {
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

// decodeOverrideTitles splits a title_override value on the multi-episode
// separator and restores ASCII characters from filesystem-safe lookalikes.
func decodeOverrideTitles(override string) []string {
	parts := strings.Split(override, multiEpTitleSep)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		for _, rc := range reverseChars {
			p = strings.ReplaceAll(p, rc.from, rc.to)
		}
		result = append(result, p)
	}
	return result
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
	_, raw, ok := strings.Cut(after, " - ")
	if !ok {
		return nil
	}
	if raw == "" {
		return nil
	}
	// Strip any trailing -partN before splitting so multi-part files compare
	// correctly against TMDB titles (the suffix is a file-splitting convention,
	// not part of the episode title).
	raw, _ = stripPartSuffix(raw)
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

// partSuffixRe matches a trailing -partN suffix (case-insensitive) used when
// an episode spans multiple files (e.g. "-part1", "-Part2").
var partSuffixRe = regexp.MustCompile(`(?i)(-part\d+)$`)

// stripPartSuffix returns s with any trailing -partN removed and the suffix
// itself (empty string when absent).
func stripPartSuffix(s string) (string, string) {
	m := partSuffixRe.FindStringIndex(s)
	if m == nil {
		return s, ""
	}
	return s[:m[0]], s[m[0]:]
}

// placeholderTitleRe matches TMDB placeholder episode titles like "Episode 2".
var placeholderTitleRe = regexp.MustCompile(`(?i)^Episode\s+\d+$`)

// allPlaceholders reports whether every title in the slice is a TMDB
// placeholder of the form "Episode N".
func allPlaceholders(titles []string) bool {
	if len(titles) == 0 {
		return false
	}
	for _, t := range titles {
		if !placeholderTitleRe.MatchString(strings.TrimSpace(t)) {
			return false
		}
	}
	return true
}

// titlesMatch returns true when the file and expected title lists are equivalent
// (same count, each pair equal after normalisation).
func titlesMatch(fileTitles, expectedTitles []string) bool {
	if len(fileTitles) != len(expectedTitles) {
		return false
	}
	for i := range fileTitles {
		if !titleNormEqual(fileTitles[i], expectedTitles[i]) {
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

// buildNewStemFromFS replaces the title portion of a filename stem with a
// pre-computed filesystem-safe title part, truncating if necessary to keep
// the full path within windowsMaxPath characters.
// Returns the new stem and whether truncation occurred.
func buildNewStemFromFS(oldStem, fsTitlePart, dir string) (string, bool) {
	m := titleEpCodeRe.FindStringIndex(oldStem)
	if m == nil {
		return oldStem, false
	}
	prefix := oldStem[:m[1]]

	// Carry forward any -partN suffix from the old stem (file-splitting marker).
	_, partSuffix := stripPartSuffix(oldStem)

	newStem := prefix + " - " + fsTitlePart + partSuffix

	if len(filepath.Join(dir, newStem+".mkv")) <= windowsMaxPath {
		return newStem, false
	}

	overhead := len(filepath.Join(dir, prefix+" - .mkv")) + len(partSuffix) + 1 // +1 for null terminator
	maxTitle := windowsMaxPath - overhead
	if maxTitle <= 0 {
		return oldStem, false
	}
	return prefix + " - " + truncateTitleToBytes(fsTitlePart, maxTitle) + partSuffix, true
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
