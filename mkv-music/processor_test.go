package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func (f *fakeTagger) MKVDate(mkvPath string) (time.Time, bool) {
	return time.Time{}, false
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
	clean, suffix, post := cleanVideoBase("Artist - Song-video")
	if clean != "Artist - Song" || suffix != "-video" || post != "" {
		t.Errorf("clean=%q suffix=%q post=%q", clean, suffix, post)
	}
}

func TestCleanVideoBase_StripsLiveSuffix(t *testing.T) {
	clean, suffix, post := cleanVideoBase("Artist - Song-live")
	if clean != "Artist - Song" || suffix != "-live" || post != "" {
		t.Errorf("clean=%q suffix=%q post=%q", clean, suffix, post)
	}
}

func TestCleanVideoBase_NoKnownSuffix(t *testing.T) {
	clean, suffix, post := cleanVideoBase("Artist - Song")
	if clean != "Artist - Song" || suffix != "" || post != "" {
		t.Errorf("clean=%q suffix=%q post=%q", clean, suffix, post)
	}
}

func TestCleanVideoBase_PostSuffix(t *testing.T) {
	clean, suffix, post := cleanVideoBase("Artist - Song-video (Second Version)")
	if clean != "Artist - Song" || suffix != "-video" || post != " (Second Version)" {
		t.Errorf("clean=%q suffix=%q post=%q", clean, suffix, post)
	}
}

func TestCleanVideoBase_SuffixInsideWordIgnored(t *testing.T) {
	for _, tc := range []struct{ base, clean, suffix, post string }{
		{"01 - Long-lived - Long-lived-video", "01 - Long-lived - Long-lived", "-video", ""},
		{"01 - Long-lived - Long-lived", "01 - Long-lived - Long-lived", "", ""},
		{"01 - Re-live - Re-live-video (Second Version)", "01 - Re-live - Re-live", "-video", " (Second Version)"},
		{"Artist - Song-video2", "Artist - Song", "-video", "2"},
	} {
		clean, suffix, post := cleanVideoBase(tc.base)
		if clean != tc.clean || suffix != tc.suffix || post != tc.post {
			t.Errorf("cleanVideoBase(%q) = %q, %q, %q; want %q, %q, %q", tc.base, clean, suffix, post, tc.clean, tc.suffix, tc.post)
		}
	}
}

func TestRun_SuffixKeywordInTitleStaysRenamed(t *testing.T) {
	// "-live" inside "Long-lived" must not turn the video into a live extra.
	dir := musicDir(t, "01 - Long-lived.flac", "01 - Old - Old-video (Second Version).mkv")
	assertTagsStable(t, dir, nil, "r")
	assertFiles(t, dir, "01 - Long-lived - Long-lived-video.mkv")
	if stats, _, _ := NewProcessor(dir, "", &fakePrompter{}).Run(); stats != (Stats{}) {
		t.Errorf("third run stats = %+v, want none", stats)
	}
}

// --- processVideo tests ---

func TestProcessVideo_NoAudioData_Unmatched(t *testing.T) {
	dir := t.TempDir()
	p := NewProcessor(dir, "", &fakePrompter{})
	result := p.processVideo(dir, "Unknown - Song-video.mkv", map[string]string{}, nil)
	if result != "unmatched" {
		t.Errorf("got %q, want 'unmatched'", result)
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

	wantVideoPath := filepath.Join(dir, "Artist - Song OFFICIAL-video.mkv")
	if savedPattern != " OFFICIAL" {
		t.Errorf("saved pattern %q, want ' OFFICIAL'", savedPattern)
	}
	if savedVideo != wantVideoPath {
		t.Errorf("saved videoFile %q, want %q", savedVideo, wantVideoPath)
	}
	patterns := p.ignoreData.FilePatterns[wantVideoPath]
	if len(patterns) != 1 || patterns[0] != " OFFICIAL" {
		t.Errorf("FilePatterns=%v", p.ignoreData.FilePatterns)
	}
}

func TestProcessVideo_PostSuffix_PromptsAndIgnores(t *testing.T) {
	dir := t.TempDir()
	mkvFile := "08 - Rise & Shine - Rise & Shine-video (Second Version).mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)

	var savedPattern string
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", ""}})
	p.patternAppender = func(_, _, pattern string) error {
		savedPattern = pattern
		return nil
	}

	audioData := map[string]string{"08 - Rise & Shine": "08 - Rise & Shine - Rise & Shine"}
	p.processVideo(dir, mkvFile, audioData, nil)

	if savedPattern != "(Second Version)" {
		t.Errorf("saved pattern %q, want '(Second Version)'", savedPattern)
	}
}

func TestProcessVideo_PostSuffix_AlreadyIgnored_NoPrompt(t *testing.T) {
	dir := t.TempDir()
	mkvFile := "08 - Rise & Shine - Rise & Shine-video (Second Version).mkv"
	os.WriteFile(filepath.Join(dir, mkvFile), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData.FilePatterns = map[string][]string{filepath.Join(dir, mkvFile): {" (Second Version)"}}

	audioData := map[string]string{"08 - Rise & Shine": "08 - Rise & Shine - Rise & Shine"}
	result := p.processVideo(dir, mkvFile, audioData, nil)
	if result != "" {
		t.Errorf("got %q, want empty (already correct after ignore)", result)
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
	stats, skipped, _ := p.Run()
	if stats.Renamed != 0 || stats.Skipped != 0 || len(skipped) != 0 {
		t.Errorf("expected no actions, got %+v skipped=%v", stats, skipped)
	}
}

func TestRun_SkipsNonVideoFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{})
	stats, _, _ := p.Run()
	if stats.Renamed != 0 || stats.Skipped != 0 {
		t.Errorf("non-video files should produce no stats, got %+v", stats)
	}
}

func TestRun_CountsUnmatched(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	// "Nemure.mkv" has no Latin/Japanese character overlap with audio files.
	os.WriteFile(filepath.Join(dir, "Nemure.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{})
	stats, _, unmatched := p.Run()
	if stats.Unmatched != 1 {
		t.Errorf("Unmatched=%d, want 1", stats.Unmatched)
	}
	if len(unmatched) != 1 {
		t.Errorf("unmatched paths len=%d, want 1", len(unmatched))
	}
}

func TestRun_CountsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	// Video name doesn't match the target name.
	os.WriteFile(filepath.Join(dir, "Artist - Song-video.mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"s"}})
	stats, skipped, _ := p.Run()
	if stats.Skipped != 1 {
		t.Errorf("Skipped=%d, want 1", stats.Skipped)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped paths len=%d, want 1", len(skipped))
	}
}

func TestRun_SuffixedVideoPromptedBeforeUnsuffixed(t *testing.T) {
	// Both videos target "01 - foo - foo-video". The "-video" file sorts after
	// the " (Special Music Video)" one but must get the first rename prompt.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "01 - foo.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "01 - bob - bob-video.mkv"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "01 - bob - bob (Special Music Video).mkv"), []byte{}, 0644)

	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "s"}})
	p.Run()
	for _, name := range []string{"01 - foo - foo-video.mkv", "01 - bob - bob (Special Music Video).mkv"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("want %q to exist: %v", name, err)
		}
	}
}

