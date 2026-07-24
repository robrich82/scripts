package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const failedCacheFile = "failed_files.json"
const unknownCodecsFile = "unknown_codecs.json"

// MKVIdentifier reads track metadata from an MKV file.
type MKVIdentifier interface {
	Identify(filePath string) (*MKVInfo, error)
	// ReadTagDate returns the DATE_RECORDED Matroska tag value and true when
	// present. Returns zero time and false when absent or unsupported.
	ReadTagDate(filePath string) (time.Time, bool)
}

// MKVEditor applies edits to an MKV file via mkvpropedit.
type MKVEditor interface {
	Apply(filePath string, edits []string) error
}

type realMKVRunner struct {
	mkvmerge    string
	mkvpropedit string
	mkvextract  string
}

func newRealMKVRunner() (*realMKVRunner, error) {
	merge, err := exec.LookPath("mkvmerge")
	if err != nil {
		return nil, fmt.Errorf("mkvmerge not found in PATH")
	}
	prop, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, fmt.Errorf("mkvpropedit not found in PATH")
	}
	extract, _ := exec.LookPath("mkvextract") // optional; empty string disables date sync
	return &realMKVRunner{mkvmerge: merge, mkvpropedit: prop, mkvextract: extract}, nil
}

func (r *realMKVRunner) ReadTagDate(filePath string) (time.Time, bool) {
	if r.mkvextract == "" {
		return time.Time{}, false
	}
	return readTagDateFromMKA(r.mkvextract, filePath)
}

