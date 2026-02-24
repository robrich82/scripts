package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const tmdbImageBase = "https://image.tmdb.org/t/p/original"
const mkvpropeditSuccessMsg = "The changes are written to the file. Done."

// Config holds all runtime configuration for a processing run.
type Config struct {
	MediaDir   string
	CutoffDate time.Time
	ProcessAll bool
}

// Stats counts outcomes across an entire processing run.
type Stats struct {
	Success    int
	Failed     int
	SkippedOld int
}

// Processor orchestrates the scan loop, TMDB lookups, and MKV metadata updates.
type Processor struct {
	cfg        Config
	cache      *ShowCache
	manualData map[string]ManualEntry
	tmdb       TMDBClient
	mkv        MKVRunner
	prompter   Prompter
	timer      FileTimer

	sessionSkipped   map[string]bool
	sessionConfirmed map[string]string
}

// NewProcessor constructs a Processor with the given dependencies.
func NewProcessor(
	cfg Config,
	cache *ShowCache,
	manualData map[string]ManualEntry,
	tmdb TMDBClient,
	mkv MKVRunner,
	prompter Prompter,
	timer FileTimer,
) *Processor {
	return &Processor{
		cfg:              cfg,
		cache:            cache,
		manualData:       manualData,
		tmdb:             tmdb,
		mkv:              mkv,
		prompter:         prompter,
		timer:            timer,
		sessionSkipped:   make(map[string]bool),
		sessionConfirmed: make(map[string]string),
	}
}

var (
	multiEpRe = regexp.MustCompile(`([Ss]\d+)((?:[Ee]\d+){2,})`)
	eNumRe    = regexp.MustCompile(`[Ee](\d+)`)
	epRe      = regexp.MustCompile(`(?i)S(\d+)E(\d+)`)
)

// Run scans MediaDir and applies TMDB metadata to every qualifying MKV file.
func (p *Processor) Run() Stats {
	var stats Stats

	entries, err := os.ReadDir(p.cfg.MediaDir)
	if err != nil {
		fmt.Printf("Error reading directory: %v\n", err)
		return stats
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		showFolder := entry.Name()
		showPath := filepath.Join(p.cfg.MediaDir, showFolder)

		showID := p.resolveShowID(showFolder)
		if showID == "SKIP" {
			continue
		}

		// Fetch the show's main poster path as fallback for seasons without one.
		mainPoster := ""
		if details, err := p.tmdb.GetShow(showID); err == nil && details.PosterPath != "" {
			mainPoster = tmdbImageBase + details.PosterPath
		}

		// seasonCache is reset for each directory in the walk (matching Python behaviour).
		seasonCache := map[string]SeasonResult{}
		currentDir := ""

		filepath.WalkDir(showPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
				return nil
			}

			// Reset season cache when entering a new subdirectory.
			dir := filepath.Dir(path)
			if dir != currentDir {
				seasonCache = map[string]SeasonResult{}
				currentDir = dir
			}

			info, err := d.Info()
			if err != nil || info.ModTime().Before(p.cfg.CutoffDate) {
				if err == nil {
					stats.SkippedOld++
				}
				return nil
			}

			file, filePath := p.normalizeFilename(dir, d.Name())

			epMatch := epRe.FindStringSubmatch(file)
			if epMatch == nil {
				return nil
			}
			sNum, _ := strconv.Atoi(epMatch[1])
			eNum, _ := strconv.Atoi(epMatch[2])

			currID, tmdbSeason, tmdbEpisode := p.resolveEpisode(showFolder, showID, sNum, eNum)

			// Fetch season data (cached per directory run).
			var relDate, posterURL, summary string
			posterURL = mainPoster
			cKey := fmt.Sprintf("%s_S%d", currID, tmdbSeason)
			if _, ok := seasonCache[cKey]; !ok {
				if sr, err := p.tmdb.GetSeason(currID, tmdbSeason); err == nil {
					seasonCache[cKey] = sr
				}
			}
			if sr, ok := seasonCache[cKey]; ok {
				if sr.PosterPath != "" {
					posterURL = tmdbImageBase + sr.PosterPath
				}
				for _, ep := range sr.Episodes {
					if ep.EpisodeNumber == tmdbEpisode {
						summary = ep.Overview
						if ep.AirDate != "" {
							relDate = ep.AirDate + "T00:00:00Z"
						}
						break
					}
				}
			}

			title := cleanMetadataTitle(file)
			if p.applyMKV(filePath, title, summary, currID, relDate, posterURL) {
				fmt.Printf("[✓] Processed: %s\n", file)
				stats.Success++
			} else {
				fmt.Printf("  [X] Failed: %s\n", file)
				stats.Failed++
			}

			time.Sleep(50 * time.Millisecond)
			return nil
		})
	}

	return stats
}

