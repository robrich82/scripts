package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeIdentifier returns a pre-set MKVInfo (or error) for any file.
type fakeIdentifier struct {
	info *MKVInfo
	err  error
}

func (f *fakeIdentifier) Identify(_ string) (*MKVInfo, error) {
	return f.info, f.err
}

func (f *fakeIdentifier) ReadTagDate(_ string) (time.Time, bool) {
	return time.Time{}, false
}

// fakeEditor records Apply calls and returns a controlled error.
type fakeEditor struct {
	calls []string
	err   error
}

func (f *fakeEditor) Apply(filePath string, _ []string) error {
	f.calls = append(f.calls, filePath)
	return f.err
}

// fakePrompter returns a fixed answer.
type fakePrompter struct {
	answer string
}

func (f *fakePrompter) Ask(_ string) (string, error) {
	return f.answer, nil
}

func makeMKVFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRunAll_NoEditsNeeded_OK(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "movie.mkv")

	info := &MKVInfo{Tracks: []Track{
		{ID: 0, Type: "video", Properties: TrackProperties{Language: "eng", LanguageIETF: "en-US"}},
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US",
			CodecID: "A_FLAC", AudioChannels: 6, DefaultTrack: true,
		}},
	}}

	editor := &fakeEditor{}
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})

	if stats.Scanned != 1 {
		t.Errorf("Scanned=%d, want 1", stats.Scanned)
	}
	if len(editor.calls) != 0 {
		t.Error("Apply should not be called when no edits needed")
	}
}

func TestRunAll_IdentifyError_CountsFailed(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "bad.mkv")

	ident := &fakeIdentifier{err: fmt.Errorf("mkvmerge failed")}
	stats := RunAll(dir, t.TempDir(), nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})

	if stats.Failed != 1 {
		t.Errorf("Failed=%d, want 1", stats.Failed)
	}
	if len(stats.FailedPaths) != 1 {
		t.Errorf("FailedPaths len=%d, want 1", len(stats.FailedPaths))
	}
}

func TestRunAll_EditsApplied_CountsFixed(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "fix.mkv")

	// Audio track with wrong language will generate a lang fix edit.
	info := &MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "en", LanguageIETF: "en",
			CodecID: "A_AC3", AudioChannels: 2, DefaultTrack: true,
		}},
	}}

	editor := &fakeEditor{}
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})

	if stats.Fixed != 1 {
		t.Errorf("Fixed=%d, want 1", stats.Fixed)
	}
	if len(editor.calls) != 1 {
		t.Errorf("Apply call count=%d, want 1", len(editor.calls))
	}
}

func TestRunAll_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})
	if stats.Scanned != 0 {
		t.Errorf("Scanned=%d, want 0 for non-MKV file", stats.Scanned)
	}
}

func TestRunAll_MKAIncluded(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "audio.mka")

	info := &MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US",
			CodecID: "A_FLAC", DefaultTrack: true,
		}},
	}}

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})
	if stats.Scanned != 1 {
		t.Errorf("Scanned=%d, want 1 for .mka file", stats.Scanned)
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

func TestRunAll_UnreadableRootWarns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")

	var stats Stats
	out := captureStdout(t, func() {
		stats = RunAll(root, t.TempDir(), nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})
	})
	if stats.Scanned != 0 {
		t.Errorf("Scanned=%d, want 0", stats.Scanned)
	}
	if want := "[WARN]   skipping unreadable " + root; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestRunAll_SavesFailedCache(t *testing.T) {
	dir, stateDir := t.TempDir(), filepath.Join(t.TempDir(), "state")
	makeMKVFile(t, dir, "bad.mkv")

	ident := &fakeIdentifier{err: fmt.Errorf("mkvmerge failed")}
	RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})

	got := loadFailedCache(stateDir)
	if want := filepath.Join(dir, "bad.mkv"); len(got) != 1 || got[0] != want {
		t.Errorf("failed cache = %v, want [%s]", got, want)
	}
}

func TestRunAll_UnreadableRootKeepsEarlierFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "offline")
	stateDir := t.TempDir()
	old := []string{filepath.Join(root, "Show", "a.mkv")}
	if err := saveFailedCache(stateDir, old); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		RunAll(root, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})
	})

	if got := loadFailedCache(stateDir); !slices.Equal(got, old) {
		t.Errorf("failed cache = %v, want %v", got, old)
	}
}

func TestRunAll_FullScanDropsFixedFailuresKeepsOthers(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "fixed.mkv")
	outside := filepath.Join(t.TempDir(), "other.mkv")
	if err := saveFailedCache(stateDir, []string{filepath.Join(dir, "fixed.mkv"), outside}); err != nil {
		t.Fatal(err)
	}

	RunAll(dir, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})

	if got := loadFailedCache(stateDir); !slices.Equal(got, []string{outside}) {
		t.Errorf("failed cache = %v, want [%s]", got, outside)
	}
}

