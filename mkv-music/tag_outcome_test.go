package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeFileTimer records timestamp writes and fails them on request.
type fakeFileTimer struct {
	setTimesErr, setCreationTimeErr error
	setTimes, setCreationTime       []string
}

func (f *fakeFileTimer) CreationTimeNeedsUpdate(string, int64) bool { return true }

func (f *fakeFileTimer) SetTimes(path string, _ time.Time) error {
	f.setTimes = append(f.setTimes, path)
	return f.setTimesErr
}

func (f *fakeFileTimer) SetCreationTime(path string, _ time.Time) error {
	f.setCreationTime = append(f.setCreationTime, path)
	return f.setCreationTimeErr
}

const outcomeVideo = "Artist - Song - Song-video.mkv"

// runTagged runs a Processor over a folder holding one correctly named video
// and its audio, so the run tags it without prompting.
func runTagged(t *testing.T, tagger *fakeTagger, timer *fakeFileTimer) (TagOutcome, string, string) {
	t.Helper()
	dir := musicDir(t, "Artist - Song.flac", outcomeVideo)
	p := NewProcessor(dir, "", &fakePrompter{})
	p.tagger = tagger
	p.timer = timer
	var tags TagOutcome
	out := captureStdout(t, func() { p.Run(); tags = p.Tags() })
	return tags, out, filepath.Join(dir, outcomeVideo)
}

var headerDate = time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)

func TestRun_TagOutcome_CopiedAndSynced_CountsTagged(t *testing.T) {
	timer := &fakeFileTimer{}
	tags, _, mkv := runTagged(t, &fakeTagger{date: headerDate}, timer)

	if tags.Copied != 1 || len(tags.Failed) != 0 {
		t.Errorf("Copied = %d, Failed = %v; want 1 and none", tags.Copied, tags.Failed)
	}
	if len(timer.setTimes) != 1 || timer.setTimes[0] != mkv {
		t.Errorf("SetTimes calls = %v, want one for %s", timer.setTimes, mkv)
	}
}

func TestRun_TagOutcome_CopyFails_RecordsFailure(t *testing.T) {
	timer := &fakeFileTimer{}
	tags, out, mkv := runTagged(t, &fakeTagger{err: errors.New("mkvpropedit exited 2"), date: headerDate}, timer)

	if tags.Copied != 0 {
		t.Errorf("Copied = %d, want 0", tags.Copied)
	}
	if want := mkv + " (failed: tags)"; len(tags.Failed) != 1 || tags.Failed[0] != want {
		t.Errorf("Failed = %v, want [%s]", tags.Failed, want)
	}
	if len(timer.setTimes) != 0 {
		t.Errorf("SetTimes called %v after a failed copy", timer.setTimes)
	}
	if !strings.Contains(out, "[ERROR] tag copy failed for "+outcomeVideo) {
		t.Errorf("output missing the per-file error:\n%s", out)
	}
}

func TestRun_TagOutcome_TimestampsFail_RecordsBoth(t *testing.T) {
	timer := &fakeFileTimer{setTimesErr: errors.New("denied"), setCreationTimeErr: errors.New("denied")}
	tags, out, mkv := runTagged(t, &fakeTagger{date: headerDate}, timer)

	if tags.Copied != 0 {
		t.Errorf("Copied = %d, want 0: the timestamps weren't synced", tags.Copied)
	}
	if want := mkv + " (failed: ctime, mtime)"; len(tags.Failed) != 1 || tags.Failed[0] != want {
		t.Errorf("Failed = %v, want [%s]", tags.Failed, want)
	}
	for _, msg := range []string{"[ERROR] set ctime failed", "[ERROR] set mtime failed"} {
		if !strings.Contains(out, msg) {
			t.Errorf("output missing %q:\n%s", msg, out)
		}
	}
}

func TestRun_TagOutcome_UpToDateButTimestampFails_RecordsFailure(t *testing.T) {
	tagger := &fakeTagger{tagsMatch: true, date: headerDate}
	tags, _, mkv := runTagged(t, tagger, &fakeFileTimer{setTimesErr: errors.New("denied")})

	if len(tagger.calls) != 0 {
		t.Errorf("CopyTagsToMKV called %v, want no call: tags already match", tagger.calls)
	}
	if tags.Copied != 0 {
		t.Errorf("Copied = %d, want 0: nothing was copied", tags.Copied)
	}
	if want := mkv + " (failed: mtime)"; len(tags.Failed) != 1 || tags.Failed[0] != want {
		t.Errorf("Failed = %v, want [%s]", tags.Failed, want)
	}
}