func TestRun_TakenNameKeepsExtraText(t *testing.T) {
	// Once the "-video" file has the standard name, the special video is
	// offered the new title with its "(Special Music Video)" kept, plus "-video".
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "01 - foo.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "01 - bob - bob-video.mkv"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "01 - bob - bob (Special Music Video).mkv"), []byte{}, 0644)

	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "r"}})
	p.tagger = tagger
	if stats, _, _ := p.Run(); stats.Renamed != 2 {
		t.Errorf("Renamed=%d, want 2", stats.Renamed)
	}
	special := filepath.Join(dir, "01 - foo - foo (Special Music Video)-video.mkv")
	for _, name := range []string{"01 - foo - foo-video.mkv", filepath.Base(special)} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("want %q to exist: %v", name, err)
		}
	}
	if len(tagger.calls) != 2 || tagger.calls[1].mkvPath != special || tagger.calls[1].titleSuffix != "(Special Music Video)" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}

	// A second run finds both names correct: no prompts.
	if stats, _, _ := NewProcessor(dir, "", &fakePrompter{}).Run(); stats != (Stats{}) {
		t.Errorf("second run stats = %+v, want none", stats)
	}
}

func TestRun_PlainSuffixedVideoPromptedBeforeVersioned(t *testing.T) {
	// "-video (Second Version)" sorts before "-video" but must not take its name.
	dir := t.TempDir()
	for _, n := range []string{"01 - New.flac", "01 - Old - Old-video.mkv", "01 - Old - Old-video (Second Version).mkv"} {
		os.WriteFile(filepath.Join(dir, n), []byte{}, 0644)
	}
	NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "r"}}).Run()
	assertFiles(t, dir, "01 - New - New-video.mkv", "01 - New - New-video (Second Version).mkv")
	if stats, _, _ := NewProcessor(dir, "", &fakePrompter{}).Run(); stats != (Stats{}) {
		t.Errorf("second run stats = %+v, want none", stats)
	}
}

// musicDir creates a temp folder holding empty files with the given names.
func musicDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func musicFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
		t.Fatal(err)
	}
}

func assertFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("want %q to exist: %v", n, err)
		}
	}
}

// assertTagsStable runs dir twice and checks the second run writes each video
// the title suffix the first run gave it: a renamed file's tag must not change
// once more on the next run. It returns the first run's suffixes by filename.
func assertTagsStable(t *testing.T, dir string, global []string, answers ...string) map[string]string {
	t.Helper()
	suffixes := func(answers []string) map[string]string {
		tagger := &fakeTagger{}
		p := NewProcessor(dir, "", &fakePrompter{answers: answers})
		p.ignoreData.Global = global
		p.tagger = tagger
		p.Run()
		got := map[string]string{}
		for _, c := range tagger.calls {
			got[filepath.Base(c.mkvPath)] = c.titleSuffix
		}
		return got
	}
	first := suffixes(answers)
	second := suffixes(nil)
	if len(second) == 0 || len(second) != len(first) {
		t.Fatalf("first run tagged %q, second run %q", first, second)
	}
	for name, want := range second {
		if got, ok := first[name]; !ok || got != want {
			t.Errorf("%s: first run tag %q (tagged %v), second run %q", name, got, ok, want)
		}
	}
	return first
}