func TestRunAll_FullScanKeepsFailuresSkippedByCutoff(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "old.mkv")
	cutoff := time.Now().Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.mkv"), old, old); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "deleted.mkv")
	if err := saveFailedCache(stateDir, []string{filepath.Join(dir, "old.mkv"), gone}); err != nil {
		t.Fatal(err)
	}

	RunAll(dir, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, nil, cutoff)

	// old.mkv wasn't retried, so it stays; deleted.mkv is in a scanned folder and gone.
	if got, want := loadFailedCache(stateDir), []string{filepath.Join(dir, "old.mkv")}; !slices.Equal(got, want) {
		t.Errorf("failed cache = %v, want %v", got, want)
	}
}

func TestRunAll_RetryOnlyReplacesFailedCache(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "fixed.mkv")
	if err := saveFailedCache(stateDir, []string{filepath.Join(dir, "fixed.mkv")}); err != nil {
		t.Fatal(err)
	}

	RunAll(dir, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "y"}, nil, nil, nil, time.Time{})

	if _, err := os.Stat(filepath.Join(stateDir, failedCacheFile)); !os.IsNotExist(err) {
		t.Errorf("failed cache still present after every retried file passed (stat err %v)", err)
	}
}

func TestRunAll_FailedCacheSaveErrorWarns(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "bad.mkv")
	// A file where the state directory should be makes MkdirAll fail.
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(stateDir, nil, 0644); err != nil {
		t.Fatal(err)
	}

	ident := &fakeIdentifier{err: fmt.Errorf("mkvmerge failed")}
	out := captureStdout(t, func() {
		RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})
	})
	if want := "[WARN]   could not save " + filepath.Join(stateDir, failedCacheFile); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestRunAll_CorruptFailedCacheScansAll(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "movie.mkv")
	if err := os.WriteFile(filepath.Join(stateDir, failedCacheFile), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	var stats Stats
	out := captureStdout(t, func() {
		// "y" would restrict the run to the cached paths, if any loaded.
		stats = RunAll(dir, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "y"}, nil, nil, nil, time.Time{})
	})
	if stats.Scanned != 1 {
		t.Errorf("Scanned=%d, want 1", stats.Scanned)
	}
	if want := "[WARN]   ignoring unreadable " + filepath.Join(stateDir, failedCacheFile); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestRunAll_UnknownCodecSaveErrorWarns(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "movie.mkv")
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(stateDir, nil, 0644); err != nil {
		t.Fatal(err)
	}
	info := &MKVInfo{Tracks: []Track{audioTrack(0, "eng", "A_UNKNOWN_FORMAT", 2, true)}}

	out := captureStdout(t, func() {
		RunAll(dir, stateDir, nil, &fakeIdentifier{info: info}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, nil, time.Time{})
	})
	if !strings.Contains(out, "[WARN]   could not save new formats") {
		t.Errorf("output missing the save warning:\n%s", out)
	}
	if strings.Contains(out, "New formats found") {
		t.Errorf("output claims the formats were saved:\n%s", out)
	}
}

// recordingRemuxer records each Remux call's plan.
type recordingRemuxer struct {
	plans []*TrimPlan
}

func (r *recordingRemuxer) Remux(_ string, plan *TrimPlan, _ MKVInfo) error {
	r.plans = append(r.plans, plan)
	return nil
}

func TestRunAll_TrimKeepsOnlyAudioTrackAndWarns(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "ep.mkv")
	lib := &Library{Path: dir, Overrides: []DirOverride{{Path: ".", OriginLang: "jpn"}}}

	info := &MKVInfo{Tracks: []Track{
		{ID: 0, Type: "video", Properties: TrackProperties{Language: "jpn", LanguageIETF: "ja"}},
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en", CodecID: "A_AC3", AudioChannels: 6, DefaultTrack: true,
		}},
		{ID: 2, Type: "subtitles", Properties: TrackProperties{
			Language: "fre", LanguageIETF: "fr", CodecID: "S_TEXT/ASS",
		}},
	}}
	remuxer := &recordingRemuxer{}

	out := captureStdout(t, func() {
		RunAll(dir, t.TempDir(), lib, &fakeIdentifier{info: info}, &fakeEditor{}, &fakePrompter{answer: "y"}, nil, remuxer, time.Time{})
	})

	if len(remuxer.plans) != 1 {
		t.Fatalf("Remux called %d times, want 1 (the fre sub still needs trimming)\n%s", len(remuxer.plans), out)
	}
	if ids := trackIDs(remuxer.plans[0].KeepAudio); !intsEqual(ids, []int{1}) {
		t.Errorf("KeepAudio = %v, want the only audio track [1]", ids)
	}
	if !strings.Contains(out, "[WARN]   ep.mkv: no audio track is the original language jpn") || !strings.Contains(out, "(audio: eng)") {
		t.Errorf("missing mismatch warning:\n%s", out)
	}
}
