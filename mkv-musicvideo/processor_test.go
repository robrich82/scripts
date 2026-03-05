package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakePrompter returns pre-queued answers in order.
type fakePrompter struct {
	answers []string
	idx     int
}

func (f *fakePrompter) Ask(_ string) (string, error) {
	if f.idx >= len(f.answers) {
		return "", nil
	}
	a := f.answers[f.idx]
	f.idx++
	return a, nil
}

// fakeTagger records calls made to CopyTagsToMKV.
type fakeTagger struct {
	calls     []taggerCall
	err       error
	tagsMatch bool // return value for TagsMatchMKV; default false → copy always runs
}

type taggerCall struct {
	audioPath   string
	mkvPath     string
	titleSuffix string
}

func (f *fakeTagger) TagsMatchMKV(audioPath, mkvPath, titleSuffix string) (bool, error) {
	return f.tagsMatch, nil
}

func (f *fakeTagger) CopyTagsToMKV(audioPath, mkvPath, titleSuffix string) error {
	f.calls = append(f.calls, taggerCall{audioPath, mkvPath, titleSuffix})
	return f.err
}

// --- buildAudioData tests ---

func TestBuildAudioData_SkipsNoSeparator(t *testing.T) {
	data := buildAudioData([]string{"SongTitle.flac", "Other.mp3"})
	if len(data) != 0 {
		t.Errorf("expected empty map, got %v", data)
	}
}

func TestBuildAudioData_BuildsMap(t *testing.T) {
	data := buildAudioData([]string{"Artist - Song Title.flac"})
	v, ok := data["Artist - Song Title"]
	if !ok || v != "Artist - Song Title - Song Title" {
		t.Errorf("got %v", data)
	}
}

func TestBuildAudioData_SkipsVideoExtensions(t *testing.T) {
	data := buildAudioData([]string{"Artist - Song.mkv"})
	if len(data) != 0 {
		t.Errorf("expected empty, got %v", data)
	}
}

func TestBuildAudioData_MultipleFiles(t *testing.T) {
	files := []string{
		"Artist - Song A.flac",
		"Artist - Song B.mp3",
		"NoSeparator.wav",
	}
	data := buildAudioData(files)
	if len(data) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(data), data)
	}
}

// --- cleanVideoBase tests ---

func TestCleanVideoBase_StripsVideoSuffix(t *testing.T) {
	clean, suffix := cleanVideoBase("Artist - Song-video")
	if clean != "Artist - Song" || suffix != "-video" {
		t.Errorf("clean=%q suffix=%q", clean, suffix)
	}
}

func TestCleanVideoBase_StripsLiveSuffix(t *testing.T) {
	clean, suffix := cleanVideoBase("Artist - Song-live")
	if clean != "Artist - Song" || suffix != "-live" {
		t.Errorf("clean=%q suffix=%q", clean, suffix)
	}
}

func TestCleanVideoBase_NoKnownSuffix(t *testing.T) {
	clean, suffix := cleanVideoBase("Artist - Song")
	if clean != "Artist - Song" || suffix != "" {
		t.Errorf("clean=%q suffix=%q", clean, suffix)
	}
}

// --- processVideo tests ---

func TestProcessVideo_NoAudioData_NoAction(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, "", &fakePrompter{})
	result := p.processVideo(dir, "Unknown - Song-video.mkv", map[string]string{}, nil)
	if result != "" {
		t.Errorf("got %q, want empty (no audio to match)", result)
	}
}

func TestProcessVideo_AlreadyCorrect_NoAction(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, "", &fakePrompter{})
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	// Video is already named correctly.
	result := p.processVideo(dir, "Artist - Song - Song-video.mkv", audioData, nil)
	if result != "" {
		t.Errorf("got %q, want empty (already correct)", result)
	}
}

