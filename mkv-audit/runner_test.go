package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)
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
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"}, nil, nil, time.Time{})

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
	stats := RunAll(dir, t.TempDir(), nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, time.Time{})

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
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"}, nil, nil, time.Time{})

	if stats.Fixed != 1 {
		t.Errorf("Fixed=%d, want 1", stats.Fixed)
	}
	if len(editor.calls) != 1 {
		t.Errorf("Apply call count=%d, want 1", len(editor.calls))
	}
}

func TestRunAll_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte{}, 0644)

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{}, nil, nil, time.Time{})
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

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, time.Time{})
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
		stats = RunAll(root, t.TempDir(), nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{}, nil, nil, time.Time{})
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
	RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, time.Time{})

	got := loadFailedCache(stateDir)
	if want := filepath.Join(dir, "bad.mkv"); len(got) != 1 || got[0] != want {
		t.Errorf("failed cache = %v, want [%s]", got, want)
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
		RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, time.Time{})
	})
	if want := "[WARN]   could not save " + failedCacheFile; !strings.Contains(out, want) {
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
		stats = RunAll(dir, stateDir, nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{answer: "y"}, nil, nil, time.Time{})
	})
	if stats.Scanned != 1 {
		t.Errorf("Scanned=%d, want 1", stats.Scanned)
	}
	if want := "[WARN]   ignoring unreadable " + failedCacheFile; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}
