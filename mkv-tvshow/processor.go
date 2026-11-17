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
	"time"
)

const tmdbImageBase = "https://image.tmdb.org/t/p/original"

// mkvpropeditWrote reports whether mkvpropedit wrote the file. Exit code 0 is
// success, 1 means it wrote the changes but printed warnings, 2 is an error.
func mkvpropeditWrote(rc int) bool { return rc == 0 || rc == 1 }

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
	PlexErrors int // show-level Plex lookups and pushes, and the post-run library scan, that failed
	LostSaves  int // caches whose changes this run couldn't save
}

// countTagged reports a file whose tags were written: "[✓] label: file", or,
// when a later step failed, "[X] label: file (failed: …)", counted as failed.
func (s *Stats) countTagged(label, file string, failed []string) {
	if len(failed) == 0 {
		fmt.Printf("[✓] %s: %s\n", label, file)
		s.Success++
		return
	}
	fmt.Printf("  [X] %s: %s (failed: %s)\n", label, file, strings.Join(failed, ", "))
	s.Failed++
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
	retryMoved bool // a rename moved a retry-list entry, so the list needs saving
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

	// Run saves the show and rating caches through these; NewProcessor sets them.
	showSaver   *cacheSaver // nil when cache is
	ratingSaver *cacheSaver // nil when ratingCache is
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
	p := &Processor{
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
	p.showSaver = newShowSaver(cache) // --deband-only runs without one
	p.ratingSaver = newRatingSaver(ratingCache, "ratings and Plex state")
	return p
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

	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(p.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
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
			if olderThan(d, p.cfg.CutoffDate) && !p.retryList.Has(path) {
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
			if details, err := p.tmdb.GetShow(showID); err != nil {
				fmt.Printf("[!] TMDB show lookup failed for %s (show poster and first air date unavailable): %v\n", showFolder, err)
			} else {
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
		// Maps each local season number to the effective TMDB ID its rating was
		// cached under, so applyPlexRatings pushes the right rating for seasons
		// remapped to a different TMDB show via manual_data overrides.
		seasonEffectiveID := map[int]string{}

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
							sr, err := p.seasonData(seasonCache, showID, posterSeason)
							if err != nil {
								p.failTMDBLookup(&stats, filePath, file, err)
								continue
							}
							if sr.PosterPath != "" {
								posterURL = tmdbImageBase + sr.PosterPath
							}
						}

						// Fetch episode metadata when an episode reference is specified.
						if epNum > 0 {
							sr, err := p.seasonData(seasonCache, showID, epSeason)
							if err != nil {
								p.failTMDBLookup(&stats, filePath, file, err)
								continue
							}
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
						// Without a date or an episode reference, no date will ever come.
						awaitingDate := stampDate == "" && epNum > 0
						if wrote, failed := p.applyMKV(filePath, title, summary, showID, relDate, stampDate, posterURL, rating, deband); wrote {
							p.trackRetry(filePath, file, awaitingDate, failed)
							showTouched = true
							extrasProcessed = true
							stats.countTagged("Processed (extra)", file, failed)
						} else {
							fmt.Printf("  [X] Failed (extra): %s\n", file)
							stats.Failed++
							p.trackTagFailure(filePath, file, awaitingDate)
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
			seasonEffectiveID[sNum] = currID

			// Resolve NZ content rating for this episode.
			rating := p.resolveEpisodeRating(currID, showName, filePath, tmdbSeason, tmdbEpisode)

			if p.cfg.RatingsOnly {
				// Ratings-only mode: only write LAW_RATING to MKV, skip all other processing.
				if failed, ok := p.applyRatingOnlyMKV(filePath, rating); ok {
					showTouched = true
					stats.countTagged("Rating updated", file, failed)
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
			sr, err := p.seasonData(seasonCache, currID, tmdbSeason)
			if err != nil {
				p.failTMDBLookup(&stats, filePath, file, err)
				continue
			}
			var prevDate string
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
			srAirDate := sr.AirDate

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
			if wrote, failed := p.applyMKV(filePath, title, summary, currID, relDate, stampDate, posterURL, rating, deband); wrote {
				p.trackRetry(filePath, file, stampDate == "", failed)
				showTouched = true
				stats.countTagged("Processed", file, failed)
			} else {
				fmt.Printf("  [X] Failed: %s\n", file)
				stats.Failed++
				p.trackTagFailure(filePath, file, stampDate == "")
			}

			time.Sleep(50 * time.Millisecond)
		}

		// Update Plex, then save the rating cache with the Plex state that recorded.
		stats.PlexErrors += p.applyPlexRatings(showID, seasonEffectiveID, showName, showFolder, showTouched, p.manualData[showFolder])
		p.ratingSaver.save(showFolder)
	}
	for _, lost := range []bool{p.showSaver.finish(), p.ratingSaver.finish(), newRetrySaver(p.retryList).saveOnce()} {
		if lost {
			stats.LostSaves++
		}
	}

	// After all shows are processed, trigger a full library scan when extras were
	// processed. The startup RefreshLibraries fires before tagging, so extras get
	// untagged metadata on first discovery; this post-run scan picks up the tags.
	if extrasProcessed && p.plex != nil {
		if err := p.plex.RefreshLibraries(); err != nil {
			fmt.Printf("[!] Plex library refresh failed: %v\n", err)
			stats.PlexErrors++
		} else {
			fmt.Println("  [i] Plex: library re-scanned for extras.")
		}
	}

	return stats
}

// olderThan reports whether d was last modified before cutoff. A file whose
// info can't be read counts as new, so it is processed rather than dropped.
func olderThan(d fs.DirEntry, cutoff time.Time) bool {
	info, err := d.Info()
	return err == nil && info.ModTime().Before(cutoff)
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
		p.showSaver.save(showFolder)
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
	if p.retryList != nil && p.retryList.Move(filepath.Join(dir, file), newPath) {
		p.retryMoved = true
	}
	return newFile, newPath
}

// applyMKV writes TMDB metadata into an MKV file using mkvpropedit, downloads
// the poster as a cover attachment, and sets filesystem timestamps to the
// episode air date. dateStr is written to MKV metadata; stampDate (which may
// differ or be empty) is used for filesystem timestamps. wrote is whether
// mkvpropedit wrote the file; failed names the later steps that didn't
// complete ("poster", "timestamps", "mtime", "ctime"). Errors are printed.
func (p *Processor) applyMKV(filePath, title, summary, tmdbID, dateStr, stampDate, posterURL string, rating NZRating, deband bool) (wrote bool, failed []string) {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return false, nil
	}

	args := []string{"--edit", "info", "--set", "title=" + title}
	if dateStr != "" {
		args = append(args, "--set", "date="+dateStr)
	}

	// Write the tags, merged into the file's existing ones, to a temp file.
	var tagsPath string
	if xml := buildTagXML(summary, tmdbID, dateStr, rating, deband); xml != "" {
		existing, err := p.mkv.ExtractTags(filePath)
		if err != nil {
			fmt.Printf("  [!] Failed to extract tags: %v\n", err)
			return false, nil
		}
		if xml, err = mergeEpisodeTags(existing, xml); err != nil {
			fmt.Printf("  [!] Failed to merge tags: %v\n", err)
			return false, nil
		}
		if tagsPath, err = p.mkv.WriteTempTags(xml); err != nil {
			fmt.Printf("  [!] Failed to write tags XML: %v\n", err)
			return false, nil
		}
		args = append(args, "--tags", "all:"+tagsPath)
	}

	// Replace existing image attachments only once a new poster has downloaded,
	// so a missing poster or failed download leaves the old cover in place.
	var posterPath string
	if posterURL != "" {
		if path, err := p.mkv.DownloadPoster(posterURL); err != nil {
			fmt.Printf("  [!] Poster download failed; cover unchanged: %v\n", err)
			failed = append(failed, "poster")
		} else {
			posterPath = path
			attachInfo, _ := p.mkv.ProbeAttachments(filePath)
			if attachInfo.HasJPEG {
				args = append(args, "--delete-attachment", "mime-type:image/jpeg")
			}
			if attachInfo.HasPNG {
				args = append(args, "--delete-attachment", "mime-type:image/png")
			}
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
		return false, nil
	}

	if !mkvpropeditWrote(rc) {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
		return false, nil
	}
	if rc != 0 {
		fmt.Printf("  [i] mkvpropedit returned rc=%d but reported success.\n%s\n", rc, combined)
	}

	// Record the rating now in the tags (the merge drops an old one when this
	// is RatingNone), so --ratings-only skips the file only when it matches.
	if p.ratingCache != nil {
		p.ratingCache.MarkApplied(filePath, rating)
	}

	// Update filesystem timestamps to the episode air date.
	if stampDate != "" {
		datePart := strings.SplitN(stampDate, "T", 2)[0]
		dt, err := time.ParseInLocation("2006-01-02", datePart, time.Local)
		if err != nil {
			fmt.Printf("  [!] Air date %q can't be read; timestamps not set: %v\n", stampDate, err)
			return true, append(failed, "timestamps")
		}
		failed = append(failed, setFileTimes(p.timer, filePath, dt)...)
	}
	return true, failed
}

// setFileTimes sets filePath's modified and creation times to t through timer,
// printing any error, and names the ones that failed ("mtime", "ctime").
func setFileTimes(timer FileTimer, filePath string, t time.Time) (failed []string) {
	if err := timer.SetTimes(filePath, t); err != nil {
		fmt.Printf("  [!] Setting the modified time failed: %v\n", err)
		failed = append(failed, "mtime")
	}
	if err := timer.SetCreationTime(filePath, t); err != nil {
		fmt.Printf("  [!] Setting the creation time failed: %v\n", err)
		failed = append(failed, "ctime")
	}
	return failed
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

	cached, haveCached := p.ratingCache.GetEpisodeRating(tmdbID, season, episode)
	if !haveCached {
		cached, haveCached = p.ratingCache.GetShowRating(tmdbID)
	}
	// Cache hits: return silently without prompting. --overwrite-ratings
	// re-fetches instead, and falls back to the cached value below.
	if haveCached && !p.cfg.OverwriteRatings {
		return cached
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

	// --overwrite-ratings: no source has data, so keep the cached rating rather
	// than prompting, as mkv-movie does.
	if suggested == RatingNone && haveCached && cached != RatingNone {
		fmt.Printf("  [i] No source has a rating for %q; keeping cached %s\n", showName, cached)
		p.sessionPromptedRating[tmdbID] = cached
		return cached
	}

	plexDefault := p.plexRatingDefault(tmdbID, showName, filePath)

	// Prompt the user once per show per run, offering a source's rating, or
	// else Plex's, as the default.
	var header, blankDoes string
	def := suggested
	switch {
	case suggested != RatingNone:
		header = fmt.Sprintf("NZ rating for %q: %s", showName, suggested)
		blankDoes = "accept " + string(suggested)
	case plexDefault != RatingNone:
		header = fmt.Sprintf("No NZ rating found for %q. Plex currently has: %s", showName, plexDefault)
		def, blankDoes = plexDefault, "use Plex's "+string(plexDefault)
	default:
		header = fmt.Sprintf("No NZ rating found for %q.\nTip: check Netflix, Crunchyroll, HiDive, or classificationoffice.govt.nz", showName)
		blankDoes = "skip"
	}
	r := askRating(p.prompter.Ask, header, def, blankDoes)
	if r != RatingNone {
		p.ratingCache.SetShowRating(tmdbID, r)
	}
	p.sessionPromptedRating[tmdbID] = r
	return r
}

// plexRatingDefault returns the rating Plex already has for the show, offered
// as the prompt's default. It tries the TMDB GUID, then the title, then the
// file path (most reliable for anime whose Plex title differs from the
// folder name). It moves to the next lookup only when the previous one found
// nothing; a failed lookup is reported and gives no default.
func (p *Processor) plexRatingDefault(tmdbID, showName, filePath string) NZRating {
	if p.plex == nil {
		return RatingNone
	}
	type lookup struct {
		name string
		find func() (string, error)
	}
	lookups := []lookup{
		{"TMDB GUID", func() (string, error) { return p.plex.FindShowByTMDBID(tmdbID) }},
		{"title", func() (string, error) { return p.plex.FindShowByTitle(showName) }},
	}
	if filePath != "" {
		lookups = append(lookups, lookup{"file path", func() (string, error) { return p.plex.FindShowByFilePath(filePath) }})
	}
	var showKey string
	for _, l := range lookups {
		key, err := l.find()
		if err == nil && key != "" {
			showKey = key
			break
		}
		if err != nil && !errors.Is(err, ErrPlexNotFound) {
			fmt.Printf("  [!] Plex: %s lookup failed for %q: %v\n", l.name, showName, err)
			return RatingNone
		}
	}
	if showKey == "" {
		fmt.Printf("  [i] Plex: %q not found (tried TMDB GUID, title, and file path)\n", showName)
		return RatingNone
	}
	if p.sessionPlexKey != nil {
		p.sessionPlexKey[tmdbID] = showKey
	}
	r, err := p.plex.GetContentRating(showKey)
	if err != nil {
		fmt.Printf("  [!] Plex: rating fetch failed for %q: %v\n", showName, err)
		return RatingNone
	}
	if r == RatingNone {
		fmt.Printf("  [i] Plex: %q found but no NZ content rating is set\n", showName)
	}
	return r
}

// applyRatingOnlyMKV writes only the LAW_RATING/COUNTRY tags to an MKV file,
// skipping all other metadata (title, date, poster, timestamps).
// The original file timestamps are preserved so that downstream tools (e.g.
// mkv-tvshow in full mode) don't see every file as newly modified.
// ok is true even when rating is RatingNone (nothing to do, not a failure);
// failed is as rewriteTags'.
func (p *Processor) applyRatingOnlyMKV(filePath string, rating NZRating) (failed []string, ok bool) {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return nil, false
	}
	if rating == RatingNone {
		return nil, true // nothing to write
	}

	// Skip if this file already has these tags written (avoids re-writing every run).
	if p.ratingCache != nil && !p.cfg.OverwriteRatings && p.ratingCache.IsApplied(filePath, rating) {
		return nil, true
	}

	failed, ok = p.rewriteTags(filePath, func(xml string) (string, error) {
		return updateRatingInXML(xml, rating)
	})
	// Record that the tags have been written so subsequent runs can skip this file.
	if ok && p.ratingCache != nil {
		p.ratingCache.MarkApplied(filePath, rating)
	}
	return failed, ok
}

// applyDebandOnlyMKV updates only the DEBAND tag in an MKV file, preserving
// all other existing tags and restoring the original file timestamps afterward.
// failed and ok are as rewriteTags'.
func (p *Processor) applyDebandOnlyMKV(filePath string, deband bool) (failed []string, ok bool) {
	if !p.mkv.Available() {
		fmt.Println("  [!] mkvpropedit not found in PATH")
		return nil, false
	}
	return p.rewriteTags(filePath, func(xml string) (string, error) {
		return updateDebandInXML(xml, deband)
	})
}

// rewriteTags passes filePath's tags through update, writes the result back
// with mkvpropedit and restores the file's timestamps, so it appears
// unmodified to other tools. ok is false when the tags weren't written; failed
// names the timestamps ("mtime", "ctime") that couldn't be restored. Errors
// are printed.
func (p *Processor) rewriteTags(filePath string, update func(string) (string, error)) (failed []string, ok bool) {
	origStat, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("  [!] Cannot read timestamps to restore, not tagging: %v\n", err)
		return nil, false
	}

	currentXML, err := p.mkv.ExtractTags(filePath)
	if err != nil {
		fmt.Printf("  [!] Failed to extract tags: %v\n", err)
		return nil, false
	}

	updatedXML, err := update(currentXML)
	if err != nil {
		fmt.Printf("  [!] Failed to update tags: %v\n", err)
		return nil, false
	}

	tagsPath, err := p.mkv.WriteTempTags(updatedXML)
	if err != nil {
		fmt.Printf("  [!] Failed to write tags XML: %v\n", err)
		return nil, false
	}

	combined, rc, err := p.mkv.Apply(filePath, []string{"--tags", "all:" + tagsPath})
	p.mkv.Cleanup(tagsPath)

	if err != nil {
		fmt.Printf("  [!] subprocess error: %v\n", err)
		return nil, false
	}
	if !mkvpropeditWrote(rc) {
		fmt.Printf("  [!] mkvpropedit failed (rc=%d)\n%s\n", rc, strings.TrimSpace(combined))
		return nil, false
	}

	return setFileTimes(p.timer, filePath, origStat.ModTime()), true
}

// RunDebandOnly scans MediaDir and updates only the DEBAND MKV tag for files
// that have an explicit deband configuration in manual_data.json. It makes no
// TMDB API calls and preserves all other existing tags and file timestamps.
// Files with no deband configuration are skipped entirely.
func (p *Processor) RunDebandOnly() Stats {
	var stats Stats

	showFiles := map[string][]string{}
	var showOrder []string

	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(p.cfg.MediaDir, func(path string, d fs.DirEntry, err error) error {
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
				if failed, ok := p.applyDebandOnlyMKV(filePath, deband); ok {
					stats.countTagged("Deband updated (extra)", file, failed)
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
			if failed, ok := p.applyDebandOnlyMKV(filePath, deband); ok {
				stats.countTagged("Deband updated", file, failed)
			} else {
				fmt.Printf("  [X] Deband update failed: %s\n", file)
				stats.Failed++
			}
		}
	}

	// Only a rename changes the list, so an untouched one isn't rewritten.
	if p.retryMoved && newRetrySaver(p.retryList).saveOnce() {
		stats.LostSaves++
	}
	return stats
}

// applyPlexRatings pushes cached ratings to Plex at episode, season, and show
// level. Season and show ratings are computed as the maximum (most restrictive)
// rating across their child items. Does nothing when Plex is not configured.
// showName is used as a title-match fallback when TMDB GUID lookup fails.
// showFolder is the show's directory name, used to look up manual_data overrides.
// entry is the show's manual_data entry, supplying any per-account audio
// language overrides. errs counts the lookups and pushes that failed, each
// printed.
func (p *Processor) applyPlexRatings(tmdbID string, seasonEffectiveID map[int]string, showName, showFolder string, touched bool, entry ManualEntry) (errs int) {
	if p.plex == nil || p.ratingCache == nil {
		return errs
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
				errs++
			} else {
				// Title fallback for shows using non-TMDB agents (e.g. HAMA).
				var titleErr error
				showKey, titleErr = p.plex.FindShowByTitle(showName)
				if titleErr != nil && titleErr != ErrPlexNotFound {
					fmt.Printf("  [!] Plex: FindShowByTitle(%q): %v\n", showName, titleErr)
					errs++
				}
			}
		}
	}
	if showKey == "" {
		return errs
	}
	// Persist the resolved key so --language-only can find this show without scanning files.
	p.ratingCache.SetPlexKey(tmdbID, showKey)

	seasons, err := p.plex.GetSeasons(showKey)
	if err != nil {
		fmt.Printf("  [!] Plex: GetSeasons(%s): %v\n", showKey, err)
		errs++
		return errs
	}

	// Build an inverse mapping from (Plex season, Plex episode) → local epKey
	// so that manual_data Dates and TitleOverrides (keyed by local epKey) can be
	// looked up per Plex episode. Episodes without a Mapping entry use their Plex
	// season/episode numbers as the local key directly.
	type epKey struct{ s, e int }
	inverseMapping := make(map[epKey]string)
	manualEntry := p.manualData[showFolder]
	for localKey, pair := range manualEntry.Mapping {
		if len(pair) >= 2 {
			inverseMapping[epKey{pair[0], pair[1]}] = localKey
		}
	}

	showMax := RatingNone
	for _, season := range seasons {
		episodes, err := p.plex.GetEpisodes(season.RatingKey)
		if err != nil {
			fmt.Printf("  [!] Plex: GetEpisodes(%s) season %d: %v\n", season.RatingKey, season.SeasonNumber, err)
			errs++
			continue
		}

		seasonMax := RatingNone
		for _, ep := range episodes {
			// Ratings for a season remapped via overrides were cached under the
			// override's TMDB ID, not the folder-level one.
			effID := tmdbID
			if id, ok := seasonEffectiveID[ep.SeasonNumber]; ok && id != "" {
				effID = id
			}
			r, ok := p.ratingCache.GetEpisodeRating(effID, ep.SeasonNumber, ep.EpisodeNumber)
			if !ok {
				r, ok = p.ratingCache.GetShowRating(effID)
			}
			if !ok || r == RatingNone {
				continue
			}
			if err := p.plex.SetContentRating(ep.RatingKey, PlexTypeEpisode, r); err != nil {
				fmt.Printf("  [!] Plex: set episode %s rating: %v\n", ep.RatingKey, err)
				errs++
			}
			seasonMax = MaxRating(seasonMax, r)
		}

		// Set originally available date (and title when also overridden) in Plex
		// for episodes that have a dates entry in manual_data.
		for _, ep := range episodes {
			localKey, hasMapped := inverseMapping[epKey{ep.SeasonNumber, ep.EpisodeNumber}]
			if !hasMapped {
				localKey = fmt.Sprintf("S%02dE%02d", ep.SeasonNumber, ep.EpisodeNumber)
			}
			epDate := manualEntry.Dates[localKey]
			if epDate == "" {
				continue
			}
			epTitle := cleanMetadataTitle(manualEntry.TitleOverrides[localKey])
			if err := p.plex.SetEpisodeMetadata(ep.RatingKey, epTitle, epDate); err != nil {
				fmt.Printf("  [!] Plex: set episode %s metadata: %v\n", ep.RatingKey, err)
				errs++
			}
		}

		if seasonMax != RatingNone {
			if err := p.plex.SetContentRating(season.RatingKey, PlexTypeSeason, seasonMax); err != nil {
				fmt.Printf("  [!] Plex: set season %s rating: %v\n", season.RatingKey, err)
				errs++
			}
			showMax = MaxRating(showMax, seasonMax)
		}
	}

	if showMax != RatingNone {
		if err := p.plex.SetContentRating(showKey, PlexTypeShow, showMax); err != nil {
			fmt.Printf("  [!] Plex: set show %s rating: %v\n", showKey, err)
			errs++
		}
	}

	if !p.cfg.RatingsOnly {
		if !p.applyPlexAudioLanguage(showKey, tmdbID, showName, entry) {
			errs++
		}
	}

	if touched {
		if err := p.plex.RefreshMetadata(showKey); err != nil {
			fmt.Printf("  [!] Plex: refresh metadata for show %s: %v\n", showKey, err)
			errs++
		} else {
			fmt.Printf("  [i] Plex: metadata refresh triggered for show %s\n", showKey)
		}
	}
	return errs
}

// originalLang returns the TMDB original_language code (ISO 639-1) for the
// given show, using the session cache to avoid redundant API calls. It is ""
// when TMDB has none, or there is no TMDB client; err is a failed TMDB lookup.
func (p *Processor) originalLang(tmdbID string) (string, error) {
	if lang, ok := p.sessionOriginalLang[tmdbID]; ok {
		return lang, nil
	}
	if p.tmdb == nil {
		return "", nil
	}
	details, err := p.tmdb.GetShow(tmdbID)
	if err != nil {
		return "", err
	}
	if details.OriginalLanguage != "" {
		p.sessionOriginalLang[tmdbID] = details.OriginalLanguage
	}
	return details.OriginalLanguage, nil
}

// seasonData returns a TMDB season, caching it in cache. A season TMDB doesn't
// have is reported once and is an empty result. Any other error is returned
// and not cached, so the next episode tries again.
func (p *Processor) seasonData(cache map[string]SeasonResult, showID string, season int) (SeasonResult, error) {
	key := fmt.Sprintf("%s_S%d", showID, season)
	if sr, ok := cache[key]; ok {
		return sr, nil
	}
	sr, err := p.tmdb.GetSeason(showID, season)
	if errors.Is(err, errTMDBNotFound) {
		// TMDB's 404 is the same for a missing season and a wrong show ID
		// (e.g. a typo in a manual_data override), so say which was asked for.
		fmt.Printf("  [i] TMDB has no season %d for show ID %s; tagging without episode data\n", season, showID)
		sr, err = SeasonResult{}, nil
	}
	if err != nil {
		return SeasonResult{}, fmt.Errorf("show ID %s season %d: %w", showID, season, err)
	}
	cache[key] = sr
	return sr, nil
}

// trackRetry keeps a file on the retry list while it is awaiting an air date
// or a step failed, so a later run finishes it even once it falls before the
// cutoff. A file that keeps failing a step is dropped once RetryList.Fail
// gives up on it, so a step that always fails doesn't rewrite it on every run.
// One still waiting for an air date is never dropped.
func (p *Processor) trackRetry(filePath, file string, awaitingDate bool, failed []string) {
	switch {
	case awaitingDate:
		p.retryList.Keep(filePath)
	case len(failed) > 0:
		if gaveUp, runs, days := p.retryList.Fail(filePath); gaveUp {
			hint := "" // under --all it is processed on every run anyway
			if !p.cfg.CutoffDate.IsZero() {
				hint = "; run with --all to try again"
			}
			fmt.Printf("  [!] Not retrying %s after %d runs in a row failed over %d days (failed: %s)%s.\n",
				file, runs, days, strings.Join(failed, ", "), hint)
		}
	default:
		p.retryList.Remove(filePath)
	}
}

// trackTagFailure is trackRetry for a file whose tags couldn't be written.
// Missing mkvpropedit fails every file alike, so the list is left as it was:
// the run doesn't count, and a file not on it isn't added (under --all that
// would put the whole library on it).
func (p *Processor) trackTagFailure(filePath, file string, awaitingDate bool) {
	if !p.mkv.Available() {
		return
	}
	p.trackRetry(filePath, file, awaitingDate, []string{"tags"})
}

// failTMDBLookup reports a failed TMDB season lookup for file and leaves it
// untagged rather than writing partial metadata. The retry list brings it back
// on the next run even once it falls before the cutoff.
func (p *Processor) failTMDBLookup(stats *Stats, filePath, file string, err error) {
	fmt.Printf("  [!] TMDB season lookup failed: %v\n", err)
	fmt.Printf("  [X] Failed: %s\n", file)
	p.retryList.Add(filePath)
	stats.Failed++
}

// applyPlexAudioLanguage sets the show's preferred audio language in Plex, but
// only once per show unless OverwriteLanguage is set. This lets the user
// override the preference inside Plex on a show-by-show basis without it being
// overwritten on subsequent runs. The language is manual_data.json's "language"
// if set, otherwise TMDB's original_language; "alt-language" and
// "alt2-language" override it for the corresponding alt accounts. Nothing is
// applied when no language is known for the primary account. ok is false when
// the TMDB lookup or the push failed (printed); a failed alt account is only
// named, as in --language-only.
func (p *Processor) applyPlexAudioLanguage(showKey, tmdbID, showName string, entry ManualEntry) (ok bool) {
	if p.ratingCache == nil {
		return true
	}
	langs, err := p.showAudioLangs(tmdbID, entry)
	if err != nil {
		fmt.Printf("  [!] TMDB show lookup failed for ID %s (original language unknown): %v\n", tmdbID, err)
		return false
	}
	if langs.Default == "" {
		return true
	}
	applied, due := p.languageDue(tmdbID, langs)
	if !due {
		return true
	}
	set, altSuffix, err := p.setAudioLanguage(showKey, tmdbID, langs, applied)
	switch {
	case errors.Is(err, ErrPlexNotFound):
		// The show key is stale and was cleared; the next run looks it up again.
	case err != nil:
		fmt.Printf("  [!] Plex: set audio language for %q: %v\n", showName, err)
		return false
	case set != "" || altSuffix != "":
		fmt.Printf("  [i] Plex: audio language for %q set to %q%s\n", showName, appliedLangSignature(p.plex, langs), altSuffix)
	}
	return true
}

// showAudioLangs returns the show's audio languages from its manual_data.json
// entry, with TMDB's original language as the default when the entry sets
// none. An empty Default means no language is known; err is a failed TMDB lookup.
func (p *Processor) showAudioLangs(tmdbID string, entry ManualEntry) (AudioLangs, error) {
	tmdbLang := ""
	if entry.Language == "" {
		var err error
		if tmdbLang, err = p.originalLang(tmdbID); err != nil {
			return AudioLangs{}, err
		}
	}
	return entry.audioLangs(tmdbLang), nil
}

// languageDue reports whether langs have yet to be set for the show, and the
// signature recorded as applied ("" with OverwriteLanguage, so every account
// is set again).
func (p *Processor) languageDue(tmdbID string, langs AudioLangs) (applied string, due bool) {
	if p.cfg.OverwriteLanguage {
		return "", true
	}
	applied, ok := p.ratingCache.GetLanguageApplied(tmdbID)
	return applied, !ok || applied != appliedLangSignature(p.plex, langs)
}

// setAudioLanguage sets langs on the Plex show showKey and records them. With
// alt accounts, only those applied doesn't cover yet are set, so a retry
// leaves the others (and any choice made in Plex) alone; a failed alt is
// reported in altSuffix and left out of the record, so the next run retries it
// alone. set is the signature langs give every account, failed alts included,
// or "" when no account was pushed to: only removed alt accounts differed, or
// every alt that needed it failed. A show key Plex no longer has is cleared,
// and err is ErrPlexNotFound.
func (p *Processor) setAudioLanguage(showKey, tmdbID string, langs AudioLangs, applied string) (set, altSuffix string, err error) {
	sig := appliedLangSignature(p.plex, langs)
	record := sig
	pushed := false
	multi, isMulti := p.plex.(multiAccountReporter)
	switch {
	case isMulti:
		record, pushed, err = multi.setLanguagePending(showKey, langs, applied)
	case parseLangSignature(applied)[""] == sig:
		// Only alt entries differ (their tokens were removed): the primary
		// already has its language, so it isn't pushed again.
	default:
		err = p.plex.SetAudioLanguage(showKey, langs)
		pushed = true
	}
	if err != nil {
		if errors.Is(err, ErrPlexNotFound) {
			p.ratingCache.ClearPlexKey(tmdbID)
		}
		return "", "", err
	}
	if isMulti {
		altSuffix = multi.altErrSuffix()
	}
	p.ratingCache.SetLanguageApplied(tmdbID, record)
	if pushed {
		set = sig
	}
	return set, altSuffix, nil
}
