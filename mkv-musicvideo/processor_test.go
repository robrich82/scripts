package main

import (
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
	calls []taggerCall
	err   error
}

type taggerCall struct {
	audioPath   string
	mkvPath     string
	titleSuffix string
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
	p := NewProcessor(dir, &fakePrompter{})
	result := p.processVideo(dir, "Unknown - Song-video.mkv", map[string]string{}, nil)
	if result != "" {
		t.Errorf("got %q, want empty (no audio to match)", result)
	}
}

func TestProcessVideo_AlreadyCorrect_NoAction(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, &fakePrompter{})
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

	p := NewProcessor(dir, &fakePrompter{answers: []string{"r"}})
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
	p := NewProcessor(dir, &fakePrompter{answers: []string{"s"}})
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}

	result := p.processVideo(dir, "Artist - Song-video.mkv", audioData, nil)
	if result != "skipped" {
		t.Errorf("got %q, want 'skipped'", result)
	}
}

func TestProcessVideo_IgnoreChosen_SavesPattern(t *testing.T) {
	dir := t.TempDir()
	// Video has " OFFICIAL" suffix beyond the audio match.
	p := NewProcessor(dir, &fakePrompter{answers: []string{"i", " OFFICIAL"}})

	var saved string
	p.patternAppender = func(_, pattern string) error {
		saved = pattern
		return nil
	}

	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	p.processVideo(dir, "Artist - Song OFFICIAL-video.mkv", audioData, nil)

	if saved != " OFFICIAL" {
		t.Errorf("saved pattern %q, want ' OFFICIAL'", saved)
	}
	if len(p.ignorePatterns) != 1 || p.ignorePatterns[0] != " OFFICIAL" {
		t.Errorf("ignorePatterns=%v", p.ignorePatterns)
	}
}

func TestProcessVideo_DefaultSuffix_WhenNoKnownSuffix(t *testing.T) {
	dir := t.TempDir()
	// Video has no known suffix → effective suffix defaults to "-video".
	// Audio data key must match loosely.
	audioData := map[string]string{"Artist - Song": "Artist - Song - Song"}
	// "Artist - Song - Song" is the target, and our current is
	// cleanVideoIgnored ("Artist - Song") + "-video" = "Artist - Song-video".
	// They differ, so a prompt fires.
	p := NewProcessor(dir, &fakePrompter{answers: []string{"s"}})
	result := p.processVideo(dir, "Artist - Song.mkv", audioData, nil)
	// The file has no known suffix, effective suffix is "-video".
	// currentName = "Artist - Song-video", targetName = "Artist - Song - Song-video" → differ → skipped.
	if result != "skipped" {
		t.Errorf("got %q, want 'skipped'", result)
	}
}

// --- Run integration tests ---

func TestRun_AlreadyCorrect_NoStats(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song - Song-video.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, &fakePrompter{})
	stats, skipped := p.Run()
	if stats.Renamed != 0 || stats.Skipped != 0 || len(skipped) != 0 {
		t.Errorf("expected no actions, got %+v skipped=%v", stats, skipped)
	}
}

func TestRun_SkipsNonVideoFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	p := NewProcessor(dir, &fakePrompter{})
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

	p := NewProcessor(dir, &fakePrompter{answers: []string{"s"}})
	stats, skipped := p.Run()
	if stats.Skipped != 1 {
		t.Errorf("Skipped=%d, want 1", stats.Skipped)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped paths len=%d, want 1", len(skipped))
	}
}

// --- LoadIgnorePatterns tests ---

func TestLoadIgnorePatterns_ReadsLines(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ignore_patterns.txt"), []byte("pattern1\npattern2\n"), 0644)

	p := NewProcessor(dir, &fakePrompter{})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.ignorePatterns) != 2 || p.ignorePatterns[0] != "pattern1" || p.ignorePatterns[1] != "pattern2" {
		t.Errorf("got %v", p.ignorePatterns)
	}
}

func TestLoadIgnorePatterns_MissingFile_OK(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, &fakePrompter{})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

func TestLoadIgnorePatterns_SkipsBlankLines(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ignore_patterns.txt"), []byte("\npattern1\n\n"), 0644)

	p := NewProcessor(dir, &fakePrompter{})
	p.LoadIgnorePatterns()
	if len(p.ignorePatterns) != 1 {
		t.Errorf("expected 1 pattern, got %v", p.ignorePatterns)
	}
}

// --- tagger integration tests ---

func TestProcessVideo_AlreadyCorrect_CopiesTags(t *testing.T) {
	dir := t.TempDir()
	mkvFile := "Artist - Song - Song-video.mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, &fakePrompter{})
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
	p := NewProcessor(dir, &fakePrompter{answers: []string{"r"}})
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
	p := NewProcessor(dir, &fakePrompter{answers: []string{"s"}})
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
	p := &Processor{ignorePatterns: []string{" (Alternate Version)"}}
	got := p.findIgnoredPatterns("09 - On My Way - On My Way (Alternate Version)")
	if got != "(Alternate Version)" {
		t.Errorf("got %q, want '(Alternate Version)'", got)
	}
}

func TestFindIgnoredPatterns_NoMatch(t *testing.T) {
	p := &Processor{ignorePatterns: []string{" (Live)"}}
	got := p.findIgnoredPatterns("01 - Song - Song")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindIgnoredPatterns_MultipleMatches(t *testing.T) {
	p := &Processor{ignorePatterns: []string{" (Live)", " (Acoustic)"}}
	got := p.findIgnoredPatterns("01 - Song (Live) (Acoustic)")
	if got != "(Live) (Acoustic)" {
		t.Errorf("got %q, want '(Live) (Acoustic)'", got)
	}
}

func TestProcessVideo_IgnorePatternInFilename_PassesTitleSuffix(t *testing.T) {
	dir := t.TempDir()
	// Video already matches after stripping " (Alternate Version)".
	mkvFile := "09 - On My Way - On My Way (Alternate Version).mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "09 - On My Way.flac"), []byte{}, 0644)

	ft := &fakeTagger{}
	p := NewProcessor(dir, &fakePrompter{})
	p.ignorePatterns = []string{" (Alternate Version)"}
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