func TestRun_RenameTagsLikeNextRun(t *testing.T) {
	t.Run("lone versioned video", func(t *testing.T) {
		dir := musicDir(t, "01 - New.flac", "01 - Old - Old-video (Second Version).mkv")
		assertTagsStable(t, dir, nil, "r")
		assertFiles(t, dir, "01 - New - New-video.mkv")
	})
	t.Run("ignored text", func(t *testing.T) {
		dir := musicDir(t, "01 - New.flac", "01 - Old - Old-video [HD].mkv")
		assertTagsStable(t, dir, []string{" [HD]"}, "r")
		assertFiles(t, dir, "01 - New - New-video.mkv")
	})
	t.Run("ignored text dropped from kept extra", func(t *testing.T) {
		dir := musicDir(t, "01 - New.flac", "01 - New - New-video.mkv", "01 - Old - Old (Live) (Official Video).mkv")
		tags := assertTagsStable(t, dir, []string{" (Official Video)"}, "r")
		if got, ok := tags["01 - New - New (Live)-video.mkv"]; !ok || got != "(Live)" {
			t.Errorf("tags = %q, want the renamed video tagged %q", tags, "(Live)")
		}
	})
	t.Run("taken name keeps extra text", func(t *testing.T) {
		dir := musicDir(t, "01 - foo.flac", "01 - bob - bob-video.mkv", "01 - bob - bob (Special Music Video).mkv")
		tags := assertTagsStable(t, dir, nil, "r", "r")
		if got := tags["01 - foo - foo (Special Music Video)-video.mkv"]; got != "(Special Music Video)" {
			t.Errorf("tags = %q, want the special video tagged %q", tags, "(Special Music Video)")
		}
	})
}

func TestRun_UnsuffixedPlainBeatsVersioned(t *testing.T) {
	// A plain unsuffixed video owns the standard name over "-video (Second Version)".
	dir := musicDir(t, "01 - New.flac", "01 - Old - Old.mkv", "01 - Old - Old-video (Second Version).mkv")
	NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "r"}}).Run()
	assertFiles(t, dir, "01 - New - New-video.mkv", "01 - New - New-video (Second Version).mkv")
}

func TestRun_UnsuffixedHolderCountsAsTaken(t *testing.T) {
	// "01 - foo - foo.mkv" is accepted as the "-video" file, so it holds the name.
	dir := musicDir(t, "01 - foo.flac", "01 - foo - foo.mkv", "01 - bob - bob (Special Music Video).mkv")
	NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	assertFiles(t, dir, "01 - foo - foo.mkv", "01 - foo - foo (Special Music Video)-video.mkv")
}

func TestRun_OnlyIgnoredTextSetsApart(t *testing.T) {
	// When ignored text is all that differs, keep it rather than get stuck.
	dir := musicDir(t, "01 - New.flac", "01 - New - New-video.mkv", "01 - Old - Old-video [HD].mkv")
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}})
	p.ignoreData.Global = []string{" [HD]"}
	p.Run()
	assertFiles(t, dir, "01 - New - New-video [HD].mkv")
	p2 := NewProcessor(dir, "", &fakePrompter{})
	p2.ignoreData = p.ignoreData
	if stats, _, _ := p2.Run(); stats != (Stats{}) {
		t.Errorf("second run stats = %+v, want none", stats)
	}
}

func TestRun_PlainBeatsIgnoredOnlyText(t *testing.T) {
	// The truly plain file claims the standard name; the "[HD]" one keeps "[HD]".
	for _, plain := range []string{"01 - Old - Old-video.mkv", "01 - Old - Old.mkv"} {
		dir := musicDir(t, "01 - New.flac", plain, "01 - Old - Old-video [HD].mkv")
		p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "r"}})
		p.ignoreData.Global = []string{" [HD]"}
		p.Run()
		assertFiles(t, dir, "01 - New - New-video.mkv", "01 - New - New-video [HD].mkv")
	}
}

func TestRun_RankAppliesIgnorePatterns(t *testing.T) {
	// "(Official Video)" is ignored, so that file is the plain one and goes first.
	dir := musicDir(t, "01 - New.flac", "01 - Old - Old (Live).mkv", "01 - Old - Old (Official Video).mkv")
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r", "r"}})
	p.ignoreData.Global = []string{" (Official Video)"}
	p.Run()
	assertFiles(t, dir, "01 - New - New-video.mkv", "01 - New - New (Live)-video.mkv")
}

func TestRun_TakenNameOtherExtension(t *testing.T) {
	// An .mp4 holding the standard name counts as taken for an .mkv.
	dir := t.TempDir()
	for _, n := range []string{"01 - foo.flac", "01 - foo - foo-video.mp4", "01 - bob - bob (Special Music Video).mkv"} {
		os.WriteFile(filepath.Join(dir, n), []byte{}, 0644)
	}
	NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if _, err := os.Stat(filepath.Join(dir, "01 - foo - foo (Special Music Video)-video.mkv")); err != nil {
		t.Error(err)
	}
}

func TestRun_TakenNameExtraAlreadyInTitle(t *testing.T) {
	// The new title already ends in "(Live)": don't offer "… (Live) (Live)".
	dir := t.TempDir()
	for _, n := range []string{"01 - foo (Live).flac", "01 - foo (Live) - foo (Live)-video.mkv", "01 - foo - foo (live).mkv"} {
		os.WriteFile(filepath.Join(dir, n), []byte{}, 0644)
	}
	if stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run(); stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Skipped=1 (standard name taken)", stats)
	}
}

func TestRun_TakenNameIgnoredTextSettles(t *testing.T) {
	// Ignoring " [HD]" lets the alternative name match, and it stays matched.
	dir := t.TempDir()
	for _, n := range []string{"01 - New.flac", "01 - New - New-video.mkv", "01 - New [HD] - New [HD] (Live)-video.mkv"} {
		os.WriteFile(filepath.Join(dir, n), []byte{}, 0644)
	}
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", " [HD]"}})
	p.tagger = tagger
	p.patternAppender = func(_, _, _ string) error { return nil }
	p.Run()
	if last := tagger.calls[len(tagger.calls)-1]; filepath.Base(last.mkvPath) != "01 - New [HD] - New [HD] (Live)-video.mkv" || last.titleSuffix != "[HD] (Live)" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
	p2 := NewProcessor(dir, "", &fakePrompter{})
	p2.ignoreData = p.ignoreData
	if stats, _, _ := p2.Run(); stats != (Stats{}) {
		t.Errorf("second run stats = %+v, want none", stats)
	}
}

