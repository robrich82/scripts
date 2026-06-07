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
	MediaDir          string
	CutoffDate        time.Time
	ProcessAll        bool
	RatingsOnly       bool // skip all standard MKV processing; only update ratings
	OverwriteRatings  bool // ignore cached ratings and re-fetch/re-prompt
	OverwriteLanguage bool // re-apply Plex audio language even if already set for a show
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
	retryList  *RetryList
	titleCache *TitleCache

	// Rating pipeline — all optional (nil disables the feature).
	ratingCache   *RatingCache
	ratingSources []RatingSource
	plex          PlexClient

	sessionSkipped        map[string]bool
	sessionConfirmed      map[string]string
	sessionPromptedRating map[string]NZRating // tmdbID → rating from user prompt this run
	sessionPlexKey        map[string]string   // tmdbID → Plex show ratingKey (cached across methods)
	sessionOriginalLang   map[string]string   // tmdbID → original_language from TMDB (ISO 639-1)
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
	retryList *RetryList,
	ratingCache *RatingCache,
	ratingSources []RatingSource,
	plex PlexClient,
	titleCache *TitleCache,
) *Processor {
	return &Processor{
		cfg:                   cfg,
		cache:                 cache,
		manualData:            manualData,
		tmdb:                  tmdb,
		mkv:                   mkv,
		prompter:              prompter,
		timer:                 timer,
		retryList:             retryList,
		ratingCache:           ratingCache,
		ratingSources:         ratingSources,
		plex:                  plex,
		titleCache:            titleCache,
		sessionSkipped:        make(map[string]bool),
		sessionConfirmed:      make(map[string]string),
		sessionPromptedRating: make(map[string]NZRating),
		sessionPlexKey:        make(map[string]string),
		sessionOriginalLang:   make(map[string]string),
	}
}

var (
	multiEpRe = regexp.MustCompile(`([Ss]\d+)((?:[Ee]\d+){2,})`)
	eNumRe    = regexp.MustCompile(`[Ee](\d+)`)
	epRe      = regexp.MustCompile(`(?i)S(\d+)E(\d+)`)
	// epRangeRe matches Plex-normalised multi-episode range filenames, e.g. S01E01-E03.
	epRangeRe = regexp.MustCompile(`(?i)S(\d+)E(\d+)-E(\d+)`)
)

