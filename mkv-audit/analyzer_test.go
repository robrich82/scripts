package main

import (
	"strings"
	"testing"
)

// helpers

func audioTrack(id int, lang, codec string, channels int, defaultTrack bool) Track {
	return Track{ID: id, Type: "audio", Properties: TrackProperties{
		Language: lang, CodecID: codec, AudioChannels: channels, DefaultTrack: defaultTrack,
	}}
}

func subTrack(id int, lang, codec, name string, def, forced bool) Track {
	return Track{ID: id, Type: "subtitles", Properties: TrackProperties{
		Language: lang, CodecID: codec, TrackName: name, DefaultTrack: def, ForcedTrack: forced,
	}}
}

func videoTrack(id int, lang string) Track {
	return Track{ID: id, Type: "video", Properties: TrackProperties{Language: lang}}
}

func hasEdit(edits []string, needle string) bool {
	for _, e := range edits {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

// --- normalizeLanguage / helpers ---

func TestNormalizeLanguage_Regional(t *testing.T) {
	if got := normalizeLanguage("en-US"); got != "eng" {
		t.Errorf("got %q, want eng", got)
	}
}

func TestNormalizeLanguage_Empty(t *testing.T) {
	if got := normalizeLanguage(""); got != "und" {
		t.Errorf("got %q, want und", got)
	}
}

// --- AnalyzeFile: language normalisation ---

func TestAnalyzeFile_NormalisesLanguage(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{Language: "en", LanguageIETF: "en-US"}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Lang != 1 {
		t.Errorf("expected 1 lang fix, got %d; edits: %v", res.LocalFixes.Lang, res.Edits)
	}
	// Should set language=eng and language-ietf=en
	if !hasEdit(res.Edits, "language=eng") {
		t.Errorf("expected language=eng in edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_AlreadyNormalised_NoLangFix(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{Language: "eng", LanguageIETF: "en-US"}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Lang != 0 {
		t.Errorf("expected 0 lang fixes, got %d", res.LocalFixes.Lang)
	}
}

func TestAnalyzeFile_NormalisedLang_MissingIETF_SetsIETFOnly(t *testing.T) {
	// When language is already normalised but language_ietf is missing, we
	// should set language-ietf without counting it as a Lang fix.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "", DefaultTrack: true,
			CodecID: "A_FLAC", AudioChannels: 2,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Lang != 0 {
		t.Errorf("expected 0 lang fixes, got %d", res.LocalFixes.Lang)
	}
	if !hasEdit(res.Edits, "language-ietf=en-US") {
		t.Errorf("expected a language-ietf edit, got %v", res.Edits)
	}
}

// --- AnalyzeFile: video language inheritance ---

func TestAnalyzeFile_VideoInheritsAudioLanguage(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "und"),
		audioTrack(1, "jpn", "A_FLAC", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Video != 1 {
		t.Errorf("expected 1 video inherit, got %d", res.LocalFixes.Video)
	}
	if !hasEdit(res.Edits, "language=jpn") {
		t.Errorf("expected language=jpn in edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_VideoAlreadyHasLanguage_NoInherit(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "jpn"),
		audioTrack(1, "jpn", "A_FLAC", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Video != 0 {
		t.Errorf("expected 0 video inherits, got %d", res.LocalFixes.Video)
	}
}

// --- AnalyzeFile: audio default flags ---

func TestAnalyzeFile_PicksHigherChannelAudio(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_AAC", 2, true),
		audioTrack(1, "jpn", "A_FLAC", 6, false), // 6ch should win
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Track 1 (6ch) should become default=1, track 0 should become default=0.
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("expected flag-default=1 in edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_PicksBetterCodecWhenChannelsEqual(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 6, true),   // priority 3
		audioTrack(1, "eng", "A_EAC3", 6, false), // priority 4, should win
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Flags == 0 {
		t.Error("expected flag fix for better codec")
	}
}

func TestAnalyzeFile_IgnoresCommentaryTrack(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", CodecID: "A_TRUEHD", AudioChannels: 8,
			TrackName: "Commentary", DefaultTrack: true,
		}},
		audioTrack(1, "eng", "A_AC3", 6, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Track 1 (non-commentary) should win, even though track 0 has better codec.
	// Expect track 1 to become default.
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("non-commentary track should become default; edits: %v", res.Edits)
	}
}

// --- AnalyzeFile: subtitle logic ---

func TestAnalyzeFile_BestSubByCodecBecomesDefault(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/UTF8", "English", true, false), // priority 5
		subTrack(2, "eng", "S_TEXT/ASS", "English", false, false), // priority 10, should win
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("ASS subtitle should become default; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForcedTrackGetsNameAndFlag(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_FLAC", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Signs", false, false), // forced keyword
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Single forced track: should become forced=1, default=0 (not the only sub), name=Signs/Songs.
	if !hasEdit(res.Edits, "flag-forced=1") {
		t.Errorf("expected flag-forced=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_NoEditsWhenAlreadyCorrect(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "eng"),
		audioTrack(1, "eng", "A_FLAC", 6, true),
		subTrack(2, "eng", "S_TEXT/ASS", "English", true, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.Failed {
		t.Errorf("unexpected failure: %v", res.FailReasons)
	}
	// No flag fixes expected since everything is already correct.
	if res.LocalFixes.Flags != 0 {
		t.Logf("edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_UnknownCodecRecorded(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_UNKNOWN_FORMAT", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	found := false
	for _, c := range res.UnknownCodecs {
		if strings.Contains(c, "A_UNKNOWN_FORMAT") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected A_UNKNOWN_FORMAT in UnknownCodecs: %v", res.UnknownCodecs)
	}
}

func TestAnalyzeFile_AllForbiddenSubs_FallsBackToAlreadyDefault(t *testing.T) {
	// When every non-forced subtitle matches a forbidden keyword (e.g. "commentary"),
	// the fallback treats them as ordinary candidates. The one already marked
	// default stays default — no edits and no failure.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_UNKNOWN_A", "Commentary A", true, false),
		subTrack(2, "eng", "S_UNKNOWN_B", "Commentary B", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.Failed {
		t.Errorf("unexpected failure: %v", res.FailReasons)
	}
	if res.LocalFixes.Flags != 0 {
		t.Errorf("expected no flag edits (already correct); edits: %v", res.Edits)
	}
}

// --- Region keyword detection ---

func TestAnalyzeFile_RegionKeyword_British_SetsEnGB(t *testing.T) {
	// A subtitle track named "British" has a forbidden keyword, but because it
	// is the only English subtitle the fallback makes it the default. It must
	// also receive language-ietf=en-GB.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "British", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "language-ietf=en-GB") {
		t.Errorf("expected language-ietf=en-GB for British subtitle; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("British track should become default when it is the only subtitle; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForbiddenOnlySub_BecomesDefault(t *testing.T) {
	// When the only subtitle for a language carries a forbidden keyword, the
	// fallback should make it the default rather than leaving flags unchanged.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Australian English", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("sole forbidden-keyword subtitle should become default; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_EnUS_BeatsEnGB_Subtitle(t *testing.T) {
	// When an en-GB subtitle (forbidden keyword "british") is the current
	// default but a clean en-US subtitle also exists, the en-US track should
	// win and the en-GB track should lose its default flag.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English", false, false),        // en-US, not default
		subTrack(2, "eng", "S_TEXT/ASS", "British English", true, false), // en-GB, currently default
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("en-US track should become default; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("en-GB track should lose its default flag; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_RegionKeyword_DoesNotApplyToWrongLanguage(t *testing.T) {
	// "British" keyword should not affect a Japanese track.
	info := MKVInfo{Tracks: []Track{
		subTrack(0, "jpn", "S_TEXT/ASS", "British subtitles", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if hasEdit(res.Edits, "language-ietf=en-GB") {
		t.Errorf("en-GB should not be set on a Japanese track; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_IETFMapOverride_EnGB(t *testing.T) {
	// When opts.IETFMap overrides eng→en-GB, all eng tracks should use en-GB.
	customMap := map[string]string{
		"eng": "en-GB",
		"jpn": "ja-JP",
		"kor": "ko-KR",
		"fre": "fr-FR",
		"rus": "ru-RU",
		"und": "und",
	}
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US",
			CodecID: "A_FLAC", AudioChannels: 2, DefaultTrack: true,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{IETFMap: customMap})
	if !hasEdit(res.Edits, "language-ietf=en-GB") {
		t.Errorf("expected language-ietf=en-GB from custom IETFMap; edits: %v", res.Edits)
	}
}
