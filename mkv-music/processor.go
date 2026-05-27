package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	videoExts     = []string{".mkv", ".mp4", ".avi", ".mov", ".vob"}
	audioExts     = []string{".flac", ".mp3", ".m4a", ".wav", ".ogg", ".wma", ".mka", ".dsf"}
	videoSuffixes = []string{"-behindthescenes", "-concert", "-interview", "-live", "-lyrics", "-video"}
)

// Stats holds the counts of actions taken during a scan.
type Stats struct {
	Renamed    int
	Skipped    int
	Unmatched  int
	SkippedOld int
}

// IgnoreData holds ignore patterns used when fuzzy-matching video filenames to
// audio filenames. Global patterns are applied to every file; FilePatterns
// entries are keyed by video filename and applied only to that file.
type IgnoreData struct {
	Global       []string            `json:"global,omitempty"`
	FilePatterns map[string][]string `json:"file_patterns,omitempty"`
}

// Processor walks a music directory, matches video files to audio files, and
// interactively prompts the user to rename mismatches.
type Processor struct {
	root            string
	ignoreData      IgnoreData
	ignoreFile      string
	prompter        Prompter
	renamer         func(oldPath, newPath string) error
	patternAppender func(ignoreFile, videoFile, pattern string) error
	tagger          MusicVideoTagger // nil = tag copying disabled
	timer           FileTimer        // nil = timestamp syncing disabled
	cutoffDate      time.Time
	plex            PlexClient          // nil = Plex refresh disabled
	touchedArtists  map[string]struct{} // artist dir names touched this run
}

// NewProcessor creates a Processor for the given root directory.
// ignoreFile is the path to the JSON ignore-patterns file; pass "" to disable
// pattern persistence.
func NewProcessor(root, ignoreFile string, prompter Prompter) *Processor {
	return &Processor{
		root:            root,
		ignoreFile:      ignoreFile,
		prompter:        prompter,
		renamer:         os.Rename,
		patternAppender: defaultPatternAppender,
		touchedArtists:  make(map[string]struct{}),
	}
}

// markArtistTouched records that the artist whose folder contains dir was
// modified this run, so a Plex refresh can be triggered at the end.
func (p *Processor) markArtistTouched(dir string) {
	rel, err := filepath.Rel(p.root, dir)
	if err != nil {
		return
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	artist := parts[0]
	if artist == "" || artist == "." {
		return
	}
	p.touchedArtists[artist] = struct{}{}
}

// refreshPlexArtists triggers a Plex metadata refresh for every artist dir
// that was touched during this run. No-op when Plex is not configured.
func (p *Processor) refreshPlexArtists() {
	if p.plex == nil || len(p.touchedArtists) == 0 {
		return
	}
	for artist := range p.touchedArtists {
		ratingKey, err := p.plex.FindArtistByTitle(artist)
		if err != nil {
			if err == ErrArtistNotFound {
				fmt.Printf("  [Plex] Artist not found in library: %q\n", artist)
			} else {
				fmt.Printf("  [Plex] Error finding artist %q: %v\n", artist, err)
			}
			continue
		}
		if err := p.plex.RefreshMetadata(ratingKey); err != nil {
			fmt.Printf("  [Plex] Refresh failed for %q: %v\n", artist, err)
		} else {
			fmt.Printf("  [Plex] Refreshed artist: %q\n", artist)
		}
		if err := p.plex.AnalyzeMedia(ratingKey); err != nil {
			fmt.Printf("  [Plex] Analyze failed for %q: %v\n", artist, err)
		} else {
			fmt.Printf("  [Plex] Media analysis triggered for %q\n", artist)
		}
	}
}

// defaultPatternAppender adds pattern to the file-specific section for
// videoFile in the JSON ignore file at ignoreFile.
func defaultPatternAppender(ignoreFile, videoFile, pattern string) error {
	var data IgnoreData
	if existing, err := os.ReadFile(ignoreFile); err == nil {
		json.Unmarshal(existing, &data)
	}
	if data.FilePatterns == nil {
		data.FilePatterns = make(map[string][]string)
	}
	data.FilePatterns[videoFile] = append(data.FilePatterns[videoFile], pattern)
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ignoreFile, b, 0644)
}