// resolveShowID returns the TMDB show ID for showFolder, prompting the user if
// the show is not already cached or confirmed this session.
func (p *Processor) resolveShowID(showFolder string) string {
	if p.sessionSkipped[showFolder] {
		return "SKIP"
	}
	if id, ok := p.sessionConfirmed[showFolder]; ok {
		return id
	}
	if id, ok := p.cache.Get(showFolder); ok {
		if !p.cfg.ProcessAll {
			ans, _ := p.prompter.Ask(fmt.Sprintf("\nShow '%s' found in cache. Process? [y/n]: ", showFolder))
			if strings.ToLower(strings.TrimSpace(ans)) == "n" {
				p.sessionSkipped[showFolder] = true
				return "SKIP"
			}
		}
		p.sessionConfirmed[showFolder] = id
		return id
	}

	// New show — search TMDB interactively.
	searchQuery := cleanSearchQuery(showFolder)
	fmt.Printf("\n--- New Show Found: %s ---\n", showFolder)

	results, err := p.tmdb.SearchTV(searchQuery)
	var resID string
	if err != nil || len(results) == 0 {
		ans, _ := p.prompter.Ask("No results. Enter TMDB ID or 's' to skip: ")
		ans = strings.TrimSpace(ans)
		if strings.ToLower(ans) == "s" || ans == "" {
			p.sessionSkipped[showFolder] = true
			return "SKIP"
		}
		resID = ans
	} else {
		best := results[0]
		firstAir := best.FirstAirDate
		if firstAir == "" {
			firstAir = "N/A"
		}
		fmt.Printf("Match: %s (%s)\n", best.Name, firstAir)
		ans, _ := p.prompter.Ask("Correct? [y/n] or enter TMDB ID: ")
		ans = strings.TrimSpace(ans)
		switch strings.ToLower(ans) {
		case "y", "":
			resID = fmt.Sprintf("%d", best.ID)
		case "n", "s":
			p.sessionSkipped[showFolder] = true
			return "SKIP"
		default:
			resID = ans
		}
	}

	if resID != "" {
		p.cache.Set(showFolder, resID)
		p.sessionConfirmed[showFolder] = resID
		if err := p.cache.Save(); err != nil {
			fmt.Printf("  [!] Cache save error: %v\n", err)
		}
		return resID
	}

	p.sessionSkipped[showFolder] = true
	return "SKIP"
}

// resolveEpisode applies manual overrides/mappings and returns the effective
// TMDB show ID, season, and episode numbers to use for lookups.
func (p *Processor) resolveEpisode(showFolder, showID string, sNum, eNum int) (id string, tmdbSeason, tmdbEpisode int) {
	id, tmdbSeason, tmdbEpisode = showID, sNum, eNum

	entry, ok := p.manualData[showFolder]
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

// normalizeFilename renames Plex multi-episode files that use three or more
// episode markers (e.g. S01E01E02E03 → S01E01-E03). Returns the effective
// filename and absolute path after any rename.
func (p *Processor) normalizeFilename(dir, file string) (string, string) {
	m := multiEpRe.FindStringSubmatch(file)
	if m == nil {
		return file, filepath.Join(dir, file)
	}

	eNums := eNumRe.FindAllStringSubmatch(m[2], -1)
	if len(eNums) <= 2 {
		return file, filepath.Join(dir, file)
	}

	first := eNums[0][1]
	last := eNums[len(eNums)-1][1]
	newFile := strings.Replace(file, m[2], fmt.Sprintf("E%s-E%s", first, last), 1)
	newPath := filepath.Join(dir, newFile)
	if err := os.Rename(filepath.Join(dir, file), newPath); err != nil {
		// If rename fails, continue with the original name.
		return file, filepath.Join(dir, file)
	}
	return newFile, newPath
}

// applyMKV writes TMDB metadata into an MKV file using mkvpropedit, downloads
// the poster as a cover attachment, and sets filesystem timestamps to the
// episode air date.
func (p *Processor) applyMKV(filePath, title, summary, tmdbID, dateStr, posterURL string) bool {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return false
	}

	args := []string{"--edit", "info", "--set", "title=" + title}
	if dateStr != "" {
		args = append(args, "--set", "date="+dateStr)
	}

	// Write tags XML to a temp file.
	var tagsPath string
	if xml := buildTagXML(title, summary, tmdbID, dateStr); xml != "" {
		var err error
		tagsPath, err = p.mkv.WriteTempTags(xml)
		if err == nil {
			args = append(args, "--tags", "all:"+tagsPath)
		}
	}

	// Delete existing image attachments before adding the new poster.
	attachInfo, _ := p.mkv.ProbeAttachments(filePath)
	if attachInfo.HasJPEG {
		args = append(args, "--delete-attachment", "mime-type:image/jpeg")
	}
	if attachInfo.HasPNG {
		args = append(args, "--delete-attachment", "mime-type:image/png")
	}

	// Download poster and queue it for attachment.
	var posterPath string
	if posterURL != "" {
		if path, ok := p.mkv.DownloadPoster(posterURL); ok {
			posterPath = path
			args = append(args,
				"--attachment-name", "cover.jpg",
				"--attachment-mime-type", "image/jpeg",
				"--add-attachment", posterPath,
			)
		}
	}

	combined, rc, err := p.mkv.Apply(filePath, args)
	p.mkv.Cleanup(tagsPath, posterPath)

	if err != nil {
		fmt.Printf("  [!] subprocess error: %v\n", err)
		return false
	}

	success := rc == 0 || strings.Contains(combined, mkvpropeditSuccessMsg)
	if !success {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
	} else if rc != 0 {
		fmt.Printf("  [i] mkvpropedit returned rc=%d but reported success.\n%s\n", rc, combined)
	}

	// Update filesystem timestamps to the episode air date.
	if success && dateStr != "" {
		datePart := strings.SplitN(dateStr, "T", 2)[0]
		if dt, err := time.Parse("2006-01-02", datePart); err == nil {
			if err := p.timer.SetTimes(filePath, dt); err != nil {
				fmt.Printf("  [!] Timestamp error: %v\n", err)
			}
			if err := p.timer.SetCreationTime(filePath, dt); err != nil {
				fmt.Printf("  [!] Timestamp error: %v\n", err)
			}
		}
	}

	return success
}