// Run scans MediaDir and applies TMDB metadata to every qualifying MKV file.
// Phase 1 does a single WalkDir over MediaDir to collect qualifying paths grouped
// by show folder. Phase 2 iterates only the shows that have qualifying files,
// avoiding TMDB calls for shows whose content is entirely before the cutoff date.
func (p *Processor) Run() Stats {
	var stats Stats

	// Phase 1: single walk over the entire media directory.
	// Qualifying paths are grouped by show folder in encounter order.
	showFiles := map[string][]string{}
	var showOrder []string

	filepath.WalkDir(p.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}

		rel, err := filepath.Rel(p.cfg.MediaDir, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) < 2 {
			// File is directly under MediaDir with no show subfolder — skip.
			return nil
		}
		showFolder := parts[0]

		// In --ratings-only mode collect every MKV regardless of modification date.
		if !p.cfg.RatingsOnly {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.ModTime().Before(p.cfg.CutoffDate) && !p.retryList.Has(path) {
				stats.SkippedOld++
				return nil
			}
		}

		if _, seen := showFiles[showFolder]; !seen {
			showOrder = append(showOrder, showFolder)
		}
		showFiles[showFolder] = append(showFiles[showFolder], path)
		return nil
	})

	// Phase 2: process each show that has at least one qualifying file.
	extrasProcessed := false
	for _, showFolder := range showOrder {
		showID := p.resolveShowID(showFolder)
		if showID == "SKIP" {
			continue
		}

		// The canonical show name used for rating lookups.
		showName := cleanSearchQuery(showFolder)

		// Fetch the show's main poster path and first air date as fallbacks.
		// In --ratings-only mode we skip the expensive TMDB calls.
		mainPoster := ""
		showFirstAirDate := ""
		if !p.cfg.RatingsOnly {
			if details, err := p.tmdb.GetShow(showID); err == nil {
				if details.PosterPath != "" {
					mainPoster = tmdbImageBase + details.PosterPath
				}
				showFirstAirDate = details.FirstAirDate
				if details.OriginalLanguage != "" {
					p.sessionOriginalLang[showID] = details.OriginalLanguage
				}
			}
		}

		// seasonCache is reset per show and again whenever the directory changes.
		seasonCache := map[string]SeasonResult{}
		currentDir := ""
		showTouched := false

		for _, path := range showFiles[showFolder] {
			file := filepath.Base(path)
			dir := filepath.Dir(path)

			// Reset season cache when entering a new subdirectory.
			if dir != currentDir {
				seasonCache = map[string]SeasonResult{}
				currentDir = dir
			}

			file, filePath := p.normalizeFilename(dir, file)
			stem := strings.TrimSuffix(file, filepath.Ext(file))

			// Check extras map first — an explicit entry overrides the S##E## episode
			// regex, so that NCOP/NCED files whose name includes an episode number are
			// processed as extras rather than as regular episodes.
			if !p.cfg.RatingsOnly {
				if entry, hasEntry := p.manualData[showFolder]; hasEntry {
					if extra, hasExtra := entry.Extras[stem]; hasExtra {
						var relDate, stampDate, posterURL, summary, title string
						posterURL = mainPoster

						// Detect OP/ED/NCOP/NCED suffix in the filename stem.
						_, opEdSuffix := extractOpEdSuffix(stem)

						// Resolve effective poster season and episode reference.
						posterSeason := extra.Season
						epSeason, epNum := 0, 0
						if s, e, ok := parseEpisodeRef(extra.Episode); ok {
							epSeason, epNum = s, e
							if posterSeason == 0 {
								posterSeason = epSeason
							}
						}

						// Fetch season data for poster.
						if posterSeason != 0 {
							cKey := fmt.Sprintf("%s_S%d", showID, posterSeason)
							if _, ok := seasonCache[cKey]; !ok {
								if sr, err := p.tmdb.GetSeason(showID, posterSeason); err == nil {
									seasonCache[cKey] = sr
								}
							}
							if sr, ok := seasonCache[cKey]; ok && sr.PosterPath != "" {
								posterURL = tmdbImageBase + sr.PosterPath
							}
						}

						// Fetch episode metadata when an episode reference is specified.
						if epNum > 0 {
							cKey := fmt.Sprintf("%s_S%d", showID, epSeason)
							if _, ok := seasonCache[cKey]; !ok {
								if sr, err := p.tmdb.GetSeason(showID, epSeason); err == nil {
									seasonCache[cKey] = sr
								}
							}
							if sr, ok := seasonCache[cKey]; ok {
								for _, ep := range sr.Episodes {
									if ep.EpisodeNumber == epNum {
										if ep.AirDate != "" {
											relDate = ep.AirDate + "T00:00:00Z"
											stampDate = relDate
										}
										summary = ep.Overview
										break
									}
								}
							}
						}

						// Manual date takes priority over episode air date.
						if extra.Date != "" {
							relDate = extra.Date + "T00:00:00Z"
							stampDate = relDate
						}

						// Build title from the filename, matching the format regular episodes use.
						// When an OP/ED suffix is detected, strip it from the base and
						// re-append in parentheses so the separator is normalised.
						if opEdSuffix != "" {
							base, _ := extractOpEdSuffix(stem)
							title = fmt.Sprintf("%s (%s)", cleanMetadataTitle(base), opEdSuffix)
						} else {
							title = cleanMetadataTitle(file)
						}

						rating := p.resolveEpisodeRating(showID, showName, filePath, posterSeason, 0)
						deband := entry.resolveDeband(fmt.Sprintf("S%02d", posterSeason), "")
						if p.applyMKV(filePath, title, summary, showID, relDate, stampDate, posterURL, rating, deband) {
							if stampDate != "" {
								p.retryList.Remove(filePath)
							} else {
								p.retryList.Add(filePath)
							}
							showTouched = true
							extrasProcessed = true
							fmt.Printf("[✓] Processed (extra): %s\n", file)
							stats.Success++
						} else {
							fmt.Printf("  [X] Failed (extra): %s\n", file)
							stats.Failed++
						}
						time.Sleep(50 * time.Millisecond)
						continue
					}
				}
			}

			epMatch := epRe.FindStringSubmatch(file)
			if epMatch == nil {
				continue
			}
			sNum, _ := strconv.Atoi(epMatch[1])
			eNum, _ := strconv.Atoi(epMatch[2])

			currID, tmdbSeason, tmdbEpisode := p.resolveEpisode(showFolder, showID, sNum, eNum)

			// Resolve NZ content rating for this episode.
			rating := p.resolveEpisodeRating(currID, showName, filePath, tmdbSeason, tmdbEpisode)

			if p.cfg.RatingsOnly {
				// Ratings-only mode: only write LAW_RATING to MKV, skip all other processing.
				if p.applyRatingOnlyMKV(filePath, rating) {
					showTouched = true
					fmt.Printf("[✓] Rating updated: %s\n", file)
					stats.Success++
				} else {
					fmt.Printf("  [X] Rating update failed: %s\n", file)
					stats.Failed++
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}

			// Full processing mode: fetch season data and apply all metadata.
			var relDate, stampDate, posterURL, summary string
			posterURL = mainPoster
			cKey := fmt.Sprintf("%s_S%d", currID, tmdbSeason)
			if _, ok := seasonCache[cKey]; !ok {
				if sr, err := p.tmdb.GetSeason(currID, tmdbSeason); err == nil {
					seasonCache[cKey] = sr
				}
			}
			var prevDate, srAirDate string
			if sr, ok := seasonCache[cKey]; ok {
				if sr.PosterPath != "" {
					posterURL = tmdbImageBase + sr.PosterPath
				}
				for _, ep := range sr.Episodes {
					if ep.EpisodeNumber == tmdbEpisode {
						summary = ep.Overview
						if ep.AirDate != "" {
							relDate = ep.AirDate + "T00:00:00Z"
							stampDate = relDate // real date: safe to stamp filesystem
						}
						break
					}
					if ep.AirDate != "" {
						prevDate = ep.AirDate
					}
				}
				srAirDate = sr.AirDate
			}

			// Manual date overrides all TMDB-derived dates.
			epKey := fmt.Sprintf("S%02dE%02d", sNum, eNum)
			if entry, ok := p.manualData[showFolder]; ok {
				if dateStr, ok := entry.Dates[epKey]; ok && dateStr != "" {
					relDate = dateStr + "T00:00:00Z"
					stampDate = relDate
				}
			}

			// Fall back to placeholder dates only when no TMDB or manual date is available.
			if relDate == "" {
				if prevDate != "" {
					relDate = prevDate + "T00:00:00Z"
					fmt.Printf("  [i] No air date for episode; using previous episode date as placeholder: %s\n", prevDate)
				} else if srAirDate != "" {
					relDate = srAirDate + "T00:00:00Z"
					fmt.Printf("  [i] No air date for episode; using season air date as placeholder: %s\n", srAirDate)
				} else if showFirstAirDate != "" {
					relDate = showFirstAirDate + "T00:00:00Z"
					fmt.Printf("  [i] No air date for episode; using show first air date as placeholder: %s\n", showFirstAirDate)
				}
			}

			title := cleanMetadataTitle(file)
			if cached, ok := p.titleCache.Get(filePath); ok {
				title = cached
			}
			var deband bool
			if entry, ok := p.manualData[showFolder]; ok {
				deband = entry.resolveDeband(
					fmt.Sprintf("S%02d", sNum),
					fmt.Sprintf("S%02dE%02d", sNum, eNum),
				)
			}
			if p.applyMKV(filePath, title, summary, currID, relDate, stampDate, posterURL, rating, deband) {
				if stampDate != "" {
					p.retryList.Remove(filePath)
				} else {
					p.retryList.Add(filePath)
				}
				showTouched = true
				fmt.Printf("[✓] Processed: %s\n", file)
				stats.Success++
			} else {
				fmt.Printf("  [X] Failed: %s\n", file)
				stats.Failed++
			}

			time.Sleep(50 * time.Millisecond)
		}

		// After processing all episodes for this show, save rating cache and update Plex.
		if p.ratingCache != nil {
			if err := p.ratingCache.Save(); err != nil {
				fmt.Printf("[!] Failed to save rating cache: %v\n", err)
			}
		}
		langOverride := p.manualData[showFolder].Language
		p.applyPlexRatings(showID, showName, showTouched, langOverride)
	}

	if err := p.retryList.Save(); err != nil {
		fmt.Printf("[!] Failed to save retry list: %v\n", err)
	}

	// After all shows are processed, trigger a full library scan when extras were
	// processed. The startup RefreshLibraries fires before tagging, so extras get
	// untagged metadata on first discovery; this post-run scan picks up the tags.
	if extrasProcessed && p.plex != nil {
		if err := p.plex.RefreshLibraries(); err != nil {
			fmt.Printf("[!] Plex library refresh failed: %v\n", err)
		} else {
			fmt.Println("  [i] Plex: library re-scanned for extras.")
		}
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
	// Use the absolute folder path as the cache key so --clean can verify
	// existence via os.Stat without needing to know the media root.
	cacheKey := filepath.Join(p.cfg.MediaDir, showFolder)
	if id, ok := p.cache.Get(cacheKey); ok {
		if !p.cfg.ProcessAll {
			ans, _ := p.prompter.Ask(fmt.Sprintf("\nShow '%s' found in cache. Process? [y/n]: ", showFolder))
			if strings.ToLower(strings.TrimSpace(ans)) != "y" {
				p.sessionSkipped[showFolder] = true
				return "SKIP"
			}
		}
		p.sessionConfirmed[showFolder] = id
		return id
	}
	// Legacy cache entries use the base folder name; migrate to absolute path on access.
	if id, ok := p.cache.Get(showFolder); ok {
		p.cache.Set(cacheKey, id)
		if !p.cfg.ProcessAll {
			ans, _ := p.prompter.Ask(fmt.Sprintf("\nShow '%s' found in cache. Process? [y/n]: ", showFolder))
			if strings.ToLower(strings.TrimSpace(ans)) != "y" {
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
		p.cache.Set(cacheKey, resID)
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
	if pair, ok := entry.Mapping[epKey]; ok && len(pair) >= 2 {
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
// episode air date. dateStr is written to MKV metadata; stampDate (which may
// differ or be empty) is used for filesystem timestamps.
func (p *Processor) applyMKV(filePath, title, summary, tmdbID, dateStr, stampDate, posterURL string, rating NZRating, deband bool) bool {
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
	if xml := buildTagXML(summary, tmdbID, dateStr, rating, deband); xml != "" {
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
	if success && stampDate != "" {
		datePart := strings.SplitN(stampDate, "T", 2)[0]
		if dt, err := time.ParseInLocation("2006-01-02", datePart, time.Local); err == nil {
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

// resolveEpisodeRating returns the best NZ rating for a single episode.
// Cache hits are returned silently. When an external source finds a rating for
// the first time it is shown and the user is prompted to confirm or override.
// The result is cached at the show level to avoid re-prompting for subsequent
// episodes of the same show within one run.
// filePath is the absolute path to the MKV being processed; used as a
// last-resort Plex lookup when TMDB GUID and title matching both fail.
func (p *Processor) resolveEpisodeRating(tmdbID, showName, filePath string, season, episode int) NZRating {
	if p.ratingCache == nil {
		return RatingNone
	}

	// Session cache: already prompted for this show this run — reuse the answer.
	if r, alreadyPrompted := p.sessionPromptedRating[tmdbID]; alreadyPrompted {
		return r
	}

	// Cache hits: return silently without prompting.
	if !p.cfg.OverwriteRatings {
		if r, ok := p.ratingCache.GetEpisodeRating(tmdbID, season, episode); ok {
			return r
		}
		if r, ok := p.ratingCache.GetShowRating(tmdbID); ok {
			return r
		}
	}

	// Try external sources. If a source finds a rating it is stored as a
	// suggestion and shown to the user for confirmation rather than applied
	// silently (the original silent-apply behaviour caused new shows to skip
	// the prompt entirely when a source happened to have data).
	var suggested NZRating
	for _, src := range p.ratingSources {
		r, err := src.FetchEpisodeRating(tmdbID, showName, season, episode)
		if err != nil {
			fmt.Printf("  [!] %s episode rating error: %v\n", src.Name(), err)
			continue
		}
		if r != RatingNone {
			suggested = r
			break
		}
	}
	if suggested == RatingNone {
		for _, src := range p.ratingSources {
			r, err := src.FetchShowRating(tmdbID, showName)
			if err != nil {
				fmt.Printf("  [!] %s show rating error: %v\n", src.Name(), err)
				continue
			}
			if r != RatingNone {
				suggested = r
				break
			}
		}
	}

	// Cache fallback when --overwrite-ratings skipped the cache checks above.
	if suggested == RatingNone && p.cfg.OverwriteRatings {
		if r, ok := p.ratingCache.GetEpisodeRating(tmdbID, season, episode); ok && r != RatingNone {
			suggested = r
		}
		if suggested == RatingNone {
			if r, ok := p.ratingCache.GetShowRating(tmdbID); ok && r != RatingNone {
				suggested = r
			}
		}
	}

	// Check Plex for an existing rating (shown when no source has data).
	// Try TMDB-ID lookup first, then title match, then file-path lookup (most
	// reliable for anime whose Plex title differs from the folder name).
	var plexDefault NZRating
	if p.plex != nil {
		showKey, err := p.plex.FindShowByTMDBID(tmdbID)
		if err != nil {
			if err != ErrPlexNotFound {
				fmt.Printf("  [i] Plex: TMDB lookup error for %q: %v\n", showName, err)
			}
			var titleErr error
			showKey, titleErr = p.plex.FindShowByTitle(showName)
			if titleErr != nil && titleErr != ErrPlexNotFound {
				fmt.Printf("  [i] Plex: title lookup error for %q: %v\n", showName, titleErr)
			}
		}
		if showKey == "" && filePath != "" {
			var pathErr error
			showKey, pathErr = p.plex.FindShowByFilePath(filePath)
			if pathErr != nil && pathErr != ErrPlexNotFound {
				fmt.Printf("  [i] Plex: file-path lookup error for %q: %v\n", showName, pathErr)
			}
		}
		if showKey != "" {
			if p.sessionPlexKey != nil {
				p.sessionPlexKey[tmdbID] = showKey
			}
			var fetchErr error
			plexDefault, fetchErr = p.plex.GetContentRating(showKey)
			if fetchErr != nil {
				fmt.Printf("  [i] Plex: rating fetch error for %q: %v\n", showName, fetchErr)
			} else if plexDefault == RatingNone {
				fmt.Printf("  [i] Plex: %q found but no NZ content rating is set\n", showName)
			}
		} else {
			fmt.Printf("  [i] Plex: %q not found (tried TMDB GUID, title, and file path)\n", showName)
		}
	}

	// Prompt the user once per show per run.
	// When a source found a rating, show it as the default so the user can
	// confirm with Enter or type a new value to override.
	const validRatings = "G/PG/M/13/RP13/R13/15/RP15/R15/16/RP16/R16/R/18/RP18/R18"
	var promptMsg string
	switch {
	case suggested != RatingNone:
		promptMsg = fmt.Sprintf(
			"NZ rating for %q: %s — press enter to confirm, or enter a new rating (%s): ",
			showName, suggested, validRatings,
		)
	case plexDefault != RatingNone:
		promptMsg = fmt.Sprintf(
			"No NZ rating found for %q.\nPlex currently has: %s\nEnter rating (%s) to override, or blank to use Plex's value: ",
			showName, plexDefault, validRatings,
		)
	default:
		promptMsg = fmt.Sprintf(
			"No NZ rating found for %q.\nEnter rating (%s)\nTip: check Netflix, Crunchyroll, HiDive, or classificationoffice.govt.nz\nLeave blank to skip: ",
			showName, validRatings,
		)
	}
	var r NZRating
	for {
		ans, _ := p.prompter.Ask(promptMsg)
		ans = strings.TrimSpace(ans)
		if ans == "" {
			if suggested != RatingNone {
				r = suggested
				p.ratingCache.SetShowRating(tmdbID, r)
			} else if plexDefault != RatingNone {
				r = plexDefault
				p.ratingCache.SetShowRating(tmdbID, r)
			}
			break
		}
		parsed, ok := ParseNZRating(ans)
		if ok {
			r = parsed
			p.ratingCache.SetShowRating(tmdbID, r)
			break
		}
		promptMsg = fmt.Sprintf("  %q is not a valid rating. Enter (%s) or blank to skip: ", ans, validRatings)
	}
	p.sessionPromptedRating[tmdbID] = r
	return r
}

// applyRatingOnlyMKV writes only the LAW_RATING/COUNTRY tags to an MKV file,
// skipping all other metadata (title, date, poster, timestamps).
// The original file timestamps are preserved so that downstream tools (e.g.
// mkv-tvshow in full mode) don't see every file as newly modified.
// Returns true even when rating is RatingNone (nothing to do, not a failure).
func (p *Processor) applyRatingOnlyMKV(filePath string, rating NZRating) bool {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return false
	}
	if rating == RatingNone {
		return true // nothing to write
	}

	// Skip if this file already has these tags written (avoids re-writing every run).
	if p.ratingCache != nil && !p.cfg.OverwriteRatings && p.ratingCache.IsApplied(filePath, rating) {
		return true
	}

	// Snapshot the current mtime before mkvpropedit touches it.
	origStat, statErr := os.Stat(filePath)

	// Extract existing tags so other tags (DEBAND, SUMMARY, TMDB_ID, etc.) are preserved.
	currentXML, err := p.mkv.ExtractTags(filePath)
	if err != nil {
		fmt.Printf("  [!] Failed to extract tags: %v\n", err)
		return false
	}

	tagsPath, err := p.mkv.WriteTempTags(updateRatingInXML(currentXML, rating))
	if err != nil {
		fmt.Printf("  [!] Failed to write tags XML: %v\n", err)
		return false
	}

	combined, rc, err := p.mkv.Apply(filePath, []string{"--tags", "all:" + tagsPath})
	p.mkv.Cleanup(tagsPath)

	if err != nil {
		fmt.Printf("  [!] subprocess error: %v\n", err)
		return false
	}
	success := rc == 0 || strings.Contains(combined, mkvpropeditSuccessMsg)
	if !success {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
		return false
	}

	// Restore the original timestamps so the file appears unmodified to other tools.
	if statErr == nil {
		if err := p.timer.SetTimes(filePath, origStat.ModTime()); err != nil {
			fmt.Printf("  [!] Timestamp restore error: %v\n", err)
		}
		if err := p.timer.SetCreationTime(filePath, origStat.ModTime()); err != nil {
			fmt.Printf("  [!] Timestamp restore error: %v\n", err)
		}
	}

	// Record that the tags have been written so subsequent runs can skip this file.
	if p.ratingCache != nil {
		p.ratingCache.MarkApplied(filePath, rating)
	}
	return true
}

// applyDebandOnlyMKV updates only the DEBAND tag in an MKV file, preserving
// all other existing tags and restoring the original file timestamps afterward.
// It extracts the current tag XML, splices in or out the DEBAND element, then
// writes the result back with mkvpropedit.
func (p *Processor) applyDebandOnlyMKV(filePath string, deband bool) bool {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return false
	}

	origStat, statErr := os.Stat(filePath)

	currentXML, err := p.mkv.ExtractTags(filePath)
	if err != nil {
		fmt.Printf("  [!] Failed to extract tags: %v\n", err)
		return false
	}

	updatedXML := updateDebandInXML(currentXML, deband)

	tagsPath, err := p.mkv.WriteTempTags(updatedXML)
	if err != nil {
		fmt.Printf("  [!] Failed to write tags XML: %v\n", err)
		return false
	}

	combined, rc, err := p.mkv.Apply(filePath, []string{"--tags", "all:" + tagsPath})
	p.mkv.Cleanup(tagsPath)

	if err != nil {
		fmt.Printf("  [!] subprocess error: %v\n", err)
		return false
	}
	success := rc == 0 || strings.Contains(combined, mkvpropeditSuccessMsg)
	if !success {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
		return false
	}

	if statErr == nil {
		if err := p.timer.SetTimes(filePath, origStat.ModTime()); err != nil {
			fmt.Printf("  [!] Timestamp restore error: %v\n", err)
		}
		if err := p.timer.SetCreationTime(filePath, origStat.ModTime()); err != nil {
			fmt.Printf("  [!] Timestamp restore error: %v\n", err)
		}
	}

	return true
}

// RunDebandOnly scans MediaDir and updates only the DEBAND MKV tag for files
// that have an explicit deband configuration in manual_data.json. It makes no
// TMDB API calls and preserves all other existing tags and file timestamps.
// Files with no deband configuration are skipped entirely.
func (p *Processor) RunDebandOnly() Stats {
	var stats Stats

	showFiles := map[string][]string{}
	var showOrder []string

	filepath.WalkDir(p.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		rel, err := filepath.Rel(p.cfg.MediaDir, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) < 2 {
			return nil
		}
		showFolder := parts[0]
		if _, seen := showFiles[showFolder]; !seen {
			showOrder = append(showOrder, showFolder)
		}
		showFiles[showFolder] = append(showFiles[showFolder], path)
		return nil
	})

	for _, showFolder := range showOrder {
		entry, hasEntry := p.manualData[showFolder]
		if !hasEntry {
			continue
		}

		for _, path := range showFiles[showFolder] {
			file := filepath.Base(path)
			dir := filepath.Dir(path)
			file, filePath := p.normalizeFilename(dir, file)
			stem := strings.TrimSuffix(file, filepath.Ext(file))

			// Check extras first.
			if extra, hasExtra := entry.Extras[stem]; hasExtra {
				seasonKey := fmt.Sprintf("S%02d", extra.Season)
				if !entry.hasDebandConfig(seasonKey, "") {
					continue
				}
				deband := entry.resolveDeband(seasonKey, "")
				if p.applyDebandOnlyMKV(filePath, deband) {
					fmt.Printf("[✓] Deband updated (extra): %s\n", file)
					stats.Success++
				} else {
					fmt.Printf("  [X] Deband update failed: %s\n", file)
					stats.Failed++
				}
				continue
			}

			epMatch := epRe.FindStringSubmatch(file)
			if epMatch == nil {
				continue
			}
			sNum, _ := strconv.Atoi(epMatch[1])
			eNum, _ := strconv.Atoi(epMatch[2])
			seasonKey := fmt.Sprintf("S%02d", sNum)
			episodeKey := fmt.Sprintf("S%02dE%02d", sNum, eNum)

			if !entry.hasDebandConfig(seasonKey, episodeKey) {
				continue
			}

			deband := entry.resolveDeband(seasonKey, episodeKey)
			if p.applyDebandOnlyMKV(filePath, deband) {
				fmt.Printf("[✓] Deband updated: %s\n", file)
				stats.Success++
			} else {
				fmt.Printf("  [X] Deband update failed: %s\n", file)
				stats.Failed++
			}
		}
	}

	return stats
}

// applyPlexRatings pushes cached ratings to Plex at episode, season, and show
// level. Season and show ratings are computed as the maximum (most restrictive)
// rating across their child items. Does nothing when Plex is not configured.
// showName is used as a title-match fallback when TMDB GUID lookup fails.
// langOverride, if non-empty, is used instead of TMDB original_language when
// setting the show's preferred audio language.
func (p *Processor) applyPlexRatings(tmdbID, showName string, touched bool, langOverride string) {
	if p.plex == nil || p.ratingCache == nil {
		return
	}

	// Use the showKey already discovered during rating lookup if available;
	// otherwise repeat the TMDB-ID → title fallback chain.
	showKey, ok := p.sessionPlexKey[tmdbID]
	if !ok {
		var err error
		showKey, err = p.plex.FindShowByTMDBID(tmdbID)
		if err != nil {
			if err != ErrPlexNotFound {
				fmt.Printf("  [!] Plex: FindShowByTMDBID(%s): %v\n", tmdbID, err)
			} else {
				// Title fallback for shows using non-TMDB agents (e.g. HAMA).
				var titleErr error
				showKey, titleErr = p.plex.FindShowByTitle(showName)
				if titleErr != nil && titleErr != ErrPlexNotFound {
					fmt.Printf("  [!] Plex: FindShowByTitle(%q): %v\n", showName, titleErr)
				}
			}
		}
	}
	if showKey == "" {
		return
	}
	// Persist the resolved key so --language-only can find this show without scanning files.
	p.ratingCache.SetPlexKey(tmdbID, showKey)

	seasons, err := p.plex.GetSeasons(showKey)
	if err != nil {
		fmt.Printf("  [!] Plex: GetSeasons(%s): %v\n", showKey, err)
		return
	}

	showMax := RatingNone
	for _, season := range seasons {
		episodes, err := p.plex.GetEpisodes(season.RatingKey)
		if err != nil {
			fmt.Printf("  [!] Plex: GetEpisodes(%s) season %d: %v\n", season.RatingKey, season.SeasonNumber, err)
			continue
		}

		seasonMax := RatingNone
		for _, ep := range episodes {
			r, ok := p.ratingCache.GetEpisodeRating(tmdbID, ep.SeasonNumber, ep.EpisodeNumber)
			if !ok {
				r, ok = p.ratingCache.GetShowRating(tmdbID)
			}
			if !ok || r == RatingNone {
				continue
			}
			if err := p.plex.SetContentRating(ep.RatingKey, PlexTypeEpisode, r); err != nil {
				fmt.Printf("  [!] Plex: set episode %s rating: %v\n", ep.RatingKey, err)
			}
			seasonMax = MaxRating(seasonMax, r)
		}

		if seasonMax != RatingNone {
			if err := p.plex.SetContentRating(season.RatingKey, PlexTypeSeason, seasonMax); err != nil {
				fmt.Printf("  [!] Plex: set season %s rating: %v\n", season.RatingKey, err)
			}
			showMax = MaxRating(showMax, seasonMax)
		}
	}

	if showMax != RatingNone {
		if err := p.plex.SetContentRating(showKey, PlexTypeShow, showMax); err != nil {
			fmt.Printf("  [!] Plex: set show %s rating: %v\n", showKey, err)
		}
	}

	if !p.cfg.RatingsOnly {
		p.applyPlexAudioLanguage(showKey, tmdbID, showName, langOverride)
	}

	if touched {
		if err := p.plex.RefreshMetadata(showKey); err != nil {
			fmt.Printf("  [!] Plex: refresh metadata for show %s: %v\n", showKey, err)
		} else {
			fmt.Printf("  [i] Plex: metadata refresh triggered for show %s\n", showKey)
		}
	}
}

// resolveOriginalLang returns the TMDB original_language code (ISO 639-1) for
// the given show, using the session cache to avoid redundant API calls.
func (p *Processor) resolveOriginalLang(tmdbID string) string {
	if lang, ok := p.sessionOriginalLang[tmdbID]; ok {
		return lang
	}
	if p.tmdb != nil {
		if details, err := p.tmdb.GetShow(tmdbID); err == nil && details.OriginalLanguage != "" {
			p.sessionOriginalLang[tmdbID] = details.OriginalLanguage
			return details.OriginalLanguage
		}
	}
	return ""
}

// RunLanguageOnly iterates all shows in the rating cache and applies the Plex
// audio language preference for each, without scanning any MKV files.
func (p *Processor) RunLanguageOnly() {
	if p.plex == nil {
		fmt.Fprintln(os.Stderr, "Plex not configured (PLEX_URL / PLEX_TOKEN not set)")
		return
	}

	// Build a reverse map: tmdbID → show folder name for title-fallback lookups.
	// Cache keys are absolute paths; extract the base name for display/search.
	tmdbToName := make(map[string]string)
	for folder, id := range p.cache.AllEntries() {
		tmdbToName[id] = filepath.Base(folder)
	}

	for _, tmdbID := range p.ratingCache.AllShowIDs() {
		showName := tmdbToName[tmdbID]

		showKey, _ := p.ratingCache.GetPlexKey(tmdbID)

		if showKey == "" {
			var err error
			showKey, err = p.plex.FindShowByTMDBID(tmdbID)
			if err == ErrPlexNotFound && showName != "" {
				// Strip the year suffix and Unicode lookalikes from the folder name
				// before title-matching — Plex display titles don't include years.
				showKey, err = p.plex.FindShowByTitle(cleanSearchQuery(showName))
			}
			if err != nil && err != ErrPlexNotFound {
				fmt.Printf("  [!] Plex: lookup TMDB %s: %v\n", tmdbID, err)
				continue
			}
			if showKey != "" {
				p.ratingCache.SetPlexKey(tmdbID, showKey)
			}
		}

		if showKey == "" {
			if showName != "" {
				fmt.Printf("  [!] Plex: show %q not found\n", showName)
			}
			continue
		}

		langOverride := p.manualData[showName].Language
		p.applyPlexAudioLanguage(showKey, tmdbID, showName, langOverride)
	}

	if err := p.ratingCache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save rating cache: %v\n", err)
	}
}

// applyPlexAudioLanguage sets the show's preferred audio language in Plex to
// the TMDB original language, but only once per show unless OverwriteLanguage
// is set. This lets the user override the preference inside Plex on a
// show-by-show basis without it being overwritten on subsequent runs.
// langOverride, if non-empty, takes precedence over the TMDB original_language.
func (p *Processor) applyPlexAudioLanguage(showKey, tmdbID, showName, langOverride string) {
	if p.ratingCache == nil {
		return
	}
	lang := langOverride
	if lang == "" {
		lang = p.resolveOriginalLang(tmdbID)
	}
	if lang == "" {
		return
	}
	if applied, ok := p.ratingCache.GetLanguageApplied(tmdbID); ok && applied == lang && !p.cfg.OverwriteLanguage {
		return
	}
	if err := p.plex.SetAudioLanguage(showKey, lang); err != nil {
		fmt.Printf("  [!] Plex: set audio language for %q: %v\n", showName, err)
		return
	}
	p.ratingCache.SetLanguageApplied(tmdbID, lang)
	fmt.Printf("  [i] Plex: audio language for %q set to %q\n", showName, lang)
}