// LoadIgnorePatterns reads ignore patterns from the JSON ignore file.
func (p *Processor) LoadIgnorePatterns() error {
	if p.ignoreFile == "" {
		return nil
	}
	data, err := os.ReadFile(p.ignoreFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &p.ignoreData)
}

// effectivePatterns returns the combined ignore patterns for videoFile:
// global patterns plus any file-specific patterns for that filename.
// Legacy entries may be keyed by bare filename; fall back to that when the
// absolute-path lookup finds nothing.
func (p *Processor) effectivePatterns(videoPath string) []string {
	patterns := append([]string{}, p.ignoreData.Global...)
	patterns = append(patterns, p.ignoreData.FilePatterns[videoPath]...)
	if base := filepath.Base(videoPath); base != videoPath {
		patterns = append(patterns, p.ignoreData.FilePatterns[base]...)
	}
	return patterns
}

// buildAudioData returns a map of audio basename -> display name.
// Only audio files whose basename contains " - " are included.
// The display name is: base + " - " + part-after-first-separator.
func buildAudioData(dirFiles []string) map[string]string {
	data := make(map[string]string)
	for _, f := range dirFiles {
		ext := strings.ToLower(filepath.Ext(f))
		if !containsStr(audioExts, ext) {
			continue
		}
		base := strings.TrimSuffix(f, filepath.Ext(f))
		parts := strings.SplitN(base, " - ", 2)
		if len(parts) > 1 {
			data[base] = base + " - " + parts[1]
		}
	}
	return data
}

// buildAudioPaths returns a map of audio basename (without extension) to the
// full filename (with extension) for each audio file that has a " - " separator.
// It mirrors the filtering logic in buildAudioData so the two maps share keys.
func buildAudioPaths(dirFiles []string) map[string]string {
	m := make(map[string]string)
	for _, f := range dirFiles {
		ext := strings.ToLower(filepath.Ext(f))
		if !containsStr(audioExts, ext) {
			continue
		}
		base := strings.TrimSuffix(f, filepath.Ext(f))
		if parts := strings.SplitN(base, " - ", 2); len(parts) > 1 {
			m[base] = f
		}
	}
	return m
}

// cleanVideoBase strips the last occurrence of a known suffix from base.
// Returns the cleaned name, the matched suffix, and any text that followed
// the suffix (e.g. " (Second Version)"). All three are "" when no suffix matches.
func cleanVideoBase(base string) (clean, suffix, postSuffix string) {
	for _, s := range videoSuffixes {
		if strings.Contains(base, s) {
			idx := strings.LastIndex(base, s)
			return base[:idx], s, base[idx+len(s):]
		}
	}
	return base, "", ""
}

// trackNumber returns the text before the first " - " separator, or "".
func trackNumber(name string) string {
	if idx := strings.Index(name, " - "); idx >= 0 {
		return name[:idx]
	}
	return ""
}

// computeDiff finds the part of cleanVideo that differs from matchedBase.
func computeDiff(cleanVideo, matchedBase string) string {
	diff := strings.TrimSpace(strings.Replace(cleanVideo, matchedBase, "", 1))
	if diff == "" && strings.HasPrefix(cleanVideo, matchedBase) {
		diff = strings.TrimSpace(cleanVideo[len(matchedBase):])
	}
	return diff
}