func TestProcessVideo_RenameChosen_RenamesFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song-video.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}})
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}

	result := p.processVideo(dir, "Artist - Song-video.mkv", audioData, nil)
	if result != "renamed" {
		t.Errorf("got %q, want 'renamed'", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "Artist - Song - Song-video.mkv")); err != nil {
		t.Errorf("renamed file not found: %v", err)
	}
}

func TestProcessVideo_SkipChosen_ReturnsSkipped(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"s"}})
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}

	result := p.processVideo(dir, "Artist - Song-video.mkv", audioData, nil)
	if result != "skipped" {
		t.Errorf("got %q, want 'skipped'", result)
	}
}

func TestProcessVideo_IgnoreChosen_SavesPattern(t *testing.T) {
	dir := t.TempDir()
	// Video has " OFFICIAL" suffix beyond the audio match.
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", " OFFICIAL"}})

	var savedVideo, savedPattern string
	p.patternAppender = func(_, videoFile, pattern string) error {
		savedVideo = videoFile
		savedPattern = pattern
		return nil
	}

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	p.processVideo(dir, "Artist - Song OFFICIAL-video.mkv", audioData, nil)

	if savedPattern != " OFFICIAL" {
		t.Errorf("saved pattern %q, want ' OFFICIAL'", savedPattern)
	}
	if savedVideo != "Artist - Song OFFICIAL-video.mkv" {
		t.Errorf("saved videoFile %q, want 'Artist - Song OFFICIAL-video.mkv'", savedVideo)
	}
	patterns := p.ignoreData.FilePatterns["Artist - Song OFFICIAL-video.mkv"]
	if len(patterns) != 1 || patterns[0] != " OFFICIAL" {
		t.Errorf("FilePatterns=%v", p.ignoreData.FilePatterns)
	}
}

func TestProcessVideo_DefaultSuffix_WhenNoKnownSuffix(t *testing.T) {
	dir := t.TempDir()
	// Video has no known suffix → effective suffix defaults to "-video".
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"s"}})
	result := p.processVideo(dir, "Artist - Song.mkv", audioData, nil)
	if result != "skipped" {
		t.Errorf("got %q, want 'skipped'", result)
	}
}

// --- Run integration tests ---

func TestRun_AlreadyCorrect_NoStats(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song - Song-video.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{})
	stats, skipped := p.Run()
	if stats.Renamed != 0 || stats.Skipped != 0 || len(skipped) != 0 {
		t.Errorf("expected no actions, got %+v skipped=%v", stats, skipped)
	}
}

func TestRun_SkipsNonVideoFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{})
	stats, _ := p.Run()
	if stats.Renamed != 0 || stats.Skipped != 0 {
		t.Errorf("non-video files should produce no stats, got %+v", stats)
	}
}

func TestRun_CountsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	// Video name doesn't match the target name.
	os.WriteFile(filepath.Join(dir, "Artist - Song-video.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"s"}})
	stats, skipped := p.Run()
	if stats.Skipped != 1 {
		t.Errorf("Skipped=%d, want 1", stats.Skipped)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped paths len=%d, want 1", len(skipped))
	}
}

// --- LoadIgnorePatterns tests ---

func writeIgnoreJSON(t *testing.T, path string, data IgnoreData) {
	t.Helper()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIgnorePatterns_ReadsGlobal(t *testing.T) {
	dir := t.TempDir()
	ignoreFile := filepath.Join(dir, "ignore_patterns.json")
	writeIgnoreJSON(t, ignoreFile, IgnoreData{Global: []string{"pattern1", "pattern2"}})

	p := NewProcessor(dir, ignoreFile, &fakePrompter{})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.ignoreData.Global) != 2 || p.ignoreData.Global[0] != "pattern1" || p.ignoreData.Global[1] != "pattern2" {
		t.Errorf("got %v", p.ignoreData.Global)
	}
}

func TestLoadIgnorePatterns_MissingFile_OK(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, filepath.Join(dir, "ignore_patterns.json"), &fakePrompter{})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

func TestLoadIgnorePatterns_FilePatterns(t *testing.T) {
	dir := t.TempDir()
	ignoreFile := filepath.Join(dir, "ignore_patterns.json")
	writeIgnoreJSON(t, ignoreFile, IgnoreData{
		Global:       []string{"1080p"},
		FilePatterns: map[string][]string{"Song (Live).mkv": {"(Live)"}},
	})

	p := NewProcessor(dir, ignoreFile, &fakePrompter{})
	p.LoadIgnorePatterns()
	if len(p.ignoreData.Global) != 1 || p.ignoreData.Global[0] != "1080p" {
		t.Errorf("global=%v", p.ignoreData.Global)
	}
	if fp := p.ignoreData.FilePatterns["Song (Live).mkv"]; len(fp) != 1 || fp[0] != "(Live)" {
		t.Errorf("file_patterns=%v", p.ignoreData.FilePatterns)
	}
}

func TestLoadIgnorePatterns_EmptyIgnoreFile_NoOp(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, "", &fakePrompter{})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Errorf("expected nil when ignoreFile is empty, got %v", err)
	}
}