func TestRun_TagOutcome_UpToDate_NotCounted(t *testing.T) {
	tags, _, _ := runTagged(t, &fakeTagger{tagsMatch: true, date: headerDate}, &fakeFileTimer{})

	if tags.Copied != 0 || len(tags.Failed) != 0 {
		t.Errorf("Copied = %d, Failed = %v; want 0 and none", tags.Copied, tags.Failed)
	}
}

func TestRun_TagOutcome_DateHeader(t *testing.T) {
	tests := []struct {
		name       string
		tagger     *fakeTagger
		timer      *fakeFileTimer // nil → no timer configured
		wantCopied int
		wantFailed string // "" → none
		wantOut    string
	}{
		{"no date header", &fakeTagger{}, &fakeFileTimer{}, 1, "", ""},
		{"no timer", &fakeTagger{date: headerDate}, nil, 1, "", ""},
		{"header unreadable", &fakeTagger{dateErr: errors.New("exit status 2")}, &fakeFileTimer{}, 0, "date",
			"[ERROR] read date failed for " + outcomeVideo + ": exit status 2"},
		{"ctime only fails", &fakeTagger{date: headerDate}, &fakeFileTimer{setCreationTimeErr: errors.New("denied")}, 0, "ctime", ""},
		{"mtime only fails", &fakeTagger{date: headerDate}, &fakeFileTimer{setTimesErr: errors.New("denied")}, 0, "mtime", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := musicDir(t, "Artist - Song.flac", outcomeVideo)
			p := NewProcessor(dir, "", &fakePrompter{})
			p.tagger = tt.tagger
			if tt.timer != nil {
				p.timer = tt.timer
			}
			out := captureStdout(t, func() { p.Run() })
			tags := p.Tags()

			if tags.Copied != tt.wantCopied {
				t.Errorf("Copied = %d, want %d", tags.Copied, tt.wantCopied)
			}
			var want []string
			if tt.wantFailed != "" {
				want = []string{filepath.Join(dir, outcomeVideo) + " (failed: " + tt.wantFailed + ")"}
			}
			if !slices.Equal(tags.Failed, want) {
				t.Errorf("Failed = %v, want %v", tags.Failed, want)
			}
			if tt.timer != nil && tt.tagger.date.IsZero() && len(tt.timer.setTimes) != 0 {
				t.Errorf("SetTimes called %v without a header date", tt.timer.setTimes)
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("output missing %q:\n%s", tt.wantOut, out)
			}
		})
	}
}

// A renamed video's outcome is recorded under its new name.
func TestRun_TagOutcome_Renamed_RecordsNewPath(t *testing.T) {
	for _, tt := range []struct {
		name       string
		copyErr    error
		wantCopied int
		wantFailed bool
	}{
		{"copied", nil, 1, false},
		{"copy fails", errors.New("mkvpropedit exited 2"), 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := musicDir(t, "01 - New.flac", "01 - Old - Old-video.mkv")
			p := NewProcessor(dir, "", &fakePrompter{answers: []string{"r"}})
			p.tagger = &fakeTagger{err: tt.copyErr, date: headerDate}
			p.timer = &fakeFileTimer{}
			var stats Stats
			captureStdout(t, func() { stats, _, _ = p.Run() })
			if stats.Renamed != 1 {
				t.Fatalf("Renamed = %d, want 1", stats.Renamed)
			}
			newPath := filepath.Join(dir, "01 - New - New-video.mkv")
			tags := p.Tags()
			if tags.Copied != tt.wantCopied {
				t.Errorf("Copied = %d, want %d", tags.Copied, tt.wantCopied)
			}
			var want []string
			if tt.wantFailed {
				want = []string{newPath + " (failed: tags)"}
			}
			if !slices.Equal(tags.Failed, want) {
				t.Errorf("Failed = %v, want %v", tags.Failed, want)
			}
		})
	}
}

