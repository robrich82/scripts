package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
	patternMover    func(ignoreFile, oldPath, newPath string) error
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
		patternMover:    defaultPatternMover,
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
	return writeIgnoreFile(ignoreFile, data)
}

// writeIgnoreFile replaces ignoreFile with data via a temp file and rename, so
// a failed write leaves the old file intact rather than truncated.
func writeIgnoreFile(ignoreFile string, data IgnoreData) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(ignoreFile), filepath.Base(ignoreFile)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil { // CreateTemp makes it 0600: keep the old file's mode
		mode := fs.FileMode(0644)
		if info, err := os.Stat(ignoreFile); err == nil {
			mode = info.Mode().Perm()
		}
		werr = os.Chmod(tmp.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), ignoreFile)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// defaultPatternMover moves oldPath's file-specific patterns (and any legacy
// entry keyed by its bare filename) to newPath in ignoreFile.
func defaultPatternMover(ignoreFile, oldPath, newPath string) error {
	if oldPath == newPath {
		return nil
	}
	existing, err := os.ReadFile(ignoreFile)
	if err != nil {
		return err
	}
	var data IgnoreData
	if err := json.Unmarshal(existing, &data); err != nil {
		return err
	}
	moved := slices.Concat(data.FilePatterns[oldPath], data.FilePatterns[filepath.Base(oldPath)])
	if len(moved) == 0 {
		return nil
	}
	for _, pat := range moved {
		if !slices.Contains(data.FilePatterns[newPath], pat) {
			data.FilePatterns[newPath] = append(data.FilePatterns[newPath], pat)
		}
	}
	delete(data.FilePatterns, oldPath)
	return writeIgnoreFile(ignoreFile, data)
}

// moveFilePatterns carries oldPath's file-specific patterns over to newPath
// after a rename, so they keep applying. A legacy bare-filename entry is copied
// and left in place, since it may apply to other files of that name.
func (p *Processor) moveFilePatterns(oldPath, newPath string) {
	moved := p.filePatterns(oldPath)
	if len(moved) == 0 || oldPath == newPath {
		return
	}
	for _, pat := range moved {
		if !slices.Contains(p.ignoreData.FilePatterns[newPath], pat) {
			p.ignoreData.FilePatterns[newPath] = append(p.ignoreData.FilePatterns[newPath], pat)
		}
	}
	delete(p.ignoreData.FilePatterns, oldPath)
	if p.ignoreFile == "" {
		return
	}
	if err := p.patternMover(p.ignoreFile, oldPath, newPath); err != nil {
		fmt.Printf("    [ERROR] Could not move ignore patterns in %s from %q to %q: %v\n", p.ignoreFile, oldPath, newPath, err)
	}
}

// filePatterns returns videoPath's file-specific patterns, including any
// legacy entry keyed by its bare filename.
func (p *Processor) filePatterns(videoPath string) []string {
	pats := append([]string{}, p.ignoreData.FilePatterns[videoPath]...)
	if base := filepath.Base(videoPath); base != videoPath {
		pats = append(pats, p.ignoreData.FilePatterns[base]...)
	}
	return pats
}

// keptText returns, space-separated and in name order, the trimmed
// file-specific patterns found in s. The user ignored them for this file to
// keep its name, so a rename keeps them too; global patterns ("[HD]") are
// dropped as before. Text the new title already has, or that sits inside other
// kept text, is skipped, as is a pattern that wouldn't match once re-placed as
// " "+text ("[Fan] "), since the next run would then strip it again.
func (p *Processor) keptText(s, videoPath, matchedBase string) string {
	var kept []string
	for _, pat := range p.filePatterns(videoPath) {
		t := strings.TrimSpace(pat)
		if t != "" && strings.Contains(s, pat) && strings.Contains(" "+t, pat) &&
			!strings.Contains(matchedBase, pat) && !containsWord(matchedBase, t) && !slices.Contains(kept, t) {
			kept = append(kept, t)
		}
	}
	kept = dropContained(kept)
	slices.SortStableFunc(kept, func(a, b string) int { return strings.Index(s, a) - strings.Index(s, b) })
	return strings.Join(kept, " ")
}

