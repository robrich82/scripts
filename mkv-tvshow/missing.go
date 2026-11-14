package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const tmdbWebBase = "https://www.themoviedb.org/tv"

// parseSeasonKey parses a manual_data season key such as "S02" into its integer
// season number. Returns (0, false) for anything that isn't an "SNN" key.
func parseSeasonKey(sKey string) (int, bool) {
	if len(sKey) < 2 || (sKey[0] != 'S' && sKey[0] != 's') {
		return 0, false
	}
	n, err := strconv.Atoi(sKey[1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// missingItem represents a season or episode that has aired on TMDB but is
// not present in the local media directory.
type missingItem struct {
	kind         string // "season" or "episode"
	showID       string
	showFolder   string
	showName     string
	season       int
	episode      int    // 0 for whole-season items
	episodeCount int    // number of aired episodes (whole-season items only)
	episodes     []int  // individual episode numbers (whole-season items only)
	title        string // episode name (episode items only)
	airDate      string // first aired date for seasons; episode air date for episodes
	tmdbURL      string
}

// MissingChecker scans for episodes that have aired on TMDB but are not
// present in the local media directory.
type MissingChecker struct {
	cfg        Config
	cache      *ShowCache
	manualData map[string]ManualEntry
	tmdb       TMDBClient
	prompter   Prompter
	ignoreList *MissingIgnoreList
}

// NewMissingChecker constructs a MissingChecker.
func NewMissingChecker(
	cfg Config,
	cache *ShowCache,
	manualData map[string]ManualEntry,
	tmdb TMDBClient,
	prompter Prompter,
	ignoreList *MissingIgnoreList,
) *MissingChecker {
	return &MissingChecker{
		cfg:        cfg,
		cache:      cache,
		manualData: manualData,
		tmdb:       tmdb,
		prompter:   prompter,
		ignoreList: ignoreList,
	}
}

// Run iterates all show folders in MediaDir that are present in the show cache,
// queries TMDB for aired episodes, compares against local files, and reports
// missing content. Whole missing seasons are identified before individual
// missing episodes. The user is offered the option to add items to an ignore
// list after each show.
func (mc *MissingChecker) Run() error {
	today := time.Now().Format("2006-01-02")

	entries, err := os.ReadDir(mc.cfg.MediaDir)
	if err != nil {
		return fmt.Errorf("reading media dir: %w", err)
	}

	anyFound := false
	saver := &cacheSaver{cache: mc.ignoreList, name: "ignore list", path: mc.ignoreList.path,
		holds: "ignore choices"}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		folder := entry.Name()
		// Cache entries use the absolute folder path (legacy entries use the base
		// name); try both so --missing works regardless of which format was stored.
		showID, ok := mc.cache.Get(filepath.Join(mc.cfg.MediaDir, folder))
		if !ok {
			showID, ok = mc.cache.Get(folder)
		}
		if !ok {
			continue // not a tracked show
		}
		if mc.ignoreList.IsShowIgnored(showID) {
			continue
		}

		showDir := filepath.Join(mc.cfg.MediaDir, folder)

		// Fetch show details (includes season list and show name).
		details, err := mc.tmdb.GetShow(showID)
		if err != nil {
			fmt.Printf("Warning: could not fetch TMDB data for %q (ID %s): %v\n", folder, showID, err)
			continue
		}

		// Scan local files for episodes already present, then translate to
		// TMDB-space keys applying any manual overrides and episode mappings.
		localPresent, err := scanPresentEpisodes(showDir)
		if err != nil {
			fmt.Printf("Warning: could not scan %q: %v\n", folder, err)
			continue
		}
		manualEntry := mc.manualData[folder] // zero value if not present
		tmdbPresent := buildTMDBPresentSet(showID, localPresent, manualEntry)

		showName := details.Name
		if showName == "" {
			showName = folder
		}

		var missing []missingItem

		// Local seasons to check: the primary show's own seasons plus any season
		// that exists only under a manual override. A split-cour show's second
		// cour is often a separate TMDB entry not listed in the primary show's
		// season array, so relying on details.Seasons alone would never check it.
		localSeasons := make([]int, 0, len(details.Seasons)+len(manualEntry.Overrides))
		seenSeason := make(map[int]bool)
		for _, s := range details.Seasons {
			if !seenSeason[s.SeasonNumber] {
				seenSeason[s.SeasonNumber] = true
				localSeasons = append(localSeasons, s.SeasonNumber)
			}
		}
		for sKey := range manualEntry.Overrides {
			if n, ok := parseSeasonKey(sKey); ok && !seenSeason[n] {
				seenSeason[n] = true
				localSeasons = append(localSeasons, n)
			}
		}
		sort.Ints(localSeasons)

		for _, localSeason := range localSeasons {
			if mc.ignoreList.IsSeasonIgnored(showID, localSeason) {
				continue
			}

			// Apply any season override: local season N may map to a different
			// TMDB show ID and/or season number (common with split-cour anime).
			effectiveID := showID
			effectiveSeason := localSeason
			sKey := fmt.Sprintf("S%02d", localSeason)
			if ovr, ok := manualEntry.Overrides[sKey]; ok {
				if ovr.ID != "" {
					effectiveID = ovr.ID
				}
				if ovr.MapSeason != 0 {
					effectiveSeason = ovr.MapSeason
				}
			}

			// Fetch the full episode list for the effective TMDB show/season.
			sr, err := mc.tmdb.GetSeason(effectiveID, effectiveSeason)
			if err != nil {
				fmt.Printf("Warning: could not fetch season %d for %q: %v\n", localSeason, folder, err)
				continue
			}

			// Keep only episodes that have already aired.
			var aired []Episode
			for _, ep := range sr.Episodes {
				if ep.AirDate != "" && ep.AirDate <= today {
					aired = append(aired, ep)
				}
			}
			if len(aired) == 0 {
				continue
			}

			// Check against the TMDB-space present set for the effective show.
			presentForShow := tmdbPresent[effectiveID]
			var missingEps []Episode
			for _, ep := range aired {
				key := fmt.Sprintf("S%02dE%02d", effectiveSeason, ep.EpisodeNumber)
				if !presentForShow[key] && !mc.ignoreList.IsEpisodeIgnored(showID, localSeason, ep.EpisodeNumber) {
					missingEps = append(missingEps, ep)
				}
			}
			if len(missingEps) == 0 {
				continue
			}

			if len(missingEps) == len(aired) {
				// Every aired episode is missing — report the whole season.
				epNums := make([]int, len(missingEps))
				for i, ep := range missingEps {
					epNums[i] = ep.EpisodeNumber
				}
				missing = append(missing, missingItem{
					kind:         "season",
					showID:       showID,
					showFolder:   folder,
					showName:     showName,
					season:       localSeason,
					episodeCount: len(aired),
					episodes:     epNums,
					airDate:      aired[0].AirDate,
					tmdbURL:      fmt.Sprintf("%s/%s/season/%d", tmdbWebBase, effectiveID, effectiveSeason),
				})
			} else {
				// Only some episodes are missing — report individually.
				for _, ep := range missingEps {
					missing = append(missing, missingItem{
						kind:       "episode",
						showID:     showID,
						showFolder: folder,
						showName:   showName,
						season:     localSeason,
						episode:    ep.EpisodeNumber,
						title:      ep.Name,
						airDate:    ep.AirDate,
						tmdbURL:    fmt.Sprintf("%s/%s/season/%d/episode/%d", tmdbWebBase, effectiveID, effectiveSeason, ep.EpisodeNumber),
					})
				}
			}
		}

		if len(missing) == 0 {
			continue
		}

		anyFound = true

		// Sort: whole missing seasons first (by season number), then individual
		// missing episodes (by season, then episode number).
		sort.Slice(missing, func(i, j int) bool {
			if missing[i].kind != missing[j].kind {
				return missing[i].kind == "season"
			}
			if missing[i].season != missing[j].season {
				return missing[i].season < missing[j].season
			}
			return missing[i].episode < missing[j].episode
		})

		// Display.
		fmt.Printf("\n=== %s (TMDB ID: %s) ===\n", showName, showID)
		for i, item := range missing {
			if item.kind == "season" {
				fmt.Printf("  [%d] Season %d missing entirely (%d episodes, first aired %s)\n",
					i+1, item.season, item.episodeCount, item.airDate)
			} else {
				if item.title != "" {
					fmt.Printf("  [%d] S%02dE%02d - %q (aired %s)\n",
						i+1, item.season, item.episode, item.title, item.airDate)
				} else {
					fmt.Printf("  [%d] S%02dE%02d (aired %s)\n",
						i+1, item.season, item.episode, item.airDate)
				}
			}
			fmt.Printf("       %s\n", item.tmdbURL)
		}

		// Prompt the user to optionally ignore items.
		ans, err := mc.prompter.Ask(
			"\nIgnore items? Enter numbers (e.g. \"1,3\"), \"all\" to ignore all listed, \"show\" to ignore the whole show, or press Enter to skip: ",
		)
		if err != nil || strings.TrimSpace(ans) == "" {
			continue
		}

		ans = strings.TrimSpace(ans)
		if ans == "show" {
			mc.ignoreList.AddShow(showID)
			fmt.Printf("Added %q to the missing-episode ignore list.\n", showName)
		} else {
			items := missing
			if ans != "all" {
				items = nil
				for part := range strings.SplitSeq(ans, ",") {
					part = strings.TrimSpace(part)
					if lo, hi, ok := parseRange(part, len(missing)); ok {
						for i := lo; i <= hi; i++ {
							items = append(items, missing[i-1])
						}
					} else {
						fmt.Printf("  Skipping unrecognised entry %q\n", part)
					}
				}
			}
			for _, item := range items {
				if item.kind == "season" {
					// Season 0 (specials) uses per-episode ignores so that newly
					// added specials still surface in future checks.
					if item.season == 0 {
						for _, epNum := range item.episodes {
							mc.ignoreList.AddEpisode(showID, 0, epNum)
						}
						fmt.Printf("  Ignoring %d current specials of %s (new specials will still be reported)\n", len(item.episodes), showName)
					} else {
						mc.ignoreList.AddSeason(showID, item.season)
						fmt.Printf("  Ignoring Season %d of %s\n", item.season, showName)
					}
				} else {
					mc.ignoreList.AddEpisode(showID, item.season, item.episode)
					fmt.Printf("  Ignoring S%02dE%02d of %s\n", item.season, item.episode, showName)
				}
			}
		}

		saver.save(showName)
	}
	saver.finish()

	if !anyFound {
		fmt.Println("No missing episodes found.")
	}
	fmt.Println("\nMissing episode check complete.")
	return nil
}

// buildTMDBPresentSet translates a set of local S##E## keys into TMDB-space
// episode keys, grouped by effective TMDB show ID. Season overrides and
// episode mappings from the manual data entry are applied so that comparisons
// are made against the correct TMDB show and episode numbers.
func buildTMDBPresentSet(showID string, localPresent map[string]bool, entry ManualEntry) map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for key := range localPresent {
		m := epRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		sNum, _ := strconv.Atoi(m[1])
		eNum, _ := strconv.Atoi(m[2])

		effectiveID := showID
		tmdbSeason := sNum
		tmdbEpisode := eNum

		sKey := fmt.Sprintf("S%02d", sNum)
		if ovr, ok := entry.Overrides[sKey]; ok {
			if ovr.ID != "" {
				effectiveID = ovr.ID
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

		if result[effectiveID] == nil {
			result[effectiveID] = make(map[string]bool)
		}
		result[effectiveID][fmt.Sprintf("S%02dE%02d", tmdbSeason, tmdbEpisode)] = true
	}
	return result
}

// parseRange parses a single token that is either a plain number ("3") or a
// range ("1-5") and returns (lo, hi, true) when valid and within [1, max].
func parseRange(s string, max int) (lo, hi int, ok bool) {
	if idx := strings.Index(s, "-"); idx > 0 {
		l, err1 := strconv.Atoi(s[:idx])
		h, err2 := strconv.Atoi(s[idx+1:])
		if err1 != nil || err2 != nil || l < 1 || h > max || l > h {
			return 0, 0, false
		}
		return l, h, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > max {
		return 0, 0, false
	}
	return n, n, true
}

// scanPresentEpisodes walks showDir and returns a set of "S##E##" keys for
// every MKV file that matches a standard episode pattern. Multi-episode
// files (e.g. S01E01E02E03) are expanded to individual keys.
func scanPresentEpisodes(showDir string) (map[string]bool, error) {
	present := make(map[string]bool)
	err := filepath.WalkDir(showDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		name := d.Name()
		// Multi-episode file with consecutive markers: S01E01E02E03
		if m := multiEpRe.FindStringSubmatch(name); m != nil {
			season := strings.ToUpper(m[1]) // e.g. "S01"
			for _, em := range eNumRe.FindAllStringSubmatch(m[2], -1) {
				epNum, _ := strconv.Atoi(em[1])
				present[fmt.Sprintf("%sE%02d", season, epNum)] = true
			}
		} else if m := epRangeRe.FindStringSubmatch(name); m != nil {
			// Plex-normalised range: S01E01-E03
			sNum, _ := strconv.Atoi(m[1])
			lo, _ := strconv.Atoi(m[2])
			hi, _ := strconv.Atoi(m[3])
			for ep := lo; ep <= hi; ep++ {
				present[fmt.Sprintf("S%02dE%02d", sNum, ep)] = true
			}
		} else if m := epRe.FindStringSubmatch(name); m != nil {
			sNum, _ := strconv.Atoi(m[1])
			eNum, _ := strconv.Atoi(m[2])
			present[fmt.Sprintf("S%02dE%02d", sNum, eNum)] = true
		}
		return nil
	})
	return present, err
}
