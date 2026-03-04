package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	videoExts     = []string{".mkv", ".mp4", ".avi", ".mov", ".vob"}
	audioExts     = []string{".flac", ".mp3", ".m4a", ".wav", ".ogg", ".wma", ".mka", ".dsf"}
	videoSuffixes = []string{"-behindthescenes", "-concert", "-interview", "-live", "-lyrics", "-video"}
)

// Stats holds the counts of actions taken during a scan.
type Stats struct {
	Renamed int
	Skipped int
}

// Processor walks a music directory, matches video files to audio files, and
// interactively prompts the user to rename mismatches.
type Processor struct {
	root            string
	ignorePatterns  []string
	ignoreFile      string
	prompter        Prompter
	renamer         func(oldPath, newPath string) error
	patternAppender func(filePath, pattern string) error
	tagger          MusicVideoTagger // nil = tag copying disabled
}

// NewProcessor creates a Processor for the given root directory.
func NewProcessor(root string, prompter Prompter) *Processor {
	return &Processor{
		root:            root,
		ignoreFile:      filepath.Join(root, "ignore_patterns.txt"),
		prompter:        prompter,
		renamer:         os.Rename,
		patternAppender: defaultPatternAppender,
	}
}

func defaultPatternAppender(filePath, pattern string) error {
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, pattern)
	return err
}

// LoadIgnorePatterns reads ignore patterns from ignore_patterns.txt in root.
func (p *Processor) LoadIgnorePatterns() error {
	f, err := os.Open(p.ignoreFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			p.ignorePatterns = append(p.ignorePatterns, line)
		}
	}
	return scanner.Err()
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
// Returns the cleaned name and the matched suffix (or "" if none).
func cleanVideoBase(base string) (clean, suffix string) {
	for _, s := range videoSuffixes {
		if strings.Contains(base, s) {
			idx := strings.LastIndex(base, s)
			return base[:idx], s
		}
	}
	return base, ""
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
func (p *Processor) findIgnoredPatterns(cleanVideo string) string {
	var found []string
	for _, pat := range p.ignorePatterns {
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
// stripping and is used to detect which patterns should be appended to the title.
func (p *Processor) copyTagsIfConfigured(mkvPath, matchKey, cleanVideo, videoDir string, audioPaths map[string]string) {
	if p.tagger == nil || audioPaths == nil {
		return
	}
	audioFilename, ok := audioPaths[matchKey]
	if !ok {
		return
	}
	audioFullPath := filepath.Join(videoDir, audioFilename)
	titleSuffix := p.findIgnoredPatterns(cleanVideo)
	if match, err := p.tagger.TagsMatchMKV(audioFullPath, mkvPath, titleSuffix); err == nil && match {
		fmt.Printf("    [TAG] Already up to date: %s\n", filepath.Base(mkvPath))
		return
	}
	if err := p.tagger.CopyTagsToMKV(audioFullPath, mkvPath, titleSuffix); err != nil {
		fmt.Printf("    [WARN] tag copy failed for %s: %v\n", filepath.Base(mkvPath), err)
	} else {
		fmt.Printf("    [TAG] Copied tags from %s\n", audioFilename)
	}
}

// processVideo handles one video file: matches it to audio data, prints a
// mismatch if needed, and interactively renames/ignores/skips.
// Returns "renamed", "skipped", or "" (no action / no match).
func (p *Processor) processVideo(videoDir, videoFile string, audioData, audioPaths map[string]string) string {
	ext := filepath.Ext(videoFile)
	videoBase := strings.TrimSuffix(videoFile, ext)
	cleanVideo, foundSuffix := cleanVideoBase(videoBase)
	cleanVideoIgnored := removeIgnoredParts(cleanVideo, p.ignorePatterns)

	matchKey, ok := findMatch(cleanVideoIgnored, audioData)
	if !ok {
		return ""
	}

	matchedBase := audioData[matchKey]
	effectiveSuffix := foundSuffix
	if effectiveSuffix == "" {
		effectiveSuffix = "-video"
	}
	targetName := matchedBase + effectiveSuffix
	currentName := cleanVideoIgnored + effectiveSuffix
	fullPath := filepath.Join(videoDir, videoFile)
	if targetName == currentName {
		// Filename is already correct — just copy tags.
		p.copyTagsIfConfigured(fullPath, matchKey, cleanVideo, videoDir, audioPaths)
		return ""
	}

	fmt.Printf("\n[?] UNMATCHED VIDEO: %s\n", videoFile)
	fmt.Printf("    SUGGESTED MATCH: %s%s\n", targetName, ext)
	fmt.Printf("    Current: %s\n", cleanVideo)
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
			// Copy tags to the new path after a successful rename.
			p.copyTagsIfConfigured(newPath, matchKey, cleanVideo, videoDir, audioPaths)
			return "renamed"
		}
	case "i":
		diff := computeDiff(cleanVideo, matchedBase)
		pattern, err := p.prompter.Ask(fmt.Sprintf("    Enter the part to ignore (default: '%s'): ", diff))
		if err != nil {
			return ""
		}
		if strings.TrimSpace(pattern) == "" {
			pattern = diff
		}
		if pattern != "" {
			p.ignorePatterns = append(p.ignorePatterns, pattern)
			if err := p.patternAppender(p.ignoreFile, pattern); err != nil {
				fmt.Printf("    [ERROR] Could not save pattern: %v\n", err)
			} else {
				fmt.Printf("    [OK] Added '%s' to ignore list\n", pattern)
			}
			reChecked := removeIgnoredParts(cleanVideo, p.ignorePatterns)
			if reChecked == matchedBase {
				fmt.Println("    [OK] File now matches after ignoring pattern!")
				p.copyTagsIfConfigured(fullPath, matchKey, cleanVideo, videoDir, audioPaths)
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
func (p *Processor) Run() (Stats, []string) {
	var stats Stats
	var skippedVideos []string

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
			result := p.processVideo(dir, f, audioData, audioPaths)
			switch result {
			case "renamed":
				stats.Renamed++
			case "skipped":
				stats.Skipped++
				skippedVideos = append(skippedVideos, filepath.Join(dir, f))
			}
		}
	}
	return stats, skippedVideos
}