// withSpace returns " "+s, or "" when s is empty.
func withSpace(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
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

// cleanVideoBase strips the rightmost known suffix from base. A suffix followed
// by a letter is part of a word ("Long-lived"), not a suffix. Returns the
// cleaned name, the matched suffix, and any text that followed the suffix
// (e.g. " (Second Version)"). suffix and postSuffix are "" when none matches.
func cleanVideoBase(base string) (clean, suffix, postSuffix string) {
	idx := -1
	for _, s := range videoSuffixes {
		for i := strings.LastIndex(base, s); i > idx; i = strings.LastIndex(base[:i], s) {
			if r, _ := utf8.DecodeRuneInString(base[i+len(s):]); !unicode.IsLetter(r) {
				idx, suffix = i, s
				break
			}
		}
	}
	if idx < 0 {
		return base, "", ""
	}
	return base[:idx], suffix, base[idx+len(suffix):]
}

// videoRank orders a folder's videos so the likeliest owner of the standard
// name comes first: no extra text, then only ignored extra text ("[HD]"), then
// real extra text (" (Second Version)"); within each, a known suffix before none.
func (p *Processor) videoRank(dir, f string) int {
	clean, suffix, post := cleanVideoBase(strings.TrimSuffix(f, filepath.Ext(f)))
	pats := p.effectivePatterns(filepath.Join(dir, f))
	rank := 0
	if extraText(removeIgnoredParts(clean, pats)) != "" || removeIgnoredParts(post, pats) != "" {
		rank = 4
	} else if extraText(clean) != "" || strings.TrimSpace(post) != "" {
		rank = 2
	}
	if suffix == "" {
		rank++
	}
	return rank
}

// plainerVideoClaims reports whether another video in dir with no extra text
// matches matchKey. That video owns the standard name even while it still has
// its old name (it was skipped), so videoFile, which has extra text, must not
// take the name from it.
func (p *Processor) plainerVideoClaims(dir, videoFile, matchKey string, audioData map[string]string) bool {
	if p.videoRank(dir, videoFile) < 2 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		f := e.Name()
		if e.IsDir() || strings.EqualFold(f, videoFile) || !containsStr(videoExts, strings.ToLower(filepath.Ext(f))) ||
			p.videoRank(dir, f) >= 2 {
			continue
		}
		clean, _, _ := cleanVideoBase(strings.TrimSuffix(f, filepath.Ext(f)))
		if k, ok := findMatch(removeIgnoredParts(clean, p.effectivePatterns(filepath.Join(dir, f))), audioData); ok && k == matchKey {
			return true
		}
	}
	return false
}

