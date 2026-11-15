package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	if err := json.Unmarshal(data, &paths); err != nil {
		fmt.Printf("[WARN]   ignoring unreadable %s: %v\n", filepath.Join(stateDir, failedCacheFile), err)
		return nil
	}
	return paths
}

// saveFailedCache writes the list of failed paths to stateDir.
func saveFailedCache(stateDir string, paths []string) error {
	return writeStateJSON(stateDir, failedCacheFile, paths)
}

// unscannedFailures returns the entries of oldFails that a full scan of root
// didn't retry: not in scanned, and outside root, inside a folder in
// unreadable, or still on disk (e.g. skipped as older than the cutoff).
func unscannedFailures(oldFails, scanned []string, root string, unreadable []string) []string {
	seen := make(map[string]bool, len(scanned))
	for _, p := range scanned {
		seen[pathKey(p)] = true
	}
	var out []string
	for _, p := range oldFails {
		if seen[pathKey(p)] {
			continue
		}
		_, err := os.Stat(p)
		if !within(p, root) || slices.ContainsFunc(unreadable, func(dir string) bool { return within(p, dir) }) || !os.IsNotExist(err) {
			out = append(out, p)
		}
	}
	return out
}

// pathKey normalises p for comparison; Windows paths are case-insensitive.
func pathKey(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	absPath, err1 := filepath.Abs(path)
	absDir, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeStateJSON writes v as indented JSON to name in stateDir, creating the
// directory if needed.
func writeStateJSON(stateDir, name string, v any) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(stateDir, name), data)
}

// fetchOrigins pre-computes OriginInfo for every unique content-root directory
// in filesToScan. Results are keyed by the content-root directory name
// (first path component relative to libraryRoot). TMDB calls are sequential to
// respect rate limits. Entries that produce no match are absent from the map.
//
// When the sibling cache (mkv-tvshow's or mkv-movie's) has no ID for a
// directory, fetchOrigins searches TMDB interactively: the top result is shown
// and the user is prompted to accept it, enter a manual TMDB ID, or skip. An
// ID whose origin TMDB returns is saved back to the sibling cache so future
// runs (and mkv-tvshow / mkv-movie) can skip the search step.
func fetchOrigins(filesToScan []string, libraryRoot, libType string, tmdb TMDBOriginClient, prompter Prompter) map[string]OriginInfo {
	origins := make(map[string]OriginInfo)
	if tmdb == nil || libraryRoot == "" || (libType != "tv" && libType != "movie") {
		return origins
	}

	sibling := loadSiblingCaches(libType)
	defer sibling.finish()
	var autoAll, skipAll bool

	var dirs []string
	dirFiles := map[string][]string{} // content-root dir → its files
	for _, path := range filesToScan {
		dir := contentRootDir(path, libraryRoot)
		if dir == "" {
			continue
		}
		if _, seen := dirFiles[dir]; !seen {
			dirs = append(dirs, dir)
		}
		dirFiles[dir] = append(dirFiles[dir], path)
	}
	for _, dir := range dirs {
		title, year := dirTitleYear(dir)
		if title == "" {
			continue
		}
		root := filepath.Join(libraryRoot, dir)

		var ids []string   // best first
		var titleOnly bool // a movie found by title key, with no file mapped to it
		getOrigin := tmdb.GetMovieOriginByID
		if libType == "tv" {
			if id := sibling.lookupTV(dir, libraryRoot); id != "" {
				ids = []string{id}
			}
			getOrigin = tmdb.GetTVOriginByID
		} else {
			ids, titleOnly = sibling.lookupMovie(title, year, root)
		}
		cached := len(ids) > 0
		if !cached && !skipAll {
			if id := resolveTMDBID(dir, title, year, libType, tmdb, prompter, &autoAll, &skipAll); id != "" {
				ids = []string{id}
			}
		}

		var id string
		var info OriginInfo
		var found bool
		var err error
		for i, next := range ids {
			if i > 0 {
				fmt.Printf("[TMDB]   %s: no original language from TMDB for id %s, trying id %s\n", dir, id, next)
			}
			id = next
			if info, found, err = getOrigin(id); err != nil || found {
				break
			}
		}
		if err != nil {
			fmt.Printf("[TMDB]   %s: %v\n", dir, err)
			continue
		}
		if !found && cached {
			src := movieSibling
			if libType == "tv" {
				src = tvShowSibling
			}
			fmt.Printf("[TMDB]   %s: no original language from TMDB for %s id %s (from %s, %s); fix or remove it there\n",
				dir, src.items, id, src.name, src.file)
			continue
		}
		if !found {
			fmt.Printf("[TMDB]   %s: no match found\n", dir)
			continue
		}
		origins[dir] = info
		switch {
		case !cached && libType == "tv":
			sibling.rememberTV(dir, root, id)
		// A root whose files mkv-movie maps to IDs that didn't settle it, or
		// that holds movie folders, may be several movies: the answer is for
		// this run only, not saved under its name or onto its files. One whose
		// name gives a year is one movie, even split across subfolders (discs).
		case !cached && (sibling.rootMapped(root) || year == "" && groupsMovies(root)):
			fmt.Printf("[TMDB]   %s: id %s used for this run only, not saved: the folder may hold several movies\n", dir, id)
		case !cached:
			sibling.rememberMovie(dir, title, year, id, dirFiles[dir], false)
		// A title key alone (as older mkv-audit saved) is pruned by
		// mkv-movie --clean, so map the root's file to it too, with the
		// key's value as stored. Only a sole .mkv: a collection's files are
		// different movies.
		case titleOnly && slices.ContainsFunc(dirFiles[dir], isMKV) && soleMKV(root):
			sibling.rememberMovie(dir, title, year, sibling.titleKeyID(title, year), dirFiles[dir], true)
		}
	}
	return origins
}

