package main

import (
	"errors"
	"path/filepath"
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

func TestPrintTagSummary(t *testing.T) {
	tests := []struct {
		name string
		tags TagOutcome
		want []string
	}{
		{"nothing tagged", TagOutcome{}, nil},
		{"all copied", TagOutcome{Copied: 3}, []string{"Tags: 3 copied, 0 failed"}},
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