func TestRun_RenameTagsFromRenameMatchWhenNewNameRematches(t *testing.T) {
	// Ignoring " Love" strips the new name "01 - Love - Love-video" down to
	// "01 - -", which then matches "01 - Live": the new name isn't judged
	// correct, so tag from the audio the rename used.
	dir := musicDir(t, "01 - Love.flac", "01 - Live.flac", "01 - Song - Song [HD].mkv")
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}})
	p.ignoreData.Global = []string{" Love"}
	p.tagger = tagger
	p.Run()
	assertFiles(t, dir, "01 - Love - Love-video.mkv")
	if len(tagger.calls) != 1 || filepath.Base(tagger.calls[0].mkvPath) != "01 - Love - Love-video.mkv" ||
		filepath.Base(tagger.calls[0].audioPath) != "01 - Love.flac" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
}

func TestRun_RenamedVideoKeepsItsTrack(t *testing.T) {
	// "01 - Love - Love" is closer to "01 - Love Love" by filename, but it is
	// exactly the display name of "01 - Love": it must not move on run 2.
	dir := musicDir(t, "01 - Love.flac", "01 - Love Love.flac", "01 - Old - Old (Live).mkv")
	NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	assertFiles(t, dir, "01 - Love - Love-video.mkv")
	if stats, _, _ := NewProcessor(dir, "", &fakePrompter{}).Run(); stats != (Stats{}) {
		t.Errorf("second run stats = %+v, want none", stats)
	}
}

func TestRun_TitleSuffixNotDoubled(t *testing.T) {
	// Ignored text inside the kept extra must reach the title tag once.
	dir := musicDir(t, "01 - New.flac", "01 - New - New-video.mkv", "01 - New - New (Live) (Official Video)-video.mkv")
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData.Global = []string{" (Official Video)"}
	p.tagger = tagger
	p.Run()
	last := tagger.calls[len(tagger.calls)-1]
	if filepath.Base(last.mkvPath) != "01 - New - New (Live) (Official Video)-video.mkv" || last.titleSuffix != "(Official Video) (Live)" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
}

func TestCopyTags_ShortPostSuffixInsidePatternKept(t *testing.T) {
	// "-video2" next to an ignored "(Take 2)": the "2" must survive.
	dir := t.TempDir()
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData.Global = []string{"(Take 2)"}
	p.tagger = tagger
	mkv := filepath.Join(dir, "v.mkv")
	p.copyTagsIfConfigured(mkv, "k", "01 - New (Take 2) - New (Take 2)", "2", dir, mkv, map[string]string{"k": "a.flac"})
	if len(tagger.calls) != 1 || tagger.calls[0].titleSuffix != "(Take 2) 2" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
}

func TestCopyTags_SamePatternTwiceTaggedOnce(t *testing.T) {
	// A global "[HD]" and a file " [HD]" both match: the tag says it once.
	dir := t.TempDir()
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	mkv := filepath.Join(dir, "v.mkv")
	p.ignoreData = IgnoreData{Global: []string{"[HD]"}, FilePatterns: map[string][]string{mkv: {" [HD]"}}}
	p.tagger = tagger
	p.copyTagsIfConfigured(mkv, "k", "01 - Song [HD] - Song [HD]", "", dir, mkv, map[string]string{"k": "a.flac"})
	if len(tagger.calls) != 1 || tagger.calls[0].titleSuffix != "[HD]" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
}

func TestRun_IgnoredTextInAudioTitleNotRepeatedInTag(t *testing.T) {
	// The audio title "Song (Live)" already says "(Live)": the tag title
	// suffix must not add it again.
	dir := musicDir(t, "01 - Song (Live).flac", "01 - Old - Old-video.mkv")
	tags := assertTagsStable(t, dir, []string{" (Live)"}, "r")
	if got, ok := tags["01 - Song (Live) - Song (Live)-video.mkv"]; !ok || got != "" {
		t.Errorf("tags = %q, want the renamed video tagged with no suffix", tags)
	}
}

func TestCopyTags_IgnoredPatternInPostSuffixNotRepeated(t *testing.T) {
	// "(Official Video)" is ignored in the name and already in postSuffix:
	// the title suffix carries it once.
	dir := t.TempDir()
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData.Global = []string{" (Official Video)"}
	p.tagger = tagger
	mkv := filepath.Join(dir, "v.mkv")
	p.copyTagsIfConfigured(mkv, "k", "01 - New - New (Official Video)", "(Official Video) (Live)", dir, mkv, map[string]string{"k": "a.flac"})
	if len(tagger.calls) != 1 || tagger.calls[0].titleSuffix != "(Official Video) (Live)" {
		t.Errorf("tag calls = %+v", tagger.calls)
	}
}

func TestRun_IgnoreOnUnsuffixedTakenNameStillNeedsRename(t *testing.T) {
	// The unsuffixed file must gain "-video": ignoring an unrelated pattern
	// doesn't make its current name match, so it must not be tagged as matched.
	dir := musicDir(t, "01 - foo.flac", "01 - foo - foo.mkv", "01 - foo - foo (Live).mkv")
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", "zzz"}})
	p.patternAppender = func(string, string, string) error { return nil }
	p.tagger = tagger
	p.Run()
	for _, c := range tagger.calls {
		if filepath.Base(c.mkvPath) == "01 - foo - foo (Live).mkv" {
			t.Errorf("tagged %s as matched after ignoring an unrelated pattern", c.mkvPath)
		}
	}
	assertFiles(t, dir, "01 - foo - foo (Live).mkv")
}

