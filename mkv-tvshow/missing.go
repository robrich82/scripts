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
	title        string // episode name (episode items only)
	airDate      string // first aired date for seasons; episode air date for episodes
	tmdbURL      string
}

// MissingChecker scans for episodes that have aired on TMDB but are not
// present in the local media directory.
type MissingChecker struct {
	cfg        Config
	cache      *ShowCache
	tmdb       TMDBClient
	prompter   Prompter
	ignoreList *MissingIgnoreList
}

// NewMissingChecker constructs a MissingChecker.
func NewMissingChecker(
	cfg Config,
	cache *ShowCache,
	tmdb TMDBClient,
	prompter Prompter,
	ignoreList *MissingIgnoreList,
) *MissingChecker {
	return &MissingChecker{
		cfg:        cfg,
		cache:      cache,
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
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		folder := entry.Name()
		showID, ok := mc.cache.Get(folder)
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

		// Scan local files for episodes already present.
		present, err := scanPresentEpisodes(showDir)
		if err != nil {
			fmt.Printf("Warning: could not scan %q: %v\n", folder, err)
			continue
		}

		showName := details.Name
		if showName == "" {
			showName = folder
		}

		var missing []missingItem

		for _, s := range details.Seasons {
			if mc.ignoreList.IsSeasonIgnored(showID, s.SeasonNumber) {
				continue
			}

			// Fetch the full episode list for this season.
			sr, err := mc.tmdb.GetSeason(showID, s.SeasonNumber)
			if err != nil {
				fmt.Printf("Warning: could not fetch season %d for %q: %v\n", s.SeasonNumber, folder, err)
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

			// Find which aired episodes are absent locally and not ignored.
			var missingEps []Episode
			for _, ep := range aired {
				key := fmt.Sprintf("S%02dE%02d", s.SeasonNumber, ep.EpisodeNumber)
				if !present[key] && !mc.ignoreList.IsEpisodeIgnored(showID, s.SeasonNumber, ep.EpisodeNumber) {
					missingEps = append(missingEps, ep)
				}
			}
			if len(missingEps) == 0 {
				continue
			}

			if len(missingEps) == len(aired) {
				// Every aired episode is missing — report the whole season.
				missing = append(missing, missingItem{
					kind:         "season",
					showID:       showID,
					showFolder:   folder,
					showName:     showName,
					season:       s.SeasonNumber,
					episodeCount: len(aired),
					airDate:      aired[0].AirDate,
					tmdbURL:      fmt.Sprintf("%s/%s/season/%d", tmdbWebBase, showID, s.SeasonNumber),
				})
			} else {
				// Only some episodes are missing — report individually.
				for _, ep := range missingEps {
					missing = append(missing, missingItem{
						kind:       "episode",
						showID:     showID,
						showFolder: folder,
						showName:   showName,
						season:     s.SeasonNumber,
						episode:    ep.EpisodeNumber,
						title:      ep.Name,
						airDate:    ep.AirDate,
						tmdbURL:    fmt.Sprintf("%s/%s/season/%d/episode/%d", tmdbWebBase, showID, s.SeasonNumber, ep.EpisodeNumber),
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
			"\nIgnore items? Enter numbers (e.g. \"1,3\"), \"show\" to ignore the whole show, or press Enter to skip: ",
		)
		if err != nil || strings.TrimSpace(ans) == "" {
			continue
		}

		ans = strings.TrimSpace(ans)
		if ans == "show" {
			mc.ignoreList.AddShow(showID)
			fmt.Printf("Added %q to the missing-episode ignore list.\n", showName)
		} else {
			for _, part := range strings.Split(ans, ",") {
				part = strings.TrimSpace(part)
				n, err := strconv.Atoi(part)
				if err != nil || n < 1 || n > len(missing) {
					fmt.Printf("  Skipping unrecognised entry %q\n", part)
					continue
				}
				item := missing[n-1]
				if item.kind == "season" {
					mc.ignoreList.AddSeason(showID, item.season)
					fmt.Printf("  Ignoring Season %d of %s\n", item.season, showName)
				} else {
					mc.ignoreList.AddEpisode(showID, item.season, item.episode)
					fmt.Printf("  Ignoring S%02dE%02d of %s\n", item.season, item.episode, showName)
				}
			}
		}

		if err := mc.ignoreList.Save(); err != nil {
			fmt.Printf("Warning: could not save ignore list: %v\n", err)
		}
	}

	if !anyFound {
		fmt.Println("No missing episodes found.")
	}
	fmt.Println("\nMissing episode check complete.")
	return nil
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
		// Multi-episode file: expand each episode number individually.
		if m := multiEpRe.FindStringSubmatch(name); m != nil {
			season := strings.ToUpper(m[1]) // e.g. "S01"
			for _, em := range eNumRe.FindAllStringSubmatch(m[2], -1) {
				epNum, _ := strconv.Atoi(em[1])
				present[fmt.Sprintf("%sE%02d", season, epNum)] = true
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
