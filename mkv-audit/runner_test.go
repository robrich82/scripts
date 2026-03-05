package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeIdentifier returns a pre-set MKVInfo (or error) for any file.
type fakeIdentifier struct {
	info *MKVInfo
	err  error
}

func (f *fakeIdentifier) Identify(_ string) (*MKVInfo, error) {
	return f.info, f.err
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

func makeMKVFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	os.WriteFile(path, []byte{}, 0644)
	return path
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
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"})

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
	stats := RunAll(dir, t.TempDir(), nil, ident, &fakeEditor{}, &fakePrompter{})

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
	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, editor, &fakePrompter{answer: "n"})

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

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: &MKVInfo{}}, &fakeEditor{}, &fakePrompter{})
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

	stats := RunAll(dir, t.TempDir(), nil, &fakeIdentifier{info: info}, &fakeEditor{}, &fakePrompter{answer: "n"})
	if stats.Scanned != 1 {
		t.Errorf("Scanned=%d, want 1 for .mka file", stats.Scanned)
	}
}