func TestRun_SkippedPlainVideoKeepsTheStandardName(t *testing.T) {
	for _, versioned := range []string{"01 - Old - Old (Special Music Video).mkv", "01 - Old - Old-video (Second Version).mkv"} {
		t.Run(versioned, func(t *testing.T) {
			dir := musicDir(t, "01 - New.flac", "01 - Old - Old.mkv", versioned)
			// Skip the plain video; the versioned one must keep its extra text.
			NewProcessor(dir, "", &fakePrompter{answers: []string{"s", "r"}}).Run()
			kept := map[string]string{
				"01 - Old - Old (Special Music Video).mkv":  "01 - New - New (Special Music Video)-video.mkv",
				"01 - Old - Old-video (Second Version).mkv": "01 - New - New-video (Second Version).mkv",
			}[versioned]
			assertFiles(t, dir, "01 - Old - Old.mkv", kept)
			// The plain video can still take the standard name later.
			NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
			assertFiles(t, dir, "01 - New - New-video.mkv", kept)
			if stats, _, _ := NewProcessor(dir, "", &fakePrompter{}).Run(); stats != (Stats{}) {
				t.Errorf("third run stats = %+v, want none", stats)
			}
		})
	}
}

func TestRun_RenameKeepsFilePatternText(t *testing.T) {
	const newer, song = "01 - Newer.flac", "01 - Song (Alt Take).flac"
	for _, tc := range []struct {
		name, audio, holder, video string
		patterns                   []string
		want, tag                  string
		global                     []string
	}{
		{"in title", newer, "", "01 - New - New (Alt Take)-video.mkv", []string{" (Alt Take)"}, "01 - Newer - Newer (Alt Take)-video.mkv", "(Alt Take)", nil},
		{"after suffix", newer, "", "01 - New - New-video (Alt Take).mkv", []string{" (Alt Take)"}, "01 - Newer - Newer-video (Alt Take).mkv", "(Alt Take)", nil},
		{"taken name", newer, "01 - Newer - Newer-video.mkv", "01 - New - New (Live) (Alt Take)-video.mkv", []string{" (Alt Take)"}, "01 - Newer - Newer (Live) (Alt Take)-video.mkv", "(Alt Take) (Live)", nil},
		{"new title has it", song, "", "01 - Old - Old (Alt Take)-video.mkv", []string{" (Alt Take)"}, "01 - Song (Alt Take) - Song (Alt Take)-video.mkv", "", nil},
		{"taken, new title has it", song, "01 - Song (Alt Take) - Song (Alt Take)-video.mkv", "01 - Old - Old (Live) (Alt Take)-video.mkv", []string{" (Alt Take)"}, "01 - Song (Alt Take) - Song (Alt Take) (Live)-video.mkv", "(Live)", nil},
		{"pattern wouldn't match re-placed", newer, "", "01 - New - New (Alt) (Live)-video.mkv", []string{"(Alt) "}, "01 - Newer - Newer-video.mkv", "", nil},
		{"overlapping patterns", newer, "", "01 - New - New (Alt) (Take 2)-video.mkv", []string{" (Alt)", " (Alt) (Take 2)"}, "01 - Newer - Newer (Alt) (Take 2)-video.mkv", "(Alt) (Take 2)", nil},
		{"taken, global inside kept", newer, "01 - Newer - Newer-video.mkv", "01 - Old - Old (Live) [HD]-video.mkv", []string{" (Live) [HD]"}, "01 - Newer - Newer (Live) [HD]-video.mkv", "(Live) [HD]", []string{" [HD]"}},
		{"word inside new title", "01 - Alive.flac", "", "01 - Old - Old Live-video.mkv", []string{" Live"}, "01 - Alive - Alive Live-video.mkv", "Live", nil},
		{"pattern would cut new title", song, "", "01 - Old - Old Fan-video.mkv", []string{" Fan", "Alt"}, "01 - Song (Alt Take) - Song (Alt Take) Fan-video.mkv", "Fan", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The audio was retitled after "i" saved patterns for this file.
			dir := musicDir(t, tc.audio, tc.video)
			if tc.holder != "" {
				musicFile(t, dir, tc.holder)
			}
			ignoreFile := filepath.Join(t.TempDir(), "ignore.json")
			oldPath, newPath := filepath.Join(dir, tc.video), filepath.Join(dir, tc.want)
			writeIgnoreJSON(t, ignoreFile, IgnoreData{Global: tc.global, FilePatterns: map[string][]string{oldPath: tc.patterns}})

			run := func(answers ...string) (Stats, *fakeTagger) {
				tagger := &fakeTagger{}
				p := NewProcessor(dir, ignoreFile, &fakePrompter{answers: answers})
				if err := p.LoadIgnorePatterns(); err != nil {
					t.Fatal(err)
				}
				p.tagger = tagger
				stats, _, _ := p.Run()
				return stats, tagger
			}
			tagOf := func(tg *fakeTagger) string {
				for _, c := range tg.calls {
					if c.mkvPath == newPath {
						return c.titleSuffix
					}
				}
				return "<untagged>"
			}

			_, tagger := run("r")
			assertFiles(t, dir, tc.want)
			if got := tagOf(tagger); got != tc.tag {
				t.Errorf("rename run tag = %q, want %q", got, tc.tag)
			}
			b, _ := os.ReadFile(ignoreFile)
			var saved IgnoreData
			json.Unmarshal(b, &saved)
			if _, ok := saved.FilePatterns[oldPath]; ok || !slices.Equal(saved.FilePatterns[newPath], tc.patterns) {
				t.Errorf("saved file patterns = %q, want only %q under the new path", saved.FilePatterns, tc.patterns)
			}

			stats, tagger := run()
			if stats != (Stats{}) {
				t.Errorf("second run stats = %+v, want none", stats)
			}
			if got := tagOf(tagger); got != tc.tag {
				t.Errorf("second run tag = %q, want %q", got, tc.tag)
			}
		})
	}
}

