package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// editRecorder records every Apply call's edits.
type editRecorder struct {
	edits [][]string
	err   error
}

func (e *editRecorder) Apply(_ string, edits []string) error {
	e.edits = append(e.edits, edits)
	return e.err
}

// retagEdits returns the recorded calls that set an audio language to lang.
func (e *editRecorder) retagEdits(lang string) [][]string {
	var out [][]string
	for _, ed := range e.edits {
		if slices.Contains(ed, "language="+lang) {
			out = append(out, ed)
		}
	}
	return out
}

// prefixPrompter answers each prompt by its prefix and records the prompts.
type prefixPrompter struct {
	answers map[string]string // prompt prefix → answer; unmatched prompts get "n"
	asked   []string
}

func (p *prefixPrompter) Ask(q string) (string, error) {
	p.asked = append(p.asked, q)
	for prefix, ans := range p.answers {
		if strings.HasPrefix(q, prefix) {
			return ans, nil
		}
	}
	return "n", nil
}

func (p *prefixPrompter) count(prefix string) int {
	n := 0
	for _, q := range p.asked {
		if strings.HasPrefix(q, prefix) {
			n++
		}
	}
	return n
}

// runRetag runs RunAll over files with a video track and one audio track per
// audioLangs entry (IDs from 1), with originLang set by a library override.
func runRetag(t *testing.T, originLang string, files []string, audioLangs []string, ed *editRecorder, pr *prefixPrompter) (Stats, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		makeMKVFile(t, dir, f)
	}
	lib := &Library{Path: dir, Overrides: []DirOverride{{Path: ".", OriginLang: originLang}}}
	tracks := []Track{{ID: 0, Type: "video", Properties: TrackProperties{Language: originLang}}}
	for i, lang := range audioLangs {
		tracks = append(tracks, Track{ID: i + 1, Type: "audio", Properties: TrackProperties{
			Language: lang, LanguageIETF: ietfMap[lang], CodecID: "A_AC3", AudioChannels: 6, DefaultTrack: i == 0,
		}})
	}
	var stats Stats
	out := captureStdout(t, func() {
		stats = RunAll(dir, t.TempDir(), lib, &fakeIdentifier{info: &MKVInfo{Tracks: tracks}}, ed, pr, nil, &recordingRemuxer{}, nil, time.Time{})
	})
	return stats, out
}

func TestRunAll_RetagOnlyAudio_Yes(t *testing.T) {
	ed := &editRecorder{}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "y"}}
	stats, out := runRetag(t, "jpn", []string{"ep.mkv"}, []string{"eng"}, ed, pr)

	got := ed.retagEdits("jpn")
	want := []string{"--edit", "track:2", "--set", "language=jpn", "--set", "language-ietf=ja-JP"}
	if len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("retag edits = %v, want [%v]\n%s", got, want, out)
	}
	if !strings.Contains(pr.asked[0], `audio track: "eng" → jpn (ja-JP)`) {
		t.Errorf("prompt doesn't show the change: %q", pr.asked[0])
	}
	if stats.LangFixes != 1 || stats.Failed != 0 {
		t.Errorf("LangFixes = %d, Failed = %d; want 1, 0", stats.LangFixes, stats.Failed)
	}
	if !strings.Contains(out, "[RETAG]  ep.mkv: audio language set to jpn") {
		t.Errorf("missing retag confirmation:\n%s", out)
	}
}

func TestRunAll_RetagOnlyAudio_No(t *testing.T) {
	ed := &editRecorder{}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "n"}}
	_, out := runRetag(t, "jpn", []string{"ep.mkv"}, []string{"eng"}, ed, pr)

	if got := ed.retagEdits("jpn"); len(got) != 0 {
		t.Errorf("retag edits = %v, want none", got)
	}
	if !strings.Contains(out, "[SKIP]   ep.mkv (retag skipped)") || !strings.Contains(out, "[WARN]   ep.mkv") {
		t.Errorf("want the warning and a skip line:\n%s", out)
	}
}

func TestRunAll_RetagOnlyAudio_AllSkipsLaterPrompts(t *testing.T) {
	ed := &editRecorder{}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "a"}}
	runRetag(t, "jpn", []string{"a.mkv", "b.mkv"}, []string{"eng"}, ed, pr)

	if n := pr.count("[RETAG]"); n != 1 {
		t.Errorf("asked %d retag prompts, want 1", n)
	}
	if got := ed.retagEdits("jpn"); len(got) != 2 {
		t.Errorf("retag edits = %v, want one per file", got)
	}
}

func TestRunAll_RetagOnlyAudio_UnmappedLanguageSetsNoIETF(t *testing.T) {
	ed := &editRecorder{}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "y"}}
	runRetag(t, "ger", []string{"ep.mkv"}, []string{"eng"}, ed, pr)

	got := ed.retagEdits("ger")
	want := []string{"--edit", "track:2", "--set", "language=ger"}
	if len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("retag edits = %v, want [%v]", got, want)
	}
}

func TestRunAll_RetagOnlyAudio_ApplyErrorCountsFailed(t *testing.T) {
	ed := &editRecorder{err: errors.New("boom")}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "y"}}
	stats, out := runRetag(t, "jpn", []string{"ep.mkv"}, []string{"eng"}, ed, pr)

	if stats.Failed == 0 || !slices.ContainsFunc(stats.FailedPaths, func(p string) bool { return strings.HasSuffix(p, "ep.mkv") }) {
		t.Errorf("Failed = %d, FailedPaths = %v; want the file listed", stats.Failed, stats.FailedPaths)
	}
	if !strings.Contains(out, "[ERR]    ep.mkv: retag: boom") {
		t.Errorf("missing retag error:\n%s", out)
	}
}

func TestRunAll_SeveralMismatchedAudio_WarnsWithoutRetag(t *testing.T) {
	ed := &editRecorder{}
	pr := &prefixPrompter{answers: map[string]string{"[RETAG]": "y"}}
	_, out := runRetag(t, "jpn", []string{"ep.mkv"}, []string{"eng", "fre"}, ed, pr)

	if n := pr.count("[RETAG]"); n != 0 {
		t.Errorf("asked %d retag prompts, want none with several mismatched tracks", n)
	}
	if !strings.Contains(out, "(audio: eng, fre)") {
		t.Errorf("missing warning naming both tracks:\n%s", out)
	}
}