// resolveTMDBID searches TMDB for dir and prompts the user to confirm the top
// result or enter a manual ID, and returns the confirmed ID.
// Returns "" when the user skips or no prompter is available. When the
// prompter fails (stdin closed), it sets skipAll, so no more are asked.
func resolveTMDBID(dir, title, year, libType string, tmdb TMDBOriginClient, prompter Prompter, autoAll, skipAll *bool) string {
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

	if topID != "" && *autoAll {
		return topID
	}
	prompt := fmt.Sprintf("[TMDB]   %s: found %q (%s) [id=%s]. Accept? (y/n/a=all/s=skip all/<id>): ", dir, topName, topYear, topID)
	if topID == "" {
		prompt = fmt.Sprintf("[TMDB]   %s: no results found. Enter TMDB id or blank to skip: ", dir)
	}
	for {
		ans, err := prompter.Ask(prompt)
		if err != nil {
			*skipAll = true
			fmt.Printf("\n[TMDB]   no input (%v), so TMDB lookups are skipped for the rest of the run\n", err)
			return ""
		}
		ans = strings.TrimSpace(ans)
		if topID == "" {
			if ans == "" {
				return ""
			}
		} else {
			switch strings.ToLower(ans) {
			case "y", "":
				return topID
			case "a":
				*autoAll = true
				return topID
			case "n":
				return ""
			case "s":
				*skipAll = true
				return ""
			}
		}
		if id, ok := normalizeTMDBID(ans); ok {
			return id
		}
		fmt.Printf("[TMDB]   %q is not a TMDB id, which is a positive whole number\n", ans)
	}
}

// normalizeTMDBID returns s as TMDB writes the ID, without leading zeros, and
// whether s is one: ASCII digits, not all zero. The tools key their caches by
// the ID's text, so "0603" would never match TMDB's "603".
func normalizeTMDBID(s string) (string, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return "", false
	}
	s = strings.TrimLeft(s, "0")
	return s, s != ""
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
	// ietfMap is the file's resolved ISO 639-2/B → IETF map, used to retag
	// a mismatched audio track.
	ietfMap map[string]string
	// accountLangs are the ISO 639-2/B audio languages the Plex accounts are
	// configured to watch this title in, from the sibling tool's manual_data.json.
	accountLangs []string
}