// Outcomes add up over the videos of a run, and an upper-case .MKV is tagged.
func TestRun_TagOutcome_Accumulates(t *testing.T) {
	dir := musicDir(t, "Artist - Good.flac", "Artist - Bad.flac",
		"Artist - Good - Good-video.MKV", "Artist - Bad - Bad-video.mkv")
	p := NewProcessor(dir, "", &fakePrompter{})
	p.tagger = &fakeTagger{date: headerDate}
	p.timer = &failOnTimer{fail: "Artist - Bad - Bad-video.mkv"}
	captureStdout(t, func() { p.Run() })

	tags := p.Tags()
	want := []string{filepath.Join(dir, "Artist - Bad - Bad-video.mkv") + " (failed: mtime)"}
	if tags.Copied != 1 || !slices.Equal(tags.Failed, want) {
		t.Errorf("Copied = %d, Failed = %v; want 1 and %v", tags.Copied, tags.Failed, want)
	}
}

// failOnTimer fails SetTimes for the file named fail only.
type failOnTimer struct{ fail string }

func (f *failOnTimer) CreationTimeNeedsUpdate(string, int64) bool { return false }
func (f *failOnTimer) SetCreationTime(string, time.Time) error    { return nil }
func (f *failOnTimer) SetTimes(path string, _ time.Time) error {
	if filepath.Base(path) == f.fail {
		return errors.New("denied")
	}
	return nil
}

func TestRun_TagOutcome_NonMKV_NotTagged(t *testing.T) {
	for _, video := range []string{"Artist - Song - Song-video.mp4", "Artist - Song - Song-video.AVI"} {
		t.Run(video, func(t *testing.T) {
			dir := musicDir(t, "Artist - Song.flac", video)
			tagger := &fakeTagger{date: headerDate}
			timer := &fakeFileTimer{}
			p := NewProcessor(dir, "", &fakePrompter{})
			p.tagger = tagger
			p.timer = timer
			captureStdout(t, func() { p.Run() })

			if len(tagger.calls) != 0 {
				t.Errorf("CopyTagsToMKV called %v for a non-MKV video", tagger.calls)
			}
			if tags := p.Tags(); tags.Copied != 0 || len(tags.Failed) != 0 {
				t.Errorf("Copied = %d, Failed = %v; want 0 and none", tags.Copied, tags.Failed)
			}
			if len(timer.setTimes) != 0 {
				t.Errorf("SetTimes called %v for a non-MKV video", timer.setTimes)
			}
		})
	}
}

func TestPrintRunSummary_IncludesTagOutcome(t *testing.T) {
	out := captureStdout(t, func() {
		printRunSummary(Stats{SkippedOld: 2}, []string{`C:\m\skipped.mkv`}, nil,
			TagOutcome{Copied: 1, Failed: []string{`C:\m\b.mkv (failed: tags)`}})
	})
	want := []string{"--- Scan Complete ---", "--- SKIPPED MUSIC VIDEOS (1) ---", `C:\m\skipped.mkv`,
		"Skipped (old): 2", "--- TAGGING FAILURES (1) ---", `C:\m\b.mkv (failed: tags)`, "Tags: 1 copied, 1 failed"}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
	if strings.Index(out, "Skipped (old)") > strings.Index(out, "Tags:") {
		t.Errorf("tag summary printed before the skip counts:\n%s", out)
	}
}

func TestPrintTagSummary(t *testing.T) {
	tests := []struct {
		name string
		tags TagOutcome
		want []string
	}{
		{"nothing tagged", TagOutcome{}, nil},
		{"all copied", TagOutcome{Copied: 3}, []string{"Tags: 3 copied, 0 failed"}},
		{"only failures", TagOutcome{Failed: []string{`C:\m\a.mkv (failed: date)`}},
			[]string{"--- TAGGING FAILURES (1) ---", "Tags: 0 copied, 1 failed"}},
		{"failures listed", TagOutcome{Copied: 1, Failed: []string{`C:\m\a.mkv (failed: tags)`, `C:\m\b.mkv (failed: mtime)`}},
			[]string{"--- TAGGING FAILURES (2) ---", `C:\m\a.mkv (failed: tags)`, `C:\m\b.mkv (failed: mtime)`, "Tags: 1 copied, 2 failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printTagSummary(tt.tags) })
			if tt.want == nil && out != "" {
				t.Errorf("printed %q, want nothing", out)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
		})
	}
}