// videoNameTaken reports whether a video other than self is named base with
// any video extension.
func videoNameTaken(dir, base, self string) bool {
	for _, ext := range videoExts {
		path := filepath.Join(dir, base+ext)
		if strings.EqualFold(path, self) {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// extraText returns what follows the repeated title in an "NN - Title - Title…"
// name ("(Special Music Video)"), or "". The longest repeated title wins, so a
// title containing " - " isn't cut short.
func extraText(name string) string {
	extra, _ := repeatedTitle(name)
	return extra
}

// repeatedTitle reports whether name has the "NN - Title - Title…" shape, and
// returns the text after the repeated title as extraText does.
func repeatedTitle(name string) (extra string, ok bool) {
	_, rest, found := strings.Cut(name, " - ")
	if !found {
		return "", false
	}
	for i := 0; ; {
		j := strings.Index(rest[i:], " - ")
		if j < 0 {
			return extra, ok
		}
		title, after := rest[:i+j], rest[i+j+3:]
		if len(after) >= len(title) && strings.EqualFold(after[:len(title)], title) {
			if tail := after[len(title):]; tail == "" || tail[0] == ' ' {
				extra, ok = strings.TrimSpace(tail), true
			}
		}
		i += j + 3
	}
}

// keepLeadingSpace puts orig's leading spaces back on stripped, which
// removeIgnoredParts trims ("-video (Alt)" must not become "-video(Alt)").
func keepLeadingSpace(orig, stripped string) string {
	if stripped == "" {
		return ""
	}
	return orig[:len(orig)-len(strings.TrimLeft(orig, " "))] + stripped
}

// sameFile reports whether info and path are the same file (volume and file
// ID, so hard links count as one), as with a case-only rename on a
// case-insensitive filesystem. Symlinks are compared as links, not targets.
func sameFile(info fs.FileInfo, path string) bool {
	other, err := os.Lstat(path)
	return err == nil && os.SameFile(info, other)
}

// trackNumber returns the text before the first " - " separator, or "".
func trackNumber(name string) string {
	if before, _, ok := strings.Cut(name, " - "); ok {
		return before
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
// An audio file with the same numeric track number wins regardless of title
// similarity, so a video whose title is stale (the audio was retitled) isn't
// pulled to another track that still carries the old title.
func findMatch(cleanVideoIgnored string, audioData map[string]string) (string, bool) {
	choices := mapKeys(audioData)
	// Sort so that when several candidates tie on similarity ratio, the same
	// one wins every run — map iteration order is otherwise randomised, making
	// match selection non-deterministic between invocations.
	slices.Sort(choices)
	if track := trackNumber(cleanVideoIgnored); isTrackNumber(track) {
		var trackMatches []string
		for _, k := range choices {
			if trackNumber(k) == track {
				trackMatches = append(trackMatches, k)
			}
		}
		// A number on every audio file ("311 - …", a folder of "01 - …" singles)
		// says nothing about which track this is.
		shared := len(trackMatches) > 1 && len(trackMatches) == len(choices)
		if m, ok := closestAudio(cleanVideoIgnored, trackMatches, audioData, 0); ok && !shared {
			return m, true
		}
	}
	return closestAudio(cleanVideoIgnored, choices, audioData, 0.3)
}

// closestAudio returns the audio key most similar to query. A query already in
// the "NN - T - T" shape is also scored against each display name, taking the
// better ratio, so a renamed video isn't pulled to a longer, similar title.
// Other queries use filenames only: display names would loosen the cutoff.
func closestAudio(query string, keys []string, audioData map[string]string, cutoff float64) (string, bool) {
	_, useDisplay := repeatedTitle(query)
	best, bestRatio := "", -1.0
	for _, k := range keys {
		r := sequenceRatio(query, k)
		if useDisplay {
			r = max(r, sequenceRatio(query, audioData[k]))
		}
		if r > bestRatio {
			best, bestRatio = k, r
		}
	}
	if bestRatio < cutoff {
		return "", false
	}
	return best, true
}

// isTrackNumber reports whether s looks like a track number ("01", "1-01"),
// as opposed to an artist name in an "Artist - Title" filename.
func isTrackNumber(s string) bool {
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r != '-' && r != '.':
			return false
		}
	}
	return hasDigit
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func containsStr(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

// ignoredPatternList returns the trimmed ignore patterns that appear in
// cleanVideo. They are appended to the audio title when copying tags so that
// e.g. " (Alternate Version)" survives in the MKV title tag.
func (p *Processor) ignoredPatternList(cleanVideo, videoPath string) []string {
	var found []string
	for _, pat := range p.effectivePatterns(videoPath) {
		if strings.Contains(cleanVideo, pat) {
			if t := strings.TrimSpace(pat); t != "" && !slices.Contains(found, t) {
				found = append(found, t)
			}
		}
	}
	return dropContained(found)
}

// containsWord reports whether s has word, case-insensitively and bounded by
// spaces or the ends of s ("Live" is in "Song Live", not in "Alive").
func containsWord(s, word string) bool {
	return strings.Contains(" "+strings.ToLower(s)+" ", " "+strings.ToLower(word)+" ")
}

// dropContained removes each text that sits inside another one in ts, so
// overlapping patterns (" (Alt)" and " (Alt) (Take 2)") add their text once.
func dropContained(ts []string) []string {
	return slices.DeleteFunc(slices.Clone(ts), func(t string) bool {
		return slices.ContainsFunc(ts, func(o string) bool { return o != t && strings.Contains(o, t) })
	})
}

// copyTagsIfConfigured copies tags from the matched audio file to mkvPath when
// a tagger is configured. cleanVideo is the video base before ignore-pattern
// stripping; postSuffix is extra title text (" (Second Version)" after the
// suffix, or kept "(Special Music Video)"). Both build the MKV title suffix.
func (p *Processor) copyTagsIfConfigured(mkvPath, matchKey, cleanVideo, postSuffix, videoDir, videoPath string, audioPaths map[string]string) {
	if p.tagger == nil || audioPaths == nil {
		return
	}
	audioFilename, ok := audioPaths[matchKey]
	if !ok {
		return
	}
	audioFullPath := filepath.Join(videoDir, audioFilename)
	t := strings.TrimSpace(postSuffix)
	_, audioTitle, _ := strings.Cut(matchKey, " - ")
	var parts []string
	for _, pat := range p.ignoredPatternList(cleanVideo, videoPath) {
		// Skip text already carried by postSuffix or by the audio's own title,
		// which the tag title starts with ("Song (Live)" + "(Live)").
		if !strings.Contains(t, pat) && !strings.Contains(audioTitle, pat) {
			parts = append(parts, pat)
		}
	}
	// ...and skip postSuffix when a pattern already carries it ("(Live)" in a
	// kept "(Live) [HD]").
	if t != "" && !slices.ContainsFunc(parts, func(part string) bool { return containsWord(part, t) }) {
		parts = append(parts, t)
	}
	titleSuffix := strings.Join(parts, " ")
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

// videoPlan is what processVideo decides for one video filename.
type videoPlan struct {
	matchKey, matchedBase                     string
	cleanVideo, postSuffix, postSuffixIgnored string
	targetName, nameSuffix, tagPost           string
	correct                                   bool // the name needs no change
}

// planVideo matches videoFile to audioData and works out its target name and
// title-tag inputs. ok is false when no audio file matches.
func (p *Processor) planVideo(videoDir, videoFile string, audioData map[string]string) (plan videoPlan, ok bool) {
	videoPath := filepath.Join(videoDir, videoFile)
	videoBase := strings.TrimSuffix(videoFile, filepath.Ext(videoFile))
	cleanVideo, foundSuffix, postSuffix := cleanVideoBase(videoBase)
	cleanVideoIgnored := removeIgnoredParts(cleanVideo, p.effectivePatterns(videoPath))
	postSuffixIgnored := removeIgnoredParts(postSuffix, p.effectivePatterns(videoPath))

	matchKey, ok := findMatch(cleanVideoIgnored, audioData)
	if !ok {
		return videoPlan{}, false
	}

	matchedBase := audioData[matchKey]
	effectiveSuffix := foundSuffix
	if effectiveSuffix == "" {
		effectiveSuffix = "-video"
	}
	plan = videoPlan{
		matchKey: matchKey, matchedBase: matchedBase,
		cleanVideo: cleanVideo, postSuffix: postSuffix, postSuffixIgnored: postSuffixIgnored,
		targetName: matchedBase + effectiveSuffix, nameSuffix: effectiveSuffix, tagPost: postSuffix,
	}
	currentName := cleanVideoIgnored + effectiveSuffix + postSuffixIgnored
	if plan.targetName == currentName || plan.targetName == videoBase {
		plan.correct = true
		return plan, true
	}

	// Another video already has the standard name (an unsuffixed one counts as
	// "-video"), or a plainer one will claim it: if this one has extra text
	// ("(Special Music Video)", " (Second Version)"), keep it in the name and
	// title tag. Without any, the standard name is offered and "r" refuses if
	// that exact file exists.
	keepClean, keepPost := p.keptText(cleanVideo, videoPath, matchedBase), p.keptText(postSuffix, videoPath, matchedBase)
	if videoNameTaken(videoDir, plan.targetName, videoPath) ||
		(effectiveSuffix == "-video" && videoNameTaken(videoDir, matchedBase, videoPath)) ||
		p.plainerVideoClaims(videoDir, videoFile, matchKey, audioData) {
		clean, post := cleanVideoIgnored, keepLeadingSpace(postSuffix, postSuffixIgnored)
		extra := extraText(clean)
		if extra == "" && post == "" { // only ignored text sets it apart: keep that
			clean, post = cleanVideo, postSuffix
			extra = extraText(clean)
		}
		if extra != "" && strings.HasSuffix(strings.ToLower(matchedBase), " "+strings.ToLower(extra)) {
			extra = "" // the new title already says it
		}
		if extra != "" || strings.TrimSpace(post) != "" {
			plan.targetName = matchedBase
			if extra != "" {
				plan.targetName += " " + extra
			}
			plan.targetName += effectiveSuffix + post
			plan.tagPost = extra + " " + strings.TrimSpace(postSuffix)
			plan.nameSuffix = foundSuffix // an unsuffixed file must still be renamed to add "-video"
			plan.correct = plan.targetName == clean+foundSuffix+post || plan.targetName == videoBase
			if !plan.correct && clean == cleanVideoIgnored { // put back what this file's own patterns stripped
				// Kept text may hold extra's text too, when a global pattern sits inside
				// it (" (Live) [HD]" around " [HD]"): then it replaces extra.
				if keepClean != "" && !strings.Contains(extra, keepClean) {
					if strings.Contains(keepClean, extra) {
						extra = keepClean
					} else {
						extra += " " + keepClean
					}
				}
				if keepPost != "" && !strings.Contains(post, keepPost) {
					if strings.Contains(keepPost, strings.TrimSpace(post)) {
						post = " " + keepPost
					} else {
						post += " " + keepPost
					}
				}
				plan.targetName = matchedBase + withSpace(extra) + effectiveSuffix + post
				plan.correct = plan.targetName == videoBase
			}
		}
	} else if keepClean != "" || keepPost != "" {
		plan.targetName = matchedBase + withSpace(keepClean) + effectiveSuffix + withSpace(keepPost)
		plan.correct = plan.targetName == videoBase
	}
	return plan, true
}

// processVideo handles one video file: matches it to audio data, prints a
// mismatch if needed, and interactively renames/ignores/skips.
// Returns "renamed", "skipped", or "" (no action / no match).
func (p *Processor) processVideo(videoDir, videoFile string, audioData, audioPaths map[string]string) string {
	videoPath := filepath.Join(videoDir, videoFile)
	ext := filepath.Ext(videoFile)
	plan, ok := p.planVideo(videoDir, videoFile, audioData)
	if !ok {
		fmt.Printf("[WARN] No audio match found: %s\n", videoFile)
		return "unmatched"
	}
	matchKey, matchedBase, cleanVideo, postSuffix := plan.matchKey, plan.matchedBase, plan.cleanVideo, plan.postSuffix
	targetName, tagPost := plan.targetName, plan.tagPost
	fullPath := videoPath
	if plan.correct {
		// Filename is already correct — just copy tags.
		p.copyTagsIfConfigured(fullPath, matchKey, cleanVideo, tagPost, videoDir, videoPath, audioPaths)
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
		if newPath != fullPath {
			// A case-only rename finds the file itself here; let only that through.
			if newInfo, statErr := os.Lstat(newPath); statErr == nil && !(strings.EqualFold(newPath, fullPath) && sameFile(newInfo, fullPath)) {
				// os.Rename would silently clobber an existing file (e.g. two
				// videos matching the same track). Refuse rather than destroy it.
				fmt.Printf("    [SKIP] Target already exists, not overwriting: %s%s\n", targetName, ext)
				return "skipped"
			}
		}
		if err := p.renamer(fullPath, newPath); err != nil {
			fmt.Printf("    [ERROR] Could not rename: %v\n", err)
		} else {
			fmt.Printf("    [OK] Renamed to: %s%s\n", targetName, ext)
			p.markArtistTouched(videoDir)
			p.moveFilePatterns(videoPath, newPath)
			// Tag from the new name, as the next run will, so the tag settles now.
			if next, ok := p.planVideo(videoDir, targetName+ext, audioData); ok && next.correct {
				p.copyTagsIfConfigured(newPath, next.matchKey, next.cleanVideo, next.tagPost, videoDir, newPath, audioPaths)
			} else {
				p.copyTagsIfConfigured(newPath, matchKey, cleanVideo, tagPost, videoDir, newPath, audioPaths) // the patterns moved there
			}
			return "renamed"
		}
	case "i":
		diff := computeDiff(cleanVideo, matchedBase)
		if diff == "" {
			diff = strings.TrimSpace(plan.postSuffixIgnored)
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
			// Re-plan as the next run will, since the target may now keep this text.
			if next, ok := p.planVideo(videoDir, videoFile, audioData); ok && next.correct {
				fmt.Println("    [OK] File now matches after ignoring pattern!")
				p.copyTagsIfConfigured(fullPath, next.matchKey, next.cleanVideo, next.tagPost, videoDir, videoPath, audioPaths)
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
		// Prompt for plain names first so they claim the standard name.
		slices.SortStableFunc(files, func(a, b string) int {
			return cmp.Compare(p.videoRank(dir, a), p.videoRank(dir, b))
		})
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
