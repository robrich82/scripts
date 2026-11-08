package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingIdentifier returns an empty MKVInfo and records each path asked for.
type recordingIdentifier struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingIdentifier) Identify(path string) (*MKVInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.paths, path) {
		r.paths = append(r.paths, path)
	}
	return &MKVInfo{}, nil
}

func (r *recordingIdentifier) ReadTagDate(string) (time.Time, bool) { return time.Time{}, false }

func (r *recordingIdentifier) names() []string {
	var out []string
	for _, p := range r.paths {
		out = append(out, filepath.Base(p))
	}
	slices.Sort(out)
	return out
}

// fakeConverter fails sources whose base name is in errs, and otherwise
// replaces src with <name>.mkv.
type fakeConverter struct {
	errs  map[string]error
	calls []string
}

func (f *fakeConverter) ConvertToMKV(src string) (string, error) {
	f.calls = append(f.calls, filepath.Base(src))
	if err, ok := f.errs[filepath.Base(src)]; ok {
		return "", err
	}
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".mkv"
	if err := os.WriteFile(dst, nil, 0o644); err != nil {
		return "", err
	}
	return dst, os.Remove(src)
}

func TestRunAll_ConvertsOtherContainers(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.mkv", "b.MP4", "c.wmv", "d.avi", "e.mov", "notes.txt"} {
		makeMKVFile(t, dir, name)
	}
	conv := &fakeConverter{errs: map[string]error{
		"c.wmv": errUnsupportedContainer,
		"d.avi": errMKVExists,
		"e.mov": errors.New("mkvmerge: exit status 2"),
	}}
	ident := &recordingIdentifier{}

	var stats Stats
	out := captureStdout(t, func() {
		stats = RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, conv, time.Time{})
	})

	if got, want := ident.names(), []string{"a.mkv", "b.mkv"}; !slices.Equal(got, want) {
		t.Errorf("scanned %v, want %v", got, want)
	}
	if stats.Converted != 1 || stats.Scanned != 2 {
		t.Errorf("Converted = %d, Scanned = %d; want 1, 2", stats.Converted, stats.Scanned)
	}
	if got, want := loadFailedCache(stateDir), []string{filepath.Join(dir, "e.mov")}; !slices.Equal(got, want) {
		t.Errorf("failed cache = %v, want %v (only the real failure)", got, want)
	}
	for _, line := range []string{
		"[CONVERT] b.MP4 → b.mkv",
		"[WARN]   c.wmv: mkvmerge can't read this container, not converting",
		"[WARN]   d.avi: d.mkv already exists, not converting",
		"[ERR]    e.mov: convert: mkvmerge: exit status 2",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output missing %q:\n%s", line, out)
		}
	}
}

func TestRunAll_NilConverterIgnoresOtherContainers(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "a.mp4")
	ident := &recordingIdentifier{}

	RunAll(dir, t.TempDir(), nil, ident, &fakeEditor{}, &fakePrompter{}, nil, nil, nil, time.Time{})

	if len(ident.paths) != 0 {
		t.Errorf("scanned %v, want nothing without a converter", ident.names())
	}
}

func TestRunAll_ConvertRespectsCutoff(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "old.mp4")
	cutoff := time.Now().Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.mp4"), old, old); err != nil {
		t.Fatal(err)
	}
	conv := &fakeConverter{}

	stats := RunAll(dir, t.TempDir(), nil, &recordingIdentifier{}, &fakeEditor{}, &fakePrompter{}, nil, nil, conv, cutoff)

	if len(conv.calls) != 0 || stats.SkippedOld != 1 {
		t.Errorf("converted %v, SkippedOld = %d; want an old source skipped", conv.calls, stats.SkippedOld)
	}
}

func TestRunAll_RetryConvertsFailedSource(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "ep.mp4")
	if err := saveFailedCache(stateDir, []string{filepath.Join(dir, "ep.mp4")}); err != nil {
		t.Fatal(err)
	}
	ident := &recordingIdentifier{}

	stats := RunAll(dir, stateDir, nil, ident, &fakeEditor{}, &fakePrompter{answer: "y"}, nil, nil, &fakeConverter{}, time.Time{})

	if stats.Converted != 1 || !slices.Equal(ident.names(), []string{"ep.mkv"}) {
		t.Errorf("Converted = %d, scanned %v; want ep.mp4 converted and ep.mkv scanned", stats.Converted, ident.names())
	}
	if _, err := os.Stat(filepath.Join(stateDir, failedCacheFile)); !os.IsNotExist(err) {
		t.Errorf("failed cache still present after the retry passed (stat err %v)", err)
	}
}

func TestRunAll_FullScanDropsFailureForSkippedSource(t *testing.T) {
	// ep.mp4 failed last time; this run skips it because ep.mkv now exists.
	// It was retried, so it leaves the failure list.
	dir, stateDir := t.TempDir(), t.TempDir()
	makeMKVFile(t, dir, "ep.mp4")
	if err := saveFailedCache(stateDir, []string{filepath.Join(dir, "ep.mp4")}); err != nil {
		t.Fatal(err)
	}
	conv := &fakeConverter{errs: map[string]error{"ep.mp4": errMKVExists}}

	RunAll(dir, stateDir, nil, &recordingIdentifier{}, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, conv, time.Time{})

	if got := loadFailedCache(stateDir); len(got) != 0 {
		t.Errorf("failed cache = %v, want empty", got)
	}
}

func TestRunAll_VerboseConverterPrintsPlan(t *testing.T) {
	dir := t.TempDir()
	makeMKVFile(t, dir, "ep.mp4")
	conv := &loggingConverter{inner: &fakeConverter{}}

	out := captureStdout(t, func() {
		RunAll(dir, t.TempDir(), nil, &recordingIdentifier{}, &fakeEditor{}, &fakePrompter{}, nil, nil, conv, time.Time{})
	})

	if !strings.Contains(out, `  convert: mkvmerge -o "ep.mkv" "ep.mp4"`) {
		t.Errorf("missing planned conversion:\n%s", out)
	}
}

// End to end with mkvmerge: an MP4 in the library is converted and the MKV
// goes through the normal scan.
func TestRunAll_ConvertsRealMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	runner, err := newRealMKVRunner()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-f", "lavfi", "-i", "sine=duration=1", "-c:v", "mpeg4", "-c:a", "aac", "-shortest", mp4).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	var stats Stats
	out := captureStdout(t, func() {
		stats = RunAll(dir, t.TempDir(), nil, runner, &fakeEditor{}, &fakePrompter{answer: "n"}, nil, nil, runner, time.Time{})
	})

	if stats.Converted != 1 || stats.Scanned != 1 || stats.Failed != 0 {
		t.Errorf("Converted = %d, Scanned = %d, Failed = %d; want 1, 1, 0\n%s", stats.Converted, stats.Scanned, stats.Failed, out)
	}
	assertOnly(t, dir, "clip.mkv")
}