// --- tagger integration tests ---

func TestProcessVideo_AlreadyCorrect_CopiesTags(t *testing.T) {
	dir := t.TempDir()
	mkvFile := "Artist - Song - Song-video.mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.tagger = ft

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	audioPaths := map[string]string{"Artist - Song": "Artist - Song.flac"}

	result := p.processVideo(dir, mkvFile, audioData, audioPaths)
	if result != "" {
		t.Errorf("got %q, want empty (already correct)", result)
	}
	if len(ft.calls) != 1 {
		t.Fatalf("expected 1 tagger call, got %d", len(ft.calls))
	}
	if ft.calls[0].titleSuffix != "" {
		t.Errorf("titleSuffix = %q, want empty", ft.calls[0].titleSuffix)
	}
	if ft.calls[0].mkvPath != filepath.Join(dir, mkvFile) {
		t.Errorf("mkvPath = %q, want %q", ft.calls[0].mkvPath, filepath.Join(dir, mkvFile))
	}
}

func TestProcessVideo_Renamed_CopiesTagsToNewPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song-video.mkv"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}})
	p.tagger = ft

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	audioPaths := map[string]string{"Artist - Song": "Artist - Song.flac"}

	result := p.processVideo(dir, "Artist - Song-video.mkv", audioData, audioPaths)
	if result != "renamed" {
		t.Errorf("got %q, want 'renamed'", result)
	}
	if len(ft.calls) != 1 {
		t.Fatalf("expected 1 tagger call, got %d", len(ft.calls))
	}
	// Tags should be written to the new (renamed) path.
	wantMKV := filepath.Join(dir, "Artist - Song - Song-video.mkv")
	if ft.calls[0].mkvPath != wantMKV {
		t.Errorf("mkvPath = %q, want %q", ft.calls[0].mkvPath, wantMKV)
	}
}

func TestProcessVideo_Skipped_NoTagCopy(t *testing.T) {
	dir := t.TempDir()
	ft := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"s"}})
	p.tagger = ft

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	audioPaths := map[string]string{"Artist - Song": "Artist - Song.flac"}

	result := p.processVideo(dir, "Artist - Song-video.mkv", audioData, audioPaths)
	if result != "skipped" {
		t.Errorf("got %q, want 'skipped'", result)
	}
	if len(ft.calls) != 0 {
		t.Errorf("expected no tagger calls on skip, got %d", len(ft.calls))
	}
}

func TestFindIgnoredPatterns_MatchingPattern(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{Global: []string{" (Alternate Version)"}}}
	got := p.findIgnoredPatterns("09 - On My Way - On My Way (Alternate Version)", "")
	if got != "(Alternate Version)" {
		t.Errorf("got %q, want '(Alternate Version)'", got)
	}
}