// findMatch fuzzy-matches cleanVideoIgnored against audio keys.
// It first tries keys that share the same track-number prefix, then falls back.
func findMatch(cleanVideoIgnored string, audioData map[string]string) (string, bool) {
	choices := mapKeys(audioData)
	track := trackNumber(cleanVideoIgnored)
	if track != "" {
		var trackMatches []string
		for _, k := range choices {
			if strings.HasPrefix(k, track) {
				trackMatches = append(trackMatches, k)
			}
		}
		if len(trackMatches) > 0 {
			if m, ok := getCloseMatch(cleanVideoIgnored, trackMatches, 0.3); ok {
				return m, true
			}
		}
	}
	return getCloseMatch(cleanVideoIgnored, choices, 0.3)
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// findIgnoredPatterns returns the trimmed, space-joined set of ignore patterns
// that appear in cleanVideo. This is appended to the audio title when copying
// tags so that e.g. " (Alternate Version)" survives in the MKV title tag.
// videoFile is the video filename used to look up file-specific patterns.
func (p *Processor) findIgnoredPatterns(cleanVideo, videoPath string) string {
	var found []string
	for _, pat := range p.effectivePatterns(videoPath) {
		if strings.Contains(cleanVideo, pat) {
			if t := strings.TrimSpace(pat); t != "" {
				found = append(found, t)
			}
		}
	}
	return strings.Join(found, " ")
}

// copyTagsIfConfigured copies tags from the matched audio file to mkvPath when
// a tagger is configured. cleanVideo is the video base before ignore-pattern
// stripping; postSuffix is any text after the recognized video suffix (e.g.
// " (Second Version)"). Both are used to build the MKV title suffix.
func (p *Processor) copyTagsIfConfigured(mkvPath, matchKey, cleanVideo, postSuffix, videoDir, videoPath string, audioPaths map[string]string) {
	if p.tagger == nil || audioPaths == nil {
		return
	}
	audioFilename, ok := audioPaths[matchKey]
	if !ok {
		return
	}
	audioFullPath := filepath.Join(videoDir, audioFilename)
	titleSuffix := p.findIgnoredPatterns(cleanVideo, videoPath)
	if t := strings.TrimSpace(postSuffix); t != "" {
		if titleSuffix == "" {
			titleSuffix = t
		} else {
			titleSuffix += " " + t
		}
	}
	if match, err := p.tagger.TagsMatchMKV(audioFullPath, mkvPath, titleSuffix); err == nil && match {
		fmt.Printf("    [TAG] Already up to date: %s\n", filepath.Base(mkvPath))
		p.syncTimestamps(mkvPath)
		return
	}
	if err := p.tagger.CopyTagsToMKV(audioFullPath, mkvPath, titleSuffix); err != nil {
		fmt.Printf("    [WARN] tag copy failed for %s: %v\n", filepath.Base(mkvPath), err)
	} else {
		fmt.Printf("    [TAG] Copied tags from %s\n", audioFilename)
		p.markArtistTouched(videoDir)
		p.syncTimestamps(mkvPath)
	}
}

// syncTimestamps sets mkvPath's modified and created timestamps to match the
// date stored in the MKV container header. No-op if timer or tagger is nil,
// or if the MKV has no date header.
func (p *Processor) syncTimestamps(mkvPath string) {
	if p.timer == nil || p.tagger == nil {
		return
	}
	dt, ok := p.tagger.MKVDate(mkvPath)
	if !ok {
		return
	}
	dt = dt.Local()
	if p.timer.CreationTimeNeedsUpdate(mkvPath, dt.Unix()) {
		if err := p.timer.SetCreationTime(mkvPath, dt); err != nil {
			fmt.Printf("    [WARN] set ctime failed for %s: %v\n", filepath.Base(mkvPath), err)
		}
	}
	if err := p.timer.SetTimes(mkvPath, dt); err != nil {
		fmt.Printf("    [WARN] set mtime failed for %s: %v\n", filepath.Base(mkvPath), err)
	}
}

// processVideo handles one video file: matches it to audio data, prints a
// mismatch if needed, and interactively renames/ignores/skips.
// Returns "renamed", "skipped", or "" (no action / no match).
func (p *Processor) processVideo(videoDir, videoFile string, audioData, audioPaths map[string]string) string {
	videoPath := filepath.Join(videoDir, videoFile)
	ext := filepath.Ext(videoFile)
	videoBase := strings.TrimSuffix(videoFile, ext)
	cleanVideo, foundSuffix, postSuffix := cleanVideoBase(videoBase)
	cleanVideoIgnored := removeIgnoredParts(cleanVideo, p.effectivePatterns(videoPath))
	postSuffixIgnored := removeIgnoredParts(postSuffix, p.effectivePatterns(videoPath))

	matchKey, ok := findMatch(cleanVideoIgnored, audioData)
	if !ok {
		fmt.Printf("[WARN] No audio match found: %s\n", videoFile)
		return "unmatched"
	}

	matchedBase := audioData[matchKey]
	effectiveSuffix := foundSuffix
	if effectiveSuffix == "" {
		effectiveSuffix = "-video"
	}
	targetName := matchedBase + effectiveSuffix
	currentName := cleanVideoIgnored + effectiveSuffix + postSuffixIgnored
	fullPath := videoPath
	if targetName == currentName {
		// Filename is already correct — just copy tags.
		p.copyTagsIfConfigured(fullPath, matchKey, cleanVideo, postSuffix, videoDir, videoPath, audioPaths)
		return ""
	}

	fmt.Printf("\n[?] UNMATCHED VIDEO: %s\n", videoFile)
	fmt.Printf("    SUGGESTED MATCH: %s%s\n", targetName, ext)
	fmt.Printf("    Current: %s\n", cleanVideo+postSuffix)
	fmt.Printf("    Expected: %s\n", matchedBase)

	choice, err := p.prompter.Ask("    (r)ename, (i)gnore pattern, (s)kip: ")
	if err != nil {
		return ""
	}
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "r":
		newPath := filepath.Join(videoDir, targetName+ext)
		if err := p.renamer(fullPath, newPath); err != nil {
			fmt.Printf("    [ERROR] Could not rename: %v\n", err)
		} else {
			fmt.Printf("    [OK] Renamed to: %s%s\n", targetName, ext)
			p.markArtistTouched(videoDir)
			p.copyTagsIfConfigured(newPath, matchKey, cleanVideo, postSuffix, videoDir, videoFile, audioPaths)
			return "renamed"
		}
	case "i":
		diff := computeDiff(cleanVideo, matchedBase)
		if diff == "" {
			diff = strings.TrimSpace(postSuffixIgnored)
		}
		pattern, err := p.prompter.Ask(fmt.Sprintf("    Enter the part to ignore (default: '%s'): ", diff))
		if err != nil {
			return ""
		}
		if strings.TrimSpace(pattern) == "" {
			pattern = diff
		}
		if pattern != "" {
			if p.ignoreData.FilePatterns == nil {
				p.ignoreData.FilePatterns = make(map[string][]string)
			}
			p.ignoreData.FilePatterns[videoPath] = append(p.ignoreData.FilePatterns[videoPath], pattern)
			if err := p.patternAppender(p.ignoreFile, videoPath, pattern); err != nil {
				fmt.Printf("    [ERROR] Could not save pattern: %v\n", err)
			} else {
				fmt.Printf("    [OK] Added '%s' to ignore list for %s\n", pattern, videoFile)
			}
			reCheckedClean := removeIgnoredParts(cleanVideo, p.effectivePatterns(videoPath))
			reCheckedPost := removeIgnoredParts(postSuffix, p.effectivePatterns(videoPath))
			if reCheckedClean+effectiveSuffix+reCheckedPost == targetName {
				fmt.Println("    [OK] File now matches after ignoring pattern!")
				p.copyTagsIfConfigured(fullPath, matchKey, cleanVideo, postSuffix, videoDir, videoPath, audioPaths)
			} else {
				fmt.Println("    Still doesn't match after ignoring")
			}
		}
	default:
		fmt.Println("    [SKIP] Kept original name.")
		return "skipped"
	}
	return ""
}

