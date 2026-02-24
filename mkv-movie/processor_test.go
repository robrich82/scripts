package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeMediaInfoReader returns pre-set dates.
type fakeMediaInfoReader struct {
	best   string
	header string
	err    error
}

func (f *fakeMediaInfoReader) Query(_ string) (string, string, error) {
	return f.best, f.header, f.err
}

// fakeMKVSyncer records SyncHeader calls.
type fakeMKVSyncer struct {
	calls []string
	err   error
}

func (f *fakeMKVSyncer) SyncHeader(_ string, dateStr string) error {
	f.calls = append(f.calls, dateStr)
	return f.err
}

// fakeFileTimer records timestamp operations and returns a controlled ctime check result.
type fakeFileTimer struct {
	ctimeNeedsUpdate     bool
	setTimesCalls        []time.Time
	setCreationTimeCalls []time.Time
}

func (f *fakeFileTimer) CreationTimeNeedsUpdate(_ string, _ int64) bool {
	return f.ctimeNeedsUpdate
}

func (f *fakeFileTimer) SetTimes(_ string, t time.Time) error {
	f.setTimesCalls = append(f.setTimesCalls, t)
	return nil
}

func (f *fakeFileTimer) SetCreationTime(_ string, t time.Time) error {
	f.setCreationTimeCalls = append(f.setCreationTimeCalls, t)
	return nil
}

func makeTempMKV(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.mkv")
	if err != nil {
		t.Fatalf("creating temp mkv: %v", err)
	}
	f.Close()
	return f.Name()
}

// --- parseDate tests ---

func TestParseDate_FullDate(t *testing.T) {
	dt, err := parseDate("2021-06-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2021 || dt.Month() != 6 || dt.Day() != 15 {
		t.Errorf("got %v, want 2021-06-15", dt)
	}
}

func TestParseDate_YearOnly(t *testing.T) {
	dt, err := parseDate("2019")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2019 || dt.Month() != 1 || dt.Day() != 1 {
		t.Errorf("got %v, want 2019-01-01", dt)
	}
}

func TestParseDate_LongString_TruncatesToTen(t *testing.T) {
	dt, err := parseDate("2020-03-25T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dt.Year() != 2020 || dt.Month() != 3 || dt.Day() != 25 {
		t.Errorf("got %v, want 2020-03-25", dt)
	}
}

func TestParseDate_InvalidYear(t *testing.T) {
	_, err := parseDate("abcd")
	if err == nil {
		t.Error("expected error for invalid year-only string")
	}
}

// --- processFile tests ---

func TestProcessFile_NoDate_Skipped(t *testing.T) {
	path := makeTempMKV(t)
	mi := &fakeMediaInfoReader{best: "", header: ""}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, nil, timer)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped", result)
	}
}

func TestProcessFile_MediaInfoError_Error(t *testing.T) {
	path := makeTempMKV(t)
	mi := &fakeMediaInfoReader{err: fmt.Errorf("mediainfo not found")}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, nil, timer)
	if result != ResultError {
		t.Errorf("got %v, want ResultError", result)
	}
}

func TestProcessFile_HeaderDiffers_SyncsAndUpdates(t *testing.T) {
	path := makeTempMKV(t)
	// Set file mtime far in the past so mtime update is also needed.
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	mi := &fakeMediaInfoReader{best: "2021-04-10", header: ""}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(syncer.calls) != 1 || syncer.calls[0] != "2021-04-10" {
		t.Errorf("SyncHeader calls: %v", syncer.calls)
	}
}

func TestProcessFile_AlreadyCurrent_OK(t *testing.T) {
	path := makeTempMKV(t)
	target := time.Date(2021, 4, 10, 0, 0, 0, 0, time.Local)
	// Set mtime to match the target date so no update is needed.
	os.Chtimes(path, target, target)

	mi := &fakeMediaInfoReader{best: "2021-04-10", header: "2021-04-10"}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer)
	if result != ResultOK {
		t.Errorf("got %v, want ResultOK", result)
	}
	if len(syncer.calls) != 0 {
		t.Error("SyncHeader should not be called when header is current")
	}
	if len(timer.setTimesCalls) != 0 {
		t.Error("SetTimes should not be called when mtime is current")
	}
}

func TestProcessFile_YearOnly_ParsedAndUpdated(t *testing.T) {
	path := makeTempMKV(t)
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	mi := &fakeMediaInfoReader{best: "2018", header: ""}
	syncer := &fakeMKVSyncer{}
	timer := &fakeFileTimer{}

	result := processFile(path, mi, syncer, timer)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if len(syncer.calls) == 0 {
		t.Error("expected SyncHeader to be called")
	}
}

// --- walkAndProcess tests ---

func TestWalkAndProcess_SkipsNonMKV(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte{}, 0644)

	mi := &fakeMediaInfoReader{best: ""}
	stats := walkAndProcess(dir, mi, nil, &fakeFileTimer{})
	if stats.Skipped != 0 || stats.Updated != 0 || stats.Errors != 0 {
		t.Errorf("unexpected stats for non-mkv file: %+v", stats)
	}
}

func TestWalkAndProcess_CountsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte{}, 0644)

	mi := &fakeMediaInfoReader{best: ""} // no date -> skipped
	stats := walkAndProcess(dir, mi, nil, &fakeFileTimer{})
	if stats.Skipped != 1 {
		t.Errorf("got Skipped=%d, want 1", stats.Skipped)
	}
}