func TestFindIgnoredPatterns_NoMatch(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{Global: []string{" (Live)"}}}
	got := p.findIgnoredPatterns("01 - Song - Song", "")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindIgnoredPatterns_MultipleMatches(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{Global: []string{" (Live)", " (Acoustic)"}}}
	got := p.findIgnoredPatterns("01 - Song (Live) (Acoustic)", "")
	if got != "(Live) (Acoustic)" {
		t.Errorf("got %q, want '(Live) (Acoustic)'", got)
	}
}

func TestFindIgnoredPatterns_FileSpecific(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{
		Global:       []string{"1080p"},
		FilePatterns: map[string][]string{"Song (Stone Version).mkv": {"(Stone Version)"}},
	}}
	// File-specific pattern should appear for matching file (global first, then file-specific).
	got := p.findIgnoredPatterns("Song (Stone Version) 1080p", "Song (Stone Version).mkv")
	if got != "1080p (Stone Version)" {
		t.Errorf("got %q, want '1080p (Stone Version)'", got)
	}
	// File-specific pattern should NOT appear for a different file.
	got2 := p.findIgnoredPatterns("Song (Stone Version) 1080p", "Other Song.mkv")
	if got2 != "1080p" {
		t.Errorf("got %q, want '1080p'", got2)
	}
}

func TestProcessVideo_AlreadyCorrect_TagsUpToDate_SkipsCopy(t *testing.T) {
	dir := t.TempDir()
	mkvFile := "Artist - Song - Song-video.mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	ft := &fakeTagger{tagsMatch: true}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.tagger = ft

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	audioPaths := map[string]string{"Artist - Song": "Artist - Song.flac"}

	p.processVideo(dir, mkvFile, audioData, audioPaths)

	if len(ft.calls) != 0 {
		t.Errorf("expected no CopyTagsToMKV call when tags already match, got %d", len(ft.calls))
	}
}

func TestProcessVideo_Ignore_MatchesAfterPattern_CopiesTags(t *testing.T) {
	dir := t.TempDir()
	// Video name = matchedBase ("Artist - Song - Song") + " OFFICIAL" + "-video"
	// After ignoring " OFFICIAL", reChecked == matchedBase → tags should be copied.
	mkvFile := "Artist - Song - Song OFFICIAL-video.mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", " OFFICIAL"}})
	p.tagger = ft
	p.patternAppender = func(_, _, _ string) error { return nil }

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	audioPaths := map[string]string{"Artist - Song": "Artist - Song.flac"}

	p.processVideo(dir, mkvFile, audioData, audioPaths)

	if len(ft.calls) != 1 {
		t.Fatalf("expected 1 tagger call after ignore match, got %d", len(ft.calls))
	}
	if ft.calls[0].mkvPath != filepath.Join(dir, mkvFile) {
		t.Errorf("mkvPath = %q, want %q", ft.calls[0].mkvPath, filepath.Join(dir, mkvFile))
	}
}

func TestProcessVideo_IgnorePatternInFilename_PassesTitleSuffix(t *testing.T) {
	dir := t.TempDir()
	// Video already matches after stripping " (Alternate Version)".
	mkvFile := "09 - On My Way - On My Way (Alternate Version).mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "09 - On My Way.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData.Global = []string{" (Alternate Version)"}
	p.tagger = ft

	audioData := map[string]string{"09 - On My Way": "09 - On My Way - On My Way"}
	audioPaths := map[string]string{"09 - On My Way": "09 - On My Way.flac"}

	p.processVideo(dir, mkvFile, audioData, audioPaths)

	if len(ft.calls) != 1 {
		t.Fatalf("expected 1 tagger call, got %d", len(ft.calls))
	}
	if ft.calls[0].titleSuffix != "(Alternate Version)" {
		t.Errorf("titleSuffix = %q, want '(Alternate Version)'", ft.calls[0].titleSuffix)
	}
}