// captureStdout returns what fn prints to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	w.Close()
	return string(<-done)
}

func TestRun_RenameReportsFailedPatternMove(t *testing.T) {
	video, want := "01 - New - New (Alt Take)-video.mkv", "01 - Newer - Newer (Alt Take)-video.mkv"
	dir := musicDir(t, "01 - Newer.flac", video)
	ignoreFile := filepath.Join(t.TempDir(), "ignore.json")
	oldPath, newPath := filepath.Join(dir, video), filepath.Join(dir, want)
	writeIgnoreJSON(t, ignoreFile, IgnoreData{FilePatterns: map[string][]string{oldPath: {" (Alt Take)"}}})
	before, _ := os.ReadFile(ignoreFile)

	tagger := &fakeTagger{}
	p := NewProcessor(dir, ignoreFile, &fakePrompter{answers: []string{"r"}})
	if err := p.LoadIgnorePatterns(); err != nil {
		t.Fatal(err)
	}
	p.tagger = tagger
	p.patternMover = func(string, string, string) error { return os.ErrPermission }
	out := captureStdout(t, func() { p.Run() })

	assertFiles(t, dir, want)
	if msg := fmt.Sprintf("[ERROR] Could not move ignore patterns in %s from %q to %q", ignoreFile, oldPath, newPath); !strings.Contains(out, msg) {
		t.Errorf("output missing %q:\n%s", msg, out)
	}
	// The in-memory move still lets this run tag from the new name.
	if len(tagger.calls) != 1 || tagger.calls[0].mkvPath != newPath || tagger.calls[0].titleSuffix != "(Alt Take)" {
		t.Errorf("tag calls = %+v, want one for %s with (Alt Take)", tagger.calls, newPath)
	}
	if after, _ := os.ReadFile(ignoreFile); string(after) != string(before) {
		t.Errorf("ignore file changed despite the failed move:\n%s", after)
	}
}

