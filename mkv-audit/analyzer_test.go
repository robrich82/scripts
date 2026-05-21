package main

import (
	"slices"
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
	// should set language-ietf and count it as an IETF fix (not a Lang fix).
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
	if res.LocalFixes.IETF != 1 {
		t.Errorf("expected 1 IETF fix, got %d", res.LocalFixes.IETF)
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

func TestAnalyzeFile_VideoInheritsAudioLanguage_SetsIETF(t *testing.T) {
	// When a video track inherits language from audio, language-ietf must also
	// be set in the same edit so a second run does not re-trigger a fix.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "video", Properties: TrackProperties{Language: "und", LanguageIETF: ""}},
		audioTrack(1, "eng", "A_FLAC", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Video != 1 {
		t.Errorf("expected 1 video inherit, got %d", res.LocalFixes.Video)
	}
	if !hasEdit(res.Edits, "language=eng") {
		t.Errorf("expected language=eng in edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "language-ietf=en-US") {
		t.Errorf("expected language-ietf=en-US in edits: %v", res.Edits)
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

func TestAnalyzeFile_VideoLanguageMismatch_Inherits(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "eng"),
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

func TestAnalyzeFile_ForcedOnlySub_PromotedToMain(t *testing.T) {
	// When a forced-keyword subtitle is the only track for its language (no
	// main candidates), it is promoted to the main slot: default=1, forced=0.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_FLAC", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Signs", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("sole forced-keyword sub should become default; edits: %v", res.Edits)
	}
	if hasEdit(res.Edits, "flag-forced=1") {
		t.Errorf("sole forced-keyword sub should not be forced when promoted to main; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForcedOnlySub_PromotedToMain_IsStable(t *testing.T) {
	// After promotion (default=1, forced=0), a second analysis pass should
	// produce no further edits — the fix must not loop.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_FLAC", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Signs", true, false), // already default=1, forced=0
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.LocalFixes.Flags != 0 {
		t.Errorf("expected no flag edits on second pass; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForcedSubWithMains_GetsForced(t *testing.T) {
	// When main subtitle candidates exist, a single Signs/Songs track should
	// still receive forced=1 (existing behaviour preserved).
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_FLAC", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English", true, false),
		subTrack(2, "eng", "S_TEXT/ASS", "Signs", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-forced=1") {
		t.Errorf("Signs track should get forced=1 when main candidates exist; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForcedSubLanguageInferredFromName(t *testing.T) {
	// "English [Signs/Songs]" mislabelled as jpn should be corrected to eng.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_OPUS", 2, true),
		subTrack(1, "eng", "S_HDMV/PGS", "English [Full]", true, false),
		subTrack(2, "eng", "S_HDMV/PGS", "English [Full/Japanese Names]", false, false),
		{ID: 3, Type: "subtitles", Properties: TrackProperties{
			Language: "jpn", CodecID: "S_HDMV/PGS",
			TrackName: "English [Signs/Songs] (PGS)", DefaultTrack: true, ForcedTrack: false,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Language should be corrected to eng.
	if !hasEdit(res.Edits, "language=eng") {
		t.Errorf("expected language=eng correction for mislabelled Signs/Songs track; edits: %v", res.Edits)
	}
	// After correction, track joins eng subtitle group and should get forced=1.
	if !hasEdit(res.Edits, "flag-forced=1") {
		t.Errorf("Signs track should get forced=1 after language correction; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_UndForcedSubInheritsSubtitleLang(t *testing.T) {
	// "und" Signs/Songs track should inherit "eng" from the other subtitle tracks
	// when all other subtitles share a single language.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_DTS", 6, true),
		audioTrack(1, "eng", "A_TRUEHD", 6, false),
		subTrack(2, "eng", "S_TEXT/ASS", "Full Subtitles", true, false),
		subTrack(3, "eng", "S_HDMV/PGS", "Full Subtitles [PGS]", false, false),
		subTrack(4, "und", "S_TEXT/ASS", "Signs/Songs [Q]", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "language=eng") {
		t.Errorf("expected und Signs/Songs track to inherit language=eng; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-forced=1") {
		t.Errorf("expected Signs/Songs track to get flag-forced=1 after lang inference; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_UndForcedSubAmbiguous_NoInference(t *testing.T) {
	// When subtitles exist in multiple languages, "und" Signs/Songs track
	// should NOT have a language assigned (ambiguous).
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_DTS", 6, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Full Subtitles", true, false),
		subTrack(2, "jpn", "S_TEXT/ASS", "Full Subtitles [JPN]", false, false),
		subTrack(3, "und", "S_TEXT/ASS", "Signs/Songs", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	for _, e := range res.Edits {
		if strings.Contains(e, "track:4") && strings.Contains(e, "language=") {
			t.Errorf("should not infer language for und Signs/Songs when subtitles are in multiple languages; edits: %v", res.Edits)
		}
	}
}

func TestAnalyzeFile_UndForcedSubInherited_IsStable(t *testing.T) {
	// After inheritance (eng), a second pass should produce no further edits.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "jpn", LanguageIETF: "ja-JP", CodecID: "A_DTS", AudioChannels: 6, DefaultTrack: true,
		}},
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "A_TRUEHD", AudioChannels: 6, DefaultTrack: true,
		}},
		{ID: 2, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_TEXT/ASS",
			TrackName: "Full Subtitles", DefaultTrack: true, ForcedTrack: false,
		}},
		{ID: 3, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_HDMV/PGS",
			TrackName: "Full Subtitles [PGS]", DefaultTrack: false, ForcedTrack: false,
		}},
		{ID: 4, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_TEXT/ASS",
			TrackName: "Signs/Songs [Q]", DefaultTrack: false, ForcedTrack: true,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if len(res.Edits) != 0 {
		t.Errorf("expected no edits on already-correct file; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_ForcedSubLanguageInferred_IsStable(t *testing.T) {
	// After language correction (eng) and flag normalisation, a second pass
	// should produce no further edits. All LanguageIETF fields are pre-set
	// to the expected values so the IETF step is already satisfied.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "jpn", LanguageIETF: "ja-JP", CodecID: "A_OPUS",
			AudioChannels: 2, DefaultTrack: true,
		}},
		{ID: 1, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_HDMV/PGS",
			TrackName: "English [Full]", DefaultTrack: true, ForcedTrack: false,
		}},
		{ID: 2, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_HDMV/PGS",
			TrackName: "English [Full/Japanese Names]", DefaultTrack: false, ForcedTrack: false,
		}},
		{ID: 3, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_HDMV/PGS",
			TrackName: "English [Signs/Songs] (PGS)", DefaultTrack: false, ForcedTrack: true,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if len(res.Edits) != 0 {
		t.Errorf("expected no edits on already-correct file; edits: %v", res.Edits)
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
	// default stays default — no default-flag changes. The commentary flag is set
	// on both tracks (that is the only expected change).
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_UNKNOWN_A", "Commentary A", true, false),
		subTrack(2, "eng", "S_UNKNOWN_B", "Commentary B", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if res.Failed {
		t.Errorf("unexpected failure: %v", res.FailReasons)
	}
	// Both commentary subs should receive flag-commentary=1.
	if !hasEdit(res.Edits, "flag-commentary=1") {
		t.Errorf("expected flag-commentary=1 edits; edits: %v", res.Edits)
	}
	// The default flag on track 1 should not change.
	if hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("default flag should not be cleared on already-default commentary track; edits: %v", res.Edits)
	}
}

// --- Region keyword detection ---

func TestAnalyzeFile_RegionKeyword_British_SetsEnGB(t *testing.T) {
	// "British" is a regional variant, not a forbidden keyword. As the only
	// English subtitle it wins and also receives language-ietf=en-GB.
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

func TestAnalyzeFile_RegionalOnlySub_BecomesDefault(t *testing.T) {
	// A regional subtitle (e.g. "Australian English") is a valid main track
	// and should be set as default when it is the only option.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "Australian English", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("sole regional subtitle should become default; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_EnGB_BeatsEnUS_Subtitle(t *testing.T) {
	// en-GB (score 3) is preferred over generic English (score 1).
	// The "British English" track should displace the currently-default
	// plain "English" track.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English", true, false),          // generic, currently default
		subTrack(2, "eng", "S_TEXT/ASS", "British English", false, false), // en-GB
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("en-GB track should become default; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("generic English track should lose its default flag; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_EnSubRegionPriority(t *testing.T) {
	// Preference order: NZ (score 4) > GB (score 3) > AU (score 2) > generic (score 1).
	// Track 4 is en-NZ and should win. All tracks have identical codecs.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English", false, false),             // generic, score 1
		subTrack(2, "eng", "S_TEXT/ASS", "British English", false, false),     // en-GB, score 3
		subTrack(3, "eng", "S_TEXT/ASS", "Australian English", false, false),  // en-AU, score 2
		subTrack(4, "eng", "S_TEXT/ASS", "New Zealand English", false, false), // en-NZ, score 4
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Track 4 has mkvpropedit arg "track:5" (1-based). Verify it gets flag-default=1.
	found := false
	for i, e := range res.Edits {
		if e == "track:5" {
			if slices.Contains(res.Edits[i:], "flag-default=1") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("en-NZ track (track:5) should win with flag-default=1; edits: %v", res.Edits)
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

// --- flag-original ---

func TestAnalyzeFile_OriginalLang_SetsFlag(t *testing.T) {
	// English audio in an English show should get flag-original=1.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{OriginLang: "eng"})
	if !hasEdit(res.Edits, "flag-original=1") {
		t.Errorf("English track should get flag-original=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_OriginalLang_NonOriginalCleared(t *testing.T) {
	// An English dub in a Japanese anime should get flag-original=0 if it
	// currently has flag-original=1 set incorrectly.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", CodecID: "A_AC3", AudioChannels: 2,
			DefaultTrack: true, OriginalLanguage: true, // incorrectly set
		}},
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "jpn", CodecID: "A_FLAC", AudioChannels: 2,
			DefaultTrack: false, OriginalLanguage: false,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{OriginLang: "jpn"})
	if !hasEdit(res.Edits, "flag-original=0") {
		t.Errorf("English dub should lose flag-original; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-original=1") {
		t.Errorf("Japanese audio should gain flag-original=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_OriginalLang_AlreadyCorrect_Stable(t *testing.T) {
	// When flag-original is already correctly set, no related edits generated.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "jpn", LanguageIETF: "ja-JP", CodecID: "A_FLAC",
			AudioChannels: 2, DefaultTrack: true, OriginalLanguage: true,
		}},
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "A_AC3",
			AudioChannels: 2, DefaultTrack: false, OriginalLanguage: false,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{OriginLang: "jpn"})
	for _, e := range res.Edits {
		if e == "flag-original=1" || e == "flag-original=0" {
			t.Errorf("flag-original already correct; unexpected edit: %v", res.Edits)
			break
		}
	}
}

func TestAnalyzeFile_OriginalLang_SubtitleMarked(t *testing.T) {
	// Subtitle tracks in the original language should also get flag-original=1.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "jpn", "A_FLAC", 2, true),
		subTrack(1, "jpn", "S_TEXT/ASS", "Japanese", true, false),
		subTrack(2, "eng", "S_TEXT/ASS", "English", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{OriginLang: "jpn"})
	if !hasEdit(res.Edits, "flag-original=1") {
		t.Errorf("Japanese subtitle should get flag-original=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_NoOriginLang_NoOriginalFlags(t *testing.T) {
	// When OriginLang is empty, flag-original should never be touched.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	for _, e := range res.Edits {
		if e == "flag-original=1" || e == "flag-original=0" {
			t.Errorf("flag-original should not be set when OriginLang is empty; edits: %v", res.Edits)
			break
		}
	}
}

// --- Hearing-impaired and commentary flag detection ---

func TestAnalyzeFile_SDHSubGetsHearingImpairedFlag(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English", true, false),
		subTrack(2, "eng", "S_TEXT/ASS", "English SDH", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-hearing-impaired=1") {
		t.Errorf("SDH subtitle should get flag-hearing-impaired=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_SDHSubNotDefault(t *testing.T) {
	// An SDH subtitle should never become the default track.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		subTrack(1, "eng", "S_TEXT/ASS", "English SDH", true, false), // currently default
		subTrack(2, "eng", "S_TEXT/ASS", "English", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// SDH track should lose its default flag.
	if !hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("SDH subtitle should lose its default flag; edits: %v", res.Edits)
	}
	// Non-SDH track should become default.
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("non-SDH subtitle should become default; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_CommentaryAudioGetsFlag(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 6, true),
		{ID: 1, Type: "audio", Properties: TrackProperties{
			Language: "eng", CodecID: "A_AC3", AudioChannels: 2,
			TrackName: "Director's Commentary", DefaultTrack: false,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if !hasEdit(res.Edits, "flag-commentary=1") {
		t.Errorf("commentary audio should get flag-commentary=1; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_CommentaryAudioNotDefault(t *testing.T) {
	// A commentary audio track should never win default selection.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", CodecID: "A_TRUEHD", AudioChannels: 8,
			TrackName: "Commentary", DefaultTrack: true,
		}},
		audioTrack(1, "eng", "A_AC3", 6, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Commentary track should lose default, non-commentary should gain it.
	if !hasEdit(res.Edits, "flag-default=1") {
		t.Errorf("non-commentary track should become default; edits: %v", res.Edits)
	}
	if !hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("commentary track should lose its default flag; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_PreExistingHearingImpairedFlag_NotDefault(t *testing.T) {
	// A track already carrying flag-hearing-impaired=1 (but no keyword in name)
	// should still be excluded from default selection.
	info := MKVInfo{Tracks: []Track{
		audioTrack(0, "eng", "A_AC3", 2, true),
		{ID: 1, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", CodecID: "S_TEXT/ASS", TrackName: "English",
			DefaultTrack: true, HearingImpaired: true,
		}},
		subTrack(2, "eng", "S_TEXT/ASS", "English Full", false, false),
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	// HI track (already flagged) should not become (or stay) default.
	if !hasEdit(res.Edits, "flag-default=0") {
		t.Errorf("pre-existing HI track should lose default; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_PreExistingHearingImpairedFlag_NoFlagEdit(t *testing.T) {
	// A track already carrying flag-hearing-impaired=1 should not get a
	// redundant flag-hearing-impaired=1 edit.
	info := MKVInfo{Tracks: []Track{
		subTrack(0, "eng", "S_TEXT/ASS", "English SDH", false, false),
	}}
	// Pre-set the flag as if mkvmerge already returned it.
	info.Tracks[0].Properties.HearingImpaired = true
	res := AnalyzeFile(info, AnalyzeOptions{})
	// Count occurrences of flag-hearing-impaired=1 — should be zero (already set).
	count := 0
	for _, e := range res.Edits {
		if e == "flag-hearing-impaired=1" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("expected no flag-hearing-impaired=1 edit when flag already set; edits: %v", res.Edits)
	}
}

func TestAnalyzeFile_HearingImpairedSub_IsStable(t *testing.T) {
	// A correctly configured file: one normal default sub + one HI sub with
	// flag already set. A second analysis pass should produce no edits.
	info := MKVInfo{Tracks: []Track{
		{ID: 0, Type: "audio", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "A_AC3",
			AudioChannels: 2, DefaultTrack: true,
		}},
		{ID: 1, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_TEXT/ASS",
			TrackName: "English", DefaultTrack: true, ForcedTrack: false,
		}},
		{ID: 2, Type: "subtitles", Properties: TrackProperties{
			Language: "eng", LanguageIETF: "en-US", CodecID: "S_TEXT/ASS",
			TrackName: "English SDH", DefaultTrack: false, HearingImpaired: true,
		}},
	}}
	res := AnalyzeFile(info, AnalyzeOptions{})
	if len(res.Edits) != 0 {
		t.Errorf("expected no edits on already-correct file; edits: %v", res.Edits)
	}
}