// retagOnlyAudio offers to set a file's only audio track to the original
// language: language, plus language-ietf when the language has an IETF mapping.
// Answering "a" sets *all so later files are retagged without asking.
func retagOnlyAudio(tc trimCandidate, track Track, editor MKVEditor, prompter Prompter, all *bool, stats *Stats) {
	basename := filepath.Base(tc.path)
	ietf, hasIETF := tc.ietfMap[tc.originLang]
	if !*all {
		target := tc.originLang
		if hasIETF {
			target += " (" + ietf + ")"
		}
		ans, _ := prompter.Ask(fmt.Sprintf("[RETAG]  %s\n    audio track: %q → %s\n  Retag as the original language? (y/n/a=all): ",
			basename, normalizeLanguage(track.Properties.Language), target))
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "a":
			*all = true
		case "y":
			// proceed
		default:
			fmt.Printf("[SKIP]   %s (retag skipped)\n", basename)
			return
		}
	}
	kv := []string{"language", tc.originLang}
	if hasIETF {
		kv = append(kv, "language-ietf", ietf)
	}
	if err := editor.Apply(tc.path, trackEdit(track.ID, kv...)); err != nil {
		fmt.Printf("[ERR]    %s: retag: %v\n", basename, err)
		stats.Failed++
		stats.FailedPaths = append(stats.FailedPaths, tc.path)
		return
	}
	fmt.Printf("[RETAG]  %s: audio language set to %s\n", basename, tc.originLang)
	stats.LangFixes++
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
// converter remuxes other video containers (see convertExts) to MKV before
// processing; pass nil to process only .mkv and .mka files.
func RunAll(root, stateDir string, lib *Library, ident MKVIdentifier, editor MKVEditor, prompter Prompter, tmdb TMDBOriginClient, remuxer MKVRemuxer, converter MKVConverter, cutoffDate time.Time) Stats {
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

	fullScan := len(filesToScan) == 0
	var unreadable []string
	if fullScan {
		// The callback never returns an error, so neither does WalkDir.
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				fmt.Printf("[WARN]   skipping unreadable %s: %v\n", path, err)
				unreadable = append(unreadable, path)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			lower := strings.ToLower(d.Name())
			if !strings.HasSuffix(lower, ".mkv") && !strings.HasSuffix(lower, ".mka") &&
				(converter == nil || !isConvertible(path)) {
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

	// Convert other containers to MKV and scan the result in their place. An
	// earlier failure on a source counts as retried even if it's skipped now.
	attempted := filesToScan
	if converter != nil {
		filesToScan = convertAll(filesToScan, converter, &stats)
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
					ietfMap:      ietfMap,
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
		autoAll, retagAll := false, false
		for _, tc := range trimCandidates {
			// Reflect any lang fixes applied above so the plan doesn't drop a
			// track whose language was just corrected on disk.
			if fixes, ok := appliedLangFixes[tc.path]; ok {
				tc.info.Tracks = applyLangFixes(tc.info.Tracks, fixes)
			}
			plan := PlanTrim(tc.info, tc.originLang, tc.accountLangs)
			basename := filepath.Base(tc.path)
			if len(plan.MismatchedAudio) > 0 {
				langs := make([]string, len(plan.MismatchedAudio))
				for i, t := range plan.MismatchedAudio {
					langs[i] = normalizeLanguage(t.Properties.Language)
				}
				fmt.Printf("[WARN]   %s: no audio track is the original language %s or an account language (audio: %s); keeping all audio, check the language tags\n",
					basename, tc.originLang, strings.Join(langs, ", "))
			}
			// With one audio track the tag is the likely mistake, so offer to
			// fix it. With several it isn't clear which one to retag.
			if len(plan.MismatchedAudio) == 1 {
				retagOnlyAudio(tc, plan.MismatchedAudio[0], editor, prompter, &retagAll, &stats)
			}
			if !plan.NeedsChange {
				continue
			}
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

	// Persist failure list and unknown codecs. Earlier failures a full scan
	// didn't retry stay listed.
	failed := stats.FailedPaths
	if fullScan {
		failed = slices.Concat(failed, unscannedFailures(oldFails, slices.Concat(attempted, filesToScan), root, unreadable))
	}
	if len(failed) > 0 {
		if err := saveFailedCache(stateDir, failed); err != nil {
			fmt.Printf("[WARN]   could not save %s: %v\n", filepath.Join(stateDir, failedCacheFile), err)
		}
	} else {
		os.Remove(filepath.Join(stateDir, failedCacheFile))
	}

	if len(unknownCodecs) > 0 {
		var list []string
		for c := range unknownCodecs {
			list = append(list, c)
		}
		if err := writeStateJSON(stateDir, unknownCodecsFile, list); err != nil {
			fmt.Printf("[WARN]   could not save new formats %v: %v\n", list, err)
		} else {
			fmt.Printf("New formats found: %s\n", filepath.Join(stateDir, unknownCodecsFile))
		}
	}

	return stats
}