func TestDefaultPatternMover(t *testing.T) {
	dir := t.TempDir()
	ignoreFile := filepath.Join(dir, "ignore.json")
	oldPath, newPath := filepath.Join(dir, "Old (A).mkv"), filepath.Join(dir, "New (A).mkv")
	writeIgnoreJSON(t, ignoreFile, IgnoreData{
		Global: []string{"[HD]"},
		FilePatterns: map[string][]string{
			oldPath:       {" (A)", " (B)"},
			"Old (A).mkv": {" (B)", " (C)"},
			newPath:       {" (A)"},
			"other.mkv":   {" (D)"},
		},
	})

	if err := defaultPatternMover(ignoreFile, oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ignoreFile)
	var got IgnoreData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := IgnoreData{
		Global: []string{"[HD]"},
		FilePatterns: map[string][]string{
			"Old (A).mkv": {" (B)", " (C)"}, // legacy bare-name entry is copied, not moved
			newPath:       {" (A)", " (B)", " (C)"},
			"other.mkv":   {" (D)"},
		},
	}
	if !slices.Equal(got.Global, want.Global) || len(got.FilePatterns) != len(want.FilePatterns) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for k, v := range want.FilePatterns {
		if !slices.Equal(got.FilePatterns[k], v) {
			t.Errorf("FilePatterns[%q] = %q, want %q", k, got.FilePatterns[k], v)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dir holds %d entries, want only the ignore file", len(entries))
	}

	// A rename onto the same path moves nothing, on disk or in memory.
	if err := defaultPatternMover(ignoreFile, newPath, newPath); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(ignoreFile); string(again) != string(b) {
		t.Errorf("same-path move changed the file:\n%s", again)
	}
	p := NewProcessor(dir, "", &fakePrompter{})
	p.ignoreData = got
	p.moveFilePatterns(newPath, newPath)
	if !slices.Equal(p.ignoreData.FilePatterns[newPath], want.FilePatterns[newPath]) {
		t.Errorf("same-path move in memory left %q", p.ignoreData.FilePatterns[newPath])
	}
}

func TestProcessVideo_IgnoreWithKeptTextMatchesNow(t *testing.T) {
	// " (Alt)" is already ignored for this file; ignoring " (Foo)" too makes it
	// match, and the kept " (Alt)" must not make the re-check say otherwise.
	video := "01 - New - New (Alt) (Foo)-video.mkv"
	dir := musicDir(t, "01 - New.flac", video)
	tagger := &fakeTagger{}
	p := NewProcessor(dir, "", &fakePrompter{answers: []string{"i", " (Foo)"}})
	p.ignoreData.FilePatterns = map[string][]string{filepath.Join(dir, video): {" (Alt)"}}
	p.patternAppender = func(string, string, string) error { return nil }
	p.tagger = tagger
	out := captureStdout(t, func() { p.Run() })

	if !strings.Contains(out, "File now matches after ignoring pattern!") {
		t.Errorf("output:\n%s", out)
	}
	if len(tagger.calls) != 1 || tagger.calls[0].titleSuffix != "(Alt) (Foo)" {
		t.Errorf("tag calls = %+v, want one with (Alt) (Foo)", tagger.calls)
	}
}

func TestWriteIgnoreFile_FailureKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ignore.json")
	if err := os.Mkdir(target, 0755); err != nil { // a directory can't be replaced by a file
		t.Fatal(err)
	}
	if err := writeIgnoreFile(target, IgnoreData{Global: []string{"x"}}); err == nil {
		t.Fatal("want an error replacing a directory")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Errorf("target disturbed: info=%v err=%v", info, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %d entries", len(entries))
	}
}

func TestIsTrackNumber(t *testing.T) {
	for in, want := range map[string]bool{"01": true, "1-01": true, "1.01": true, "": false, "-": false, ".": false, "01a": false, "Artist": false} {
		if got := isTrackNumber(in); got != want {
			t.Errorf("isTrackNumber(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRun_FreeNameStillGetsStandardName(t *testing.T) {
	// With no "-video" file, the special video is offered the standard name.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "01 - foo.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "01 - bob - bob (Special Music Video).mkv"), []byte{}, 0644)

	NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if _, err := os.Stat(filepath.Join(dir, "01 - foo - foo-video.mkv")); err != nil {
		t.Error(err)
	}
}

func TestExtraText(t *testing.T) {
	for in, want := range map[string]string{
		"01 - bob - bob (Special Music Video)":     "(Special Music Video)",
		"01 - A - B - A - B (Live)":                "(Live)",
		"01 - A - A - A - A (Live)":                "(Live)",
		"01 - Love - Love Me - Love - Love Me (X)": "(X)",
		"01 - Bob - bob (Live)":                    "(Live)",
		"05 - Run - Runaway":                       "",
		"01 - bob - bob":                           "",
		"01 - bob - other":                         "",
		"NoSeparator":                              "",
	} {
		if got := extraText(in); got != want {
			t.Errorf("extraText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRun_CaseOnlyRename(t *testing.T) {
	// On a case-insensitive filesystem the target "exists" (it is this file);
	// that must not block the rename.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "PROBE")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	os.Remove(filepath.Join(dir, "probe"))
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "artist - song - song-video.mkv"), []byte{}, 0644)

	stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if stats.Renamed != 1 {
		t.Errorf("stats = %+v, want Renamed=1", stats)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Contains(names, "Artist - Song - Song-video.mkv") {
		t.Errorf("files = %q, want the exact-case name", names)
	}
}

func TestRun_DifferentFileWithSameNameStillProtected(t *testing.T) {
	// Two videos competing for one name: the second must not clobber the first.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Song - Song-video.mkv"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(dir, "Artist - Sonng - Sonng-video.mkv"), []byte{}, 0644)

	stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Skipped=1", stats)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Artist - Song - Song-video.mkv")); string(b) != "keep" {
		t.Error("existing file was overwritten")
	}
}

func TestRun_SymlinkToTargetNotRenamedOverIt(t *testing.T) {
	// A video that is a symlink to the file holding the target name resolves to
	// the same file, but renaming the link over it would destroy the data.
	dir := t.TempDir()
	target := filepath.Join(dir, "Artist - Song - Song-video.mkv")
	os.WriteFile(filepath.Join(dir, "Artist - Song.flac"), []byte{}, 0644)
	os.WriteFile(target, []byte("keep"), 0644)
	if err := os.Symlink(target, filepath.Join(dir, "Artist - Sonng - Sonng-video.mkv")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Skipped=1", stats)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target replaced: info=%v err=%v", info, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Error("target contents lost")
	}
}

// kelvinVideo is a video name that strings.EqualFold treats as equal to
// "Artist - Kick - Kick-video.mkv" (U+212A KELVIN SIGN folds to "k"), but NTFS
// and case-sensitive filesystems keep as a separate name.
const kelvinVideo = "Artist - Kick - Kick-video.mkv"

func TestRun_FoldEqualDifferentFileProtected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Artist - Kick - Kick-video.mkv")
	os.WriteFile(filepath.Join(dir, "Artist - Kick.flac"), []byte{}, 0644)
	os.WriteFile(target, []byte("keep"), 0644)
	os.WriteFile(filepath.Join(dir, kelvinVideo), []byte("video"), 0644)
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Skip("filesystem treats the two names as one")
	}

	stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Skipped=1", stats)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Error("existing file was overwritten")
	}
}

func TestRun_FoldEqualSymlinkNotRenamedOverTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Artist - Kick - Kick-video.mkv")
	os.WriteFile(filepath.Join(dir, "Artist - Kick.flac"), []byte{}, 0644)
	os.WriteFile(target, []byte("keep"), 0644)
	if err := os.Symlink(target, filepath.Join(dir, kelvinVideo)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	stats, _, _ := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}}).Run()
	if stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Skipped=1", stats)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target replaced: info=%v err=%v", info, err)
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
	got := strings.Join(p.ignoredPatternList("09 - On My Way - On My Way (Alternate Version)", ""), " ")
	if got != "(Alternate Version)" {
		t.Errorf("got %q, want '(Alternate Version)'", got)
	}
}