// Run walks root, groups files by directory, and processes each video file.
func (p *Processor) Run() (Stats, []string, []string) {
	var stats Stats
	var skippedVideos []string
	var unmatchedVideos []string

	dirOrder := []string{}
	dirFiles := map[string][]string{}
	filepath.WalkDir(p.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		dir := filepath.Dir(path)
		if _, seen := dirFiles[dir]; !seen {
			dirOrder = append(dirOrder, dir)
		}
		dirFiles[dir] = append(dirFiles[dir], d.Name())
		return nil
	})

	for _, dir := range dirOrder {
		files := dirFiles[dir]
		audioData := buildAudioData(files)
		audioPaths := buildAudioPaths(files)
		for _, f := range files {
			ext := strings.ToLower(filepath.Ext(f))
			if !containsStr(videoExts, ext) {
				continue
			}
			if !p.cutoffDate.IsZero() {
				info, err := os.Stat(filepath.Join(dir, f))
				if err == nil && info.ModTime().Before(p.cutoffDate) {
					stats.SkippedOld++
					continue
				}
			}
			result := p.processVideo(dir, f, audioData, audioPaths)
			switch result {
			case "renamed":
				stats.Renamed++
			case "skipped":
				stats.Skipped++
				skippedVideos = append(skippedVideos, filepath.Join(dir, f))
			case "unmatched":
				stats.Unmatched++
				unmatchedVideos = append(unmatchedVideos, filepath.Join(dir, f))
			}
		}
	}
	p.refreshPlexArtists()
	return stats, skippedVideos, unmatchedVideos
}
