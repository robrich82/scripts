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
// Before doing a title-search, fetchOrigins checks the sibling tool caches
// (mkv-tvshow's show_cache.json / mkv-movie's rating_cache.json) for a
// previously resolved TMDB ID and uses the direct /tv/{id} or /movie/{id}
// endpoint when one is found, avoiding a redundant search call.
func fetchOrigins(filesToScan []string, libraryRoot, libType string, tmdb TMDBOriginClient) map[string]OriginInfo {
	origins := make(map[string]OriginInfo)
	if tmdb == nil || libraryRoot == "" || (libType != "tv" && libType != "movie") {
		return origins
	}

	sibling := loadSiblingCaches()

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
			if id := sibling.lookupTV(dir); id != "" {
				info, found, err = tmdb.GetTVOriginByID(id)
			} else {
				info, found, err = tmdb.GetTVOrigin(title, year)
			}
		case "movie":
			if id := sibling.lookupMovie(title, year); id != "" {
				info, found, err = tmdb.GetMovieOriginByID(id)
			} else {
				info, found, err = tmdb.GetMovieOrigin(title, year)
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
	path    string
	trackID int
}

// trimCandidate holds the data needed for the sequential trim phase.
type trimCandidate struct {
	path       string
	info       MKVInfo
	originLang string
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
	origins := fetchOrigins(filesToScan, libRoot, libType, tmdb)

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
			originLang := origin.OrigLangISO()

			info, err := ident.Identify(p)
			var res AnalysisResult
			if err != nil {
				fmt.Fprintf(&buf, "[ERR]    %s: %v\n", basename, err)
				local.Failed++
				local.FailedPaths = append(local.FailedPaths, p)
			} else {
				res = AnalyzeFile(*info, AnalyzeOptions{IETFMap: ietfMap, OriginLang: originLang})
				localCodecs = append(localCodecs, res.UnknownCodecs...)

				// Detect video/audio tracks whose language does not match the active
				// dir override. Collect fixable cases for the sequential prompt phase.
				if overrideIETF := matchedOverrideIETF(p, libRoot, lib); len(overrideIETF) > 0 {
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
					for _, t := range info.Tracks {
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

				// MKA container date sync: read DATE_RECORDED tag and compare with
				// the container date header; append a fix edit when they differ.
				var dateFix int
				if !res.Failed && strings.EqualFold(filepath.Ext(basename), ".mka") {
					if tagDate, ok := ident.ReadTagDate(p); ok {
						needsFix := true
						if info.Container.Properties.DateUTC != nil {
							containerDate := time.Unix(*info.Container.Properties.DateUTC, 0).UTC()
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

			// Detect first audio track with language 'und' for sequential prompting.
			var localUnd []undAudioCandidate
			if info != nil {
				for _, t := range info.Tracks {
					if t.Type == "audio" {
						if normalizeLanguage(t.Properties.Language) == "und" {
							localUnd = append(localUnd, undAudioCandidate{path: p, trackID: t.ID})
							fmt.Fprintf(&buf, "[UND]    %s: first audio track language is 'und'\n", basename)
						}
						break
					}
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
			undCandidates = append(undCandidates, localUnd...)
			if remuxer != nil && info != nil && !res.Failed {
				trimCandidates = append(trimCandidates, trimCandidate{
					path:       p,
					info:       *info,
					originLang: originLang,
				})
			}
			mu.Unlock()
		}(path)
	}

	wg.Wait()

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
			}
		}
	}

	// Sequential und-audio fix phase: prompt for language, then apply.
	if len(undCandidates) > 0 {
		// Build reverse map from IETF tag (lowercase) → ISO 639-2 code.
		ietfToISO := make(map[string]string, len(ietfMap))
		for iso, ietf := range ietfMap {
			ietfToISO[strings.ToLower(ietf)] = iso
		}
		autoAll := false
		ietfAll := "en-US"
		for _, uc := range undCandidates {
			basename := filepath.Base(uc.path)
			ietfChoice := ietfAll
			if !autoAll {
				ans, _ := prompter.Ask(fmt.Sprintf("[UND]    %s: audio track language is 'und'. Enter IETF language [en-US]: ", basename))
				ans = strings.TrimSpace(ans)
				if ans == "" || strings.EqualFold(ans, "a") {
					if strings.EqualFold(ans, "a") {
						autoAll = true
					}
					ietfChoice = "en-US"
				} else {
					ietfChoice = ans
				}
			}
			isoCode, ok := ietfToISO[strings.ToLower(ietfChoice)]
			if !ok {
				fmt.Printf("[SKIP]   %s (unknown language %q)\n", basename, ietfChoice)
				continue
			}
			canonicalIETF := ietfMap[isoCode]
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

	// Sequential trim phase: prompt per file, then remux.
	if remuxer != nil {
		autoAll := false
		for _, tc := range trimCandidates {
			plan := PlanTrim(tc.info, tc.originLang)
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