func TestFindIgnoredPatterns_NoMatch(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{Global: []string{" (Live)"}}}
	got := strings.Join(p.ignoredPatternList("01 - Song - Song", ""), " ")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindIgnoredPatterns_MultipleMatches(t *testing.T) {
	p := &Processor{ignoreData: IgnoreData{Global: []string{" (Live)", " (Acoustic)"}}}
	got := strings.Join(p.ignoredPatternList("01 - Song (Live) (Acoustic)", ""), " ")
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
	got := strings.Join(p.ignoredPatternList("Song (Stone Version) 1080p", "Song (Stone Version).mkv"), " ")
	if got != "1080p (Stone Version)" {
		t.Errorf("got %q, want '1080p (Stone Version)'", got)
	}
	// File-specific pattern should NOT appear for a different file.
	got2 := strings.Join(p.ignoredPatternList("Song (Stone Version) 1080p", "Other Song.mkv"), " ")
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

// --- findMatch tests ---

func TestFindMatch_SameTrackBeatsSameTitleOnOtherTrack(t *testing.T) {
	// The audio for track 01 was retitled; the video still has the old title,
	// which another track happens to carry. Track 01 must still win.
	audio := buildAudioData([]string{"01 - Kill.flac", "02 - Bohemian Rhapsody.flac"})
	m, ok := findMatch("01 - Bohemian Rhapsody - Bohemian Rhapsody", audio)
	if !ok || m != "01 - Kill" {
		t.Errorf("got %q, %v; want %q", m, ok, "01 - Kill")
	}
}

func TestFindMatch_TrackNumberMustMatchExactly(t *testing.T) {
	// Track "1" is "1 - Other", not "10 - Song".
	audio := buildAudioData([]string{"1 - Other.flac", "10 - Song.flac"})
	m, ok := findMatch("1 - Song - Song", audio)
	if !ok || m != "1 - Other" {
		t.Errorf("got %q, %v; want %q", m, ok, "1 - Other")
	}
}

func TestFindMatch_NoSameTrackFallsBackToTitle(t *testing.T) {
	audio := buildAudioData([]string{"02 - Song.flac", "03 - Other.flac"})
	m, ok := findMatch("05 - Song - Song", audio)
	if !ok || m != "02 - Song" {
		t.Errorf("got %q, %v; want %q", m, ok, "02 - Song")
	}
}

func TestFindMatch_NumberSharedByEveryAudioKeepsCutoff(t *testing.T) {
	// "311" on every audio file is an artist, not a track: no forced match.
	audio := buildAudioData([]string{"311 - Amber.flac", "311 - Down.flac"})
	long := "Qqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	if m, ok := findMatch("311 - "+long+" - "+long, audio); ok {
		t.Errorf("got %q, want no match", m)
	}
	if m, ok := findMatch("311 - Down - Down", audio); !ok || m != "311 - Down" {
		t.Errorf("got %q, %v; want %q", m, ok, "311 - Down")
	}
}

func TestFindMatch_NoAudioForSongStaysUnmatched(t *testing.T) {
	// Display names only count for "NN - T - T" names; scoring this against
	// them would lift "03 - Reprise" over the cutoff.
	audio := buildAudioData([]string{"01 - Lucky.flac", "03 - Reprise.flac", "04 - Sunshine.flac", "05 - Midnight.flac", "07 - Golden Hour.flac"})
	if m, ok := findMatch("Purple Rain", audio); ok {
		t.Errorf("got %q, want no match", m)
	}
}

func TestFindMatch_ArtistPrefixKeepsCutoff(t *testing.T) {
	// A non-numeric prefix is an artist, not a track: no forced match.
	audio := buildAudioData([]string{"Artist - Zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz.flac"})
	if m, ok := findMatch("Artist - Qqqqqqqqqqqqqqqqqqqqqqqqqqqqqq - Qqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", audio); ok {
		t.Errorf("got %q, want no match", m)
	}
}

func TestDefaultPatternAppender_AddsToExistingFile(t *testing.T) {
	ignoreFile := filepath.Join(t.TempDir(), "ignore.json")
	writeIgnoreJSON(t, ignoreFile, IgnoreData{FilePatterns: map[string][]string{"a.mkv": {"x"}}})

	if err := defaultPatternAppender(ignoreFile, "b.mkv", "y"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ignoreFile)
	if err != nil {
		t.Fatal(err)
	}
	var got IgnoreData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.FilePatterns["a.mkv"], []string{"x"}) || !slices.Equal(got.FilePatterns["b.mkv"], []string{"y"}) {
		t.Errorf("FilePatterns = %v, want a.mkv:[x] b.mkv:[y]", got.FilePatterns)
	}
}

func TestDefaultPatternAppender_CreatesMissingFile(t *testing.T) {
	ignoreFile := filepath.Join(t.TempDir(), "ignore.json")

	if err := defaultPatternAppender(ignoreFile, "a.mkv", "x"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ignoreFile)
	if err != nil {
		t.Fatal(err)
	}
	var got IgnoreData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.FilePatterns["a.mkv"], []string{"x"}) {
		t.Errorf("FilePatterns = %v, want a.mkv:[x]", got.FilePatterns)
	}
}

func TestDefaultPatternAppender_CorruptFileLeftAlone(t *testing.T) {
	ignoreFile := filepath.Join(t.TempDir(), "ignore.json")
	corrupt := []byte(`{"file_patterns": {"a.mkv": ["x"]`)
	if err := os.WriteFile(ignoreFile, corrupt, 0644); err != nil {
		t.Fatal(err)
	}

	if err := defaultPatternAppender(ignoreFile, "b.mkv", "y"); err == nil {
		t.Error("err = nil, want a parse error")
	}
	if got, _ := os.ReadFile(ignoreFile); string(got) != string(corrupt) {
		t.Errorf("ignore file rewritten to %s", got)
	}
}

func TestRun_UnreadableRootWarns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	p := NewProcessor(root, filepath.Join(t.TempDir(), "ignore.json"), &fakePrompter{})

	out := captureStdout(t, func() { p.Run() })
	if want := "[WARN] Skipping unreadable " + root; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestSetAudioTimestamps_UnreadableRootWarns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")

	var stats AudioTimestampStats
	out := captureStdout(t, func() { stats = SetAudioTimestamps(root, time.Time{}, nil, "") })
	if stats != (AudioTimestampStats{}) {
		t.Errorf("stats = %+v, want zero", stats)
	}
	if want := "[AUDIO] Skipping unreadable " + root; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}