func (r *realMKVRunner) Identify(filePath string) (*MKVInfo, error) {
	out, err := exec.Command(r.mkvmerge, "--identify", "--identification-format", "json", filePath).Output()
	if err != nil {
		// mkvmerge exits non-zero on warnings; try parsing anyway.
		if len(out) == 0 {
			return nil, err
		}
	}
	var info MKVInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (r *realMKVRunner) Apply(filePath string, edits []string) error {
	args := append([]string{filePath}, edits...)
	cmd := exec.Command(r.mkvpropedit, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
}

// loggingEditor wraps an MKVEditor and prints the planned edits before applying.
type loggingEditor struct {
	inner MKVEditor
}

func (l *loggingEditor) Apply(filePath string, edits []string) error {
	fmt.Printf("  edits: mkvpropedit %q %s\n", filepath.Base(filePath), strings.Join(edits, " "))
	return l.inner.Apply(filePath, edits)
}

// loadFailedCache reads a list of previously failed file paths from stateDir.
func loadFailedCache(stateDir string) []string {
	data, err := os.ReadFile(filepath.Join(stateDir, failedCacheFile))
	if err != nil {
		return nil
	}
	var paths []string
	json.Unmarshal(data, &paths)
	return paths
}

// saveFailedCache writes the list of failed paths to stateDir.
func saveFailedCache(stateDir string, paths []string) {
	data, _ := json.MarshalIndent(paths, "", "    ")
	os.WriteFile(filepath.Join(stateDir, failedCacheFile), data, 0644)
}

// fetchOrigins pre-computes OriginInfo for every unique content-root directory
// in filesToScan. Results are keyed by the content-root directory name
// (first path component relative to libraryRoot). TMDB calls are sequential to
// respect rate limits. Entries that produce no match are absent from the map.
//
// When no sibling-cache entry exists for a directory, fetchOrigins searches
// TMDB interactively: the top result is shown and the user is prompted to
// accept it, enter a manual TMDB ID, or skip. Confirmed IDs are saved back to
// the sibling cache so future runs (and mkv-tvshow / mkv-movie) can skip the
// search step.
func fetchOrigins(filesToScan []string, libraryRoot, libType string, tmdb TMDBOriginClient, prompter Prompter) map[string]OriginInfo {
	origins := make(map[string]OriginInfo)
	if tmdb == nil || libraryRoot == "" || (libType != "tv" && libType != "movie") {
		return origins
	}

	sibling := loadSiblingCaches()
	var autoAll, skipAll bool

	seen := map[string]bool{}
	for _, path := range filesToScan {
		dir := contentRootDir(path, libraryRoot)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true

		title, year := dirTitleYear(dir)
		if title == "" {
			continue
		}

		var info OriginInfo
		var found bool
		var err error

		switch libType {
		case "tv":
			if id := sibling.lookupTV(dir, libraryRoot); id != "" {
				info, found, err = tmdb.GetTVOriginByID(id)
			} else if !skipAll {
				id := resolveTMDBID(dir, title, year, libType, libraryRoot, tmdb, prompter, &autoAll, &skipAll)
				if id != "" {
					info, found, err = tmdb.GetTVOriginByID(id)
				}
			}
		case "movie":
			if id := sibling.lookupMovie(title, year); id != "" {
				info, found, err = tmdb.GetMovieOriginByID(id)
			} else if !skipAll {
				id := resolveTMDBID(dir, title, year, libType, libraryRoot, tmdb, prompter, &autoAll, &skipAll)
				if id != "" {
					info, found, err = tmdb.GetMovieOriginByID(id)
				}
			}
		}
		if err != nil {
			fmt.Printf("[TMDB]   %s: %v\n", dir, err)
			continue
		}
		if found {
			origins[dir] = info
		} else {
			fmt.Printf("[TMDB]   %s: no match found\n", dir)
		}
	}
	return origins
}

// resolveTMDBID searches TMDB for dir and prompts the user to confirm the top
// result or enter a manual ID. On confirmation the ID is saved to the sibling
// cache and returned. Returns "" when the user skips or no prompter is available.
func resolveTMDBID(dir, title, year, libType, libraryRoot string, tmdb TMDBOriginClient, prompter Prompter, autoAll, skipAll *bool) string {
	if prompter == nil || *skipAll {
		return ""
	}

	var topID, topName, topYear string

	switch libType {
	case "tv":
		candidates, err := tmdb.SearchTV(title, year)
		if err != nil {
			fmt.Printf("[TMDB]   %s: search error: %v\n", dir, err)
			return ""
		}
		if len(candidates) > 0 {
			topID = candidates[0].ID
			topName = candidates[0].Name
			topYear = candidates[0].Year
		}
	case "movie":
		candidates, err := tmdb.SearchMovie(title, year)
		if err != nil {
			fmt.Printf("[TMDB]   %s: search error: %v\n", dir, err)
			return ""
		}
		if len(candidates) > 0 {
			topID = candidates[0].ID
			topName = candidates[0].Title
			topYear = candidates[0].Year
		}
	}

	var confirmedID string
	if topID == "" {
		// No search results — ask user to provide an ID manually.
		ans, _ := prompter.Ask(fmt.Sprintf("[TMDB]   %s: no results found. Enter TMDB id or blank to skip: ", dir))
		ans = strings.TrimSpace(ans)
		if ans != "" {
			confirmedID = ans
		}
	} else if *autoAll {
		confirmedID = topID
	} else {
		prompt := fmt.Sprintf("[TMDB]   %s: found %q (%s) [id=%s]. Accept? (y/n/a=all/s=skip all/<id>): ", dir, topName, topYear, topID)
		ans, _ := prompter.Ask(prompt)
		ans = strings.TrimSpace(ans)
		switch strings.ToLower(ans) {
		case "y", "":
			confirmedID = topID
		case "a":
			*autoAll = true
			confirmedID = topID
		case "n":
			// skip this entry
		case "s":
			*skipAll = true
		default:
			// Treat any other input as a manual TMDB ID.
			confirmedID = ans
		}
	}

	if confirmedID == "" {
		return ""
	}

	// Persist the confirmed ID to the sibling cache.
	switch libType {
	case "tv":
		absPath := filepath.Join(libraryRoot, dir)
		if err := saveTVShowCacheEntry(absPath, confirmedID); err != nil {
			fmt.Printf("[TMDB]   %s: could not save to sibling cache: %v\n", dir, err)
		}
	case "movie":
		if err := saveMovieTMDBIDEntry(title, year, confirmedID); err != nil {
			fmt.Printf("[TMDB]   %s: could not save to sibling cache: %v\n", dir, err)
		}
	}

	return confirmedID
}

// langFix describes a single track that needs its ISO language corrected.
type langFix struct {
	trackID     int
	trackType   string // "video" or "audio"
	currentLang string
	targetLang  string
	targetIETF  string
}

// langMismatchCandidate groups the fixable mismatch edits for one file.
type langMismatchCandidate struct {
	path  string
	fixes []langFix
}

// undAudioCandidate holds a file whose first audio track has language 'und'.
type undAudioCandidate struct {
	path         string
	trackID      int
	resolvedIETF map[string]string // per-file resolved IETF map (global + TMDB + dir overrides)
}

// trimCandidate holds the data needed for the sequential trim phase.
type trimCandidate struct {
	path       string
	info       MKVInfo
	originLang string
	// accountLangs are the ISO 639-2/B audio languages the Plex accounts are
	// configured to watch this title in, from the sibling tool's manual_data.json.
	accountLangs []string
}

// applyLangFixes returns a copy of tracks with the language and IETF tag of each
// fixed track updated to the fix's target. Used so a trim plan computed after
// the lang-mismatch phase sees the corrected languages already written to disk.
func applyLangFixes(tracks []Track, fixes []langFix) []Track {
	out := make([]Track, len(tracks))
	copy(out, tracks)
	for _, f := range fixes {
		for i := range out {
			if out[i].ID == f.trackID {
				out[i].Properties.Language = f.targetLang
				out[i].Properties.LanguageIETF = f.targetIETF
			}
		}
	}
	return out
}

// RunAll scans root for MKV/MKA files and fixes each one.
// stateDir is where failed_files.json and unknown_codecs.json are stored.
// lib is the active library config (may be nil for no overrides).
// tmdb is used to look up the original language per content-root directory;
// pass nil to skip TMDB lookups (e.g. no API key configured).
// remuxer is used to remove/reorder tracks after property edits; pass nil to
// skip track trimming entirely.
func RunAll(root, stateDir string, lib *Library, ident MKVIdentifier, editor MKVEditor, prompter Prompter, tmdb TMDBOriginClient, remuxer MKVRemuxer, cutoffDate time.Time) Stats {
	var stats Stats
	var trimCandidates []trimCandidate
	var langMismatches []langMismatchCandidate
	var undCandidates []undAudioCandidate
	unknownCodecs := map[string]bool{}

	// Check for a previous failure cache.
	var filesToScan []string
	oldFails := loadFailedCache(stateDir)
	if len(oldFails) > 0 {
		ans, _ := prompter.Ask(fmt.Sprintf("Found %d previously failed files. Run only these? (y/n): ", len(oldFails)))
		if strings.ToLower(strings.TrimSpace(ans)) == "y" {
			filesToScan = oldFails
		}
	}

	if len(filesToScan) == 0 {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			lower := strings.ToLower(d.Name())
			if !strings.HasSuffix(lower, ".mkv") && !strings.HasSuffix(lower, ".mka") {
				return nil
			}
			if !cutoffDate.IsZero() {
				info, err := d.Info()
				if err == nil && info.ModTime().Before(cutoffDate) {
					stats.SkippedOld++
					return nil
				}
			}
			filesToScan = append(filesToScan, path)
			return nil
		})
	}

	// Pre-compute TMDB origin info sequentially before the concurrent phase.
	libRoot := ""
	libType := ""
	if lib != nil {
		libRoot = lib.Path
		libType = lib.Type
	}
	origins := fetchOrigins(filesToScan, libRoot, libType, tmdb, prompter)

	// Per-title audio language preferences from mkv-tvshow / mkv-movie, so trim
	// keeps the audio each Plex account actually watches.
	accountLangs := loadAccountLangs(libType)

	// First pass: identify files whose first audio track has language 'und'.
	// This runs before the main analysis so that language fixes are applied
	// first, allowing the main pass to analyze flags with correct language tags.
	{
		sem := make(chan struct{}, runtime.NumCPU())
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, path := range filesToScan {
			wg.Add(1)
			sem <- struct{}{}
			go func(p string) {
				defer wg.Done()
				defer func() { <-sem }()
				info, err := ident.Identify(p)
				if err != nil {
					return
				}
				for _, t := range info.Tracks {
					if t.Type == "audio" {
						if normalizeLanguage(t.Properties.Language) == "und" {
							origin := origins[contentRootDir(p, libRoot)]
							resolved := resolveIETFMap(p, libRoot, lib, origin)
							mu.Lock()
							undCandidates = append(undCandidates, undAudioCandidate{
								path:         p,
								trackID:      t.ID,
								resolvedIETF: resolved,
							})
							mu.Unlock()
							fmt.Printf("[UND]    %s: first audio track language is 'und'\n", filepath.Base(p))
						}
						break
					}
				}
			}(path)
		}
		wg.Wait()
	}

	// Sequential und-audio fix phase: prompt for language, then apply.
	// Runs before the main analysis so the main pass sees correct language tags.
	if len(undCandidates) > 0 {
		autoAll := false
		for _, uc := range undCandidates {
			basename := filepath.Base(uc.path)
			defaultIETF := uc.resolvedIETF["eng"]
			if defaultIETF == "" {
				defaultIETF = "en-US"
			}
			ietfChoice := defaultIETF
			if !autoAll {
				ans, _ := prompter.Ask(fmt.Sprintf("[UND]    %s: audio track language is 'und'. Enter IETF language [%s]: ", basename, defaultIETF))
				ans = strings.TrimSpace(ans)
				switch {
				case strings.EqualFold(ans, "a"):
					autoAll = true
					// ietfChoice stays as defaultIETF
				case ans != "":
					ietfChoice = ans
				}
			}
			// Build reverse map from the file's resolved IETF map so directory
			// overrides (e.g. eng → en-GB) are reflected in the lookup.
			ietfToISO := make(map[string]string, len(uc.resolvedIETF))
			for iso, ietf := range uc.resolvedIETF {
				ietfToISO[strings.ToLower(ietf)] = iso
			}
			isoCode, ok := ietfToISO[strings.ToLower(ietfChoice)]
			if !ok {
				fmt.Printf("[SKIP]   %s (unknown language %q)\n", basename, ietfChoice)
				continue
			}
			canonicalIETF := uc.resolvedIETF[isoCode]
			edits := trackEdit(uc.trackID, "language", isoCode, "language-ietf", canonicalIETF)
			if err := editor.Apply(uc.path, edits); err != nil {
				fmt.Printf("[ERR]    %s: und fix: %v\n", basename, err)
				stats.Failed++
				stats.FailedPaths = append(stats.FailedPaths, uc.path)
			} else {
				fmt.Printf("[LANGFIX] %s: audio language 'und' → %s (%s)\n", basename, isoCode, canonicalIETF)
				stats.LangFixes++
			}
		}
	}

	sem := make(chan struct{}, runtime.NumCPU())
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, path := range filesToScan {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()

			basename := filepath.Base(p)
			var buf strings.Builder
			var local Stats
			var localCodecs []string
			var localMismatches []langMismatchCandidate

			// Resolve per-file IETF map and original language.
			origin := origins[contentRootDir(p, libRoot)]
			ietfMap := resolveIETFMap(p, libRoot, lib, origin)
			originLang := resolveOriginLang(p, libRoot, lib, origin)

			info, err := ident.Identify(p)
			var res AnalysisResult
			if err != nil {
				fmt.Fprintf(&buf, "[ERR]    %s: %v\n", basename, err)
				local.Failed++
				local.FailedPaths = append(local.FailedPaths, p)
			} else {
				res = AnalyzeFile(*info, AnalyzeOptions{IETFMap: ietfMap, OriginLang: originLang, UndLang: resolveUndLang(p, libRoot, lib)})
				localCodecs = append(localCodecs, res.UnknownCodecs...)

				// Detect video/audio tracks whose language does not match the active
				// dir override. Collect fixable cases for the sequential prompt phase.
				if rawOverride := matchedOverrideIETF(p, libRoot, lib); len(rawOverride) > 0 {
					// Exclude "und" from the mismatch check. "und" is a fallback
					// for untagged tracks, not an expected content language — a jpn
					// track is not "wrong" just because the override contains "und".
					overrideIETF := make(map[string]string, len(rawOverride))
					for k, v := range rawOverride {
						if k != "und" {
							overrideIETF[k] = v
						}
					}
					if len(overrideIETF) > 0 {
						overrideKeys := make([]string, 0, len(overrideIETF))
						for k := range overrideIETF {
							overrideKeys = append(overrideKeys, k)
						}
						sort.Strings(overrideKeys)
						keysStr := "[" + strings.Join(overrideKeys, ", ") + "]"
						// Unambiguous fix target: only when merged override has exactly one key.
						var fixLang, fixIETF string
						if len(overrideIETF) == 1 {
							fixLang, fixIETF = overrideKeys[0], overrideIETF[overrideKeys[0]]
						}
						var candidate langMismatchCandidate
						candidate.path = p
						// Use the post-normalisation track state so a track that
						// AnalyzeFile is already correcting in this same pass isn't
						// also queued for a redundant manual lang fix.
						for _, t := range res.UpdatedTracks {
							if t.Type != "video" && t.Type != "audio" {
								continue
							}
							lang := normalizeLanguage(t.Properties.Language)
							if _, ok := overrideIETF[lang]; ok {
								continue
							}
							fmt.Fprintf(&buf, "[WARN]   %s: %s track language %q not in override %s\n",
								basename, t.Type, lang, keysStr)
							if fixLang != "" {
								candidate.fixes = append(candidate.fixes, langFix{
									trackID:     t.ID,
									trackType:   t.Type,
									currentLang: lang,
									targetLang:  fixLang,
									targetIETF:  fixIETF,
								})
							}
						}
						if len(candidate.fixes) > 0 {
							localMismatches = append(localMismatches, candidate)
						}
					}
				}

				// MKA container date sync: read DATE_RECORDED tag and compare with
				// the container date header; append a fix edit when they differ.
				var dateFix int
				if !res.Failed && strings.EqualFold(filepath.Ext(basename), ".mka") {
					if tagDate, ok := ident.ReadTagDate(p); ok {
						needsFix := true
						if info.Container.Properties.DateUTC != nil {
							containerDate := time.Unix(int64(*info.Container.Properties.DateUTC), 0).UTC()
							needsFix = containerDate.Format("2006-01-02") != tagDate.UTC().Format("2006-01-02")
						}
						if needsFix {
							dateStr := tagDate.UTC().Format("2006-01-02") + "T00:00:00Z"
							res.Edits = append(res.Edits, "--edit", "info", "--set", "date="+dateStr)
							dateFix = 1
						}
					}
				}

				if res.Failed {
					fmt.Fprintf(&buf, "[FAILED] %s: %s\n", basename, strings.Join(res.FailReasons, ", "))
					local.Failed++
					local.FailedPaths = append(local.FailedPaths, p)
				} else if len(res.Edits) == 0 {
					fmt.Fprintf(&buf, "[OK]     %s (no changes needed)\n", basename)
				} else if err := editor.Apply(p, res.Edits); err != nil {
					fmt.Fprintf(&buf, "[ERR]    %s: mkvpropedit: %v\n", basename, err)
					local.Failed++
					local.FailedPaths = append(local.FailedPaths, p)
				} else {
					fmt.Fprintf(&buf, "[FIXED]  %s\n", basename)
					local.Fixed++
					local.LangFixes += res.LocalFixes.Lang
					local.IETFFixes += res.LocalFixes.IETF
					local.VideoInherit += res.LocalFixes.Video
					local.FlagFixes += res.LocalFixes.Flags
					local.NamesAdded += res.LocalFixes.Names
					local.DateFixes += dateFix
				}
			}

			mu.Lock()
			fmt.Print(buf.String())
			stats.Scanned++
			stats.Failed += local.Failed
			stats.FailedPaths = append(stats.FailedPaths, local.FailedPaths...)
			stats.Fixed += local.Fixed
			stats.LangFixes += local.LangFixes
			stats.IETFFixes += local.IETFFixes
			stats.VideoInherit += local.VideoInherit
			stats.FlagFixes += local.FlagFixes
			stats.NamesAdded += local.NamesAdded
			stats.DateFixes += local.DateFixes
			for _, c := range localCodecs {
				unknownCodecs[c] = true
			}
			langMismatches = append(langMismatches, localMismatches...)
			if remuxer != nil && info != nil && !res.Failed {
				// Use the post-normalisation track state so that language
				// corrections (e.g. jpn → eng from track-name inference) are
				// reflected in the trim decision rather than the pre-edit snapshot.
				trimInfo := *info
				trimInfo.Tracks = res.UpdatedTracks
				trimCandidates = append(trimCandidates, trimCandidate{
					path:         p,
					info:         trimInfo,
					originLang:   originLang,
					accountLangs: accountLangs.forFile(p, libRoot),
				})
			}
			mu.Unlock()
		}(path)
	}

	wg.Wait()

	// Records lang fixes actually written to disk, keyed by path, so the trim
	// phase below plans against the corrected track state rather than the
	// pre-fix snapshot captured during the parallel scan (otherwise a track
	// whose language was just corrected here could be dropped as foreign).
	appliedLangFixes := make(map[string][]langFix)

	// Sequential lang-mismatch fix phase: prompt per file, then apply.
	if len(langMismatches) > 0 {
		autoAll := false
		for _, lm := range langMismatches {
			basename := filepath.Base(lm.path)
			if !autoAll {
				var lines []string
				for _, f := range lm.fixes {
					lines = append(lines, fmt.Sprintf("    %s track: %q → %s (%s)", f.trackType, f.currentLang, f.targetLang, f.targetIETF))
				}
				ans, _ := prompter.Ask(fmt.Sprintf("[LANGFIX] %s\n%s\n  Fix language? (y/n/a=all): ", basename, strings.Join(lines, "\n")))
				ans = strings.ToLower(strings.TrimSpace(ans))
				switch ans {
				case "a":
					autoAll = true
				case "y":
					// proceed
				default:
					fmt.Printf("[SKIP]   %s (lang fix skipped)\n", basename)
					continue
				}
			}
			var edits []string
			for _, f := range lm.fixes {
				edits = append(edits, trackEdit(f.trackID, "language", f.targetLang, "language-ietf", f.targetIETF)...)
			}
			if err := editor.Apply(lm.path, edits); err != nil {
				fmt.Printf("[ERR]    %s: lang fix: %v\n", basename, err)
				stats.Failed++
				stats.FailedPaths = append(stats.FailedPaths, lm.path)
			} else {
				fmt.Printf("[LANGFIX] %s: language set to %s\n", basename, lm.fixes[0].targetLang)
				stats.LangFixes++
				appliedLangFixes[lm.path] = append(appliedLangFixes[lm.path], lm.fixes...)
			}
		}
	}

	// Sequential trim phase: prompt per file, then remux.
	if remuxer != nil {
		autoAll := false
		for _, tc := range trimCandidates {
			// Reflect any lang fixes applied above so the plan doesn't drop a
			// track whose language was just corrected on disk.
			if fixes, ok := appliedLangFixes[tc.path]; ok {
				tc.info.Tracks = applyLangFixes(tc.info.Tracks, fixes)
			}
			plan := PlanTrim(tc.info, tc.originLang, tc.accountLangs)
			if !plan.NeedsChange {
				continue
			}
			basename := filepath.Base(tc.path)
			if !autoAll {
				summary := TrimSummary(plan)
				ans, _ := prompter.Ask(fmt.Sprintf("[TRIM]   %s\n%s  Proceed? (y/n/a=all): ", basename, summary))
				ans = strings.ToLower(strings.TrimSpace(ans))
				switch ans {
				case "a":
					autoAll = true
				case "y":
					// proceed
				default:
					fmt.Printf("[SKIP]   %s (trim skipped)\n", basename)
					continue
				}
			}
			fmt.Printf("[TRIMMING] %s...\n", basename)
			if err := remuxer.Remux(tc.path, plan, tc.info); err != nil {
				fmt.Printf("[ERR]    %s: remux: %v\n", basename, err)
				stats.Failed++
				stats.FailedPaths = append(stats.FailedPaths, tc.path)
			} else {
				fmt.Printf("[TRIMMED] %s\n", basename)
				stats.Trimmed++
			}
		}
	}

	// Persist failure list and unknown codecs.
	if len(stats.FailedPaths) > 0 {
		os.MkdirAll(stateDir, 0755)
		saveFailedCache(stateDir, stats.FailedPaths)
	} else {
		os.Remove(filepath.Join(stateDir, failedCacheFile))
	}

	if len(unknownCodecs) > 0 {
		var list []string
		for c := range unknownCodecs {
			list = append(list, c)
		}
		os.MkdirAll(stateDir, 0755)
		outPath := filepath.Join(stateDir, unknownCodecsFile)
		data, _ := json.MarshalIndent(list, "", "    ")
		os.WriteFile(outPath, data, 0644)
		fmt.Printf("New formats found: %s\n", outPath)
	}

	return stats
}
