package main

import (
	"strings"
	"testing"
)

// namedAudioTrack builds an audio Track with a track name (for commentary/SDH tests).
func namedAudioTrack(id int, lang, codec string, channels int, name string) Track {
	t := audioTrack(id, lang, codec, channels, false)
	t.Properties.TrackName = name
	return t
}

// namedSubTrack builds a subtitle Track with explicit name/flags.
func namedSubTrack(id int, lang, codec, name string) Track {
	return subTrack(id, lang, codec, name, false, false)
}

func trackIDs(tracks []Track) []int {
	ids := make([]int, len(tracks))
	for i, t := range tracks {
		ids[i] = t.ID
	}
	return ids
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- audioGroupWinnerFirst ---

func TestAudioGroupWinnerFirst_SingleTrack(t *testing.T) {
	group := []Track{audioTrack(0, "eng", "A_AC3", 6, false)}
	got := audioGroupWinnerFirst(group)
	if len(got) != 1 || got[0].ID != 0 {
		t.Errorf("single track: got %v", trackIDs(got))
	}
}

func TestAudioGroupWinnerFirst_WinnerByChannels(t *testing.T) {
	// 2ch AAC comes first; 6ch AC3 should be moved to front
	group := []Track{
		audioTrack(0, "eng", "A_AAC", 2, false),
		audioTrack(1, "eng", "A_AC3", 6, false),
	}
	got := audioGroupWinnerFirst(group)
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want [1,0] got %v", trackIDs(got))
	}
}

func TestAudioGroupWinnerFirst_WinnerByCodec(t *testing.T) {
	// same channels, TrueHD beats AC3
	group := []Track{
		audioTrack(0, "eng", "A_AC3", 6, false),
		audioTrack(1, "eng", "A_TRUEHD", 6, false),
	}
	got := audioGroupWinnerFirst(group)
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want [1,0] got %v", trackIDs(got))
	}
}

func TestAudioGroupWinnerFirst_AlreadyFirst(t *testing.T) {
	group := []Track{
		audioTrack(0, "eng", "A_TRUEHD", 6, true),
		audioTrack(1, "eng", "A_AC3", 2, false),
	}
	got := audioGroupWinnerFirst(group)
	if !intsEqual(trackIDs(got), []int{0, 1}) {
		t.Errorf("want [0,1] got %v", trackIDs(got))
	}
}

func TestAudioGroupWinnerFirst_CommentaryLast(t *testing.T) {
	// commentary track should follow the clean winner even if it has higher channels
	group := []Track{
		namedAudioTrack(0, "eng", "A_TRUEHD", 6, "Commentary"),
		audioTrack(1, "eng", "A_AC3", 6, false),
	}
	got := audioGroupWinnerFirst(group)
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want clean track first, got %v", trackIDs(got))
	}
}

func TestAudioGroupWinnerFirst_AllCommentaryFallback(t *testing.T) {
	// all tracks are commentary — fall back to selecting best among all
	group := []Track{
		namedAudioTrack(0, "eng", "A_AC3", 2, "Commentary"),
		namedAudioTrack(1, "eng", "A_TRUEHD", 6, "Commentary"),
	}
	got := audioGroupWinnerFirst(group)
	if got[0].ID != 1 {
		t.Errorf("want id=1 first, got %v", trackIDs(got))
	}
}

// --- subGroupWinnerFirst ---

func TestSubGroupWinnerFirst_SingleTrack(t *testing.T) {
	group := []Track{subTrack(0, "eng", "S_TEXT/ASS", "", false, false)}
	got := subGroupWinnerFirst("eng", group)
	if len(got) != 1 || got[0].ID != 0 {
		t.Errorf("single track: got %v", trackIDs(got))
	}
}

func TestSubGroupWinnerFirst_ForcedLast(t *testing.T) {
	// Signs/Songs comes first in file; main sub should be moved to front
	group := []Track{
		subTrack(0, "eng", "S_TEXT/ASS", "Signs/Songs", false, true),
		subTrack(1, "eng", "S_TEXT/ASS", "", true, false),
	}
	got := subGroupWinnerFirst("eng", group)
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want main sub first, got %v", trackIDs(got))
	}
}

func TestSubGroupWinnerFirst_ForcedAlreadyLast(t *testing.T) {
	group := []Track{
		subTrack(0, "eng", "S_TEXT/ASS", "", true, false),
		subTrack(1, "eng", "S_TEXT/ASS", "Signs/Songs", false, true),
	}
	got := subGroupWinnerFirst("eng", group)
	if !intsEqual(trackIDs(got), []int{0, 1}) {
		t.Errorf("want [0,1] got %v", trackIDs(got))
	}
}

func TestSubGroupWinnerFirst_WinnerByCodec(t *testing.T) {
	// ASS has higher subPriority than PGS
	group := []Track{
		subTrack(0, "eng", "S_HDMV/PGS", "", false, false),
		subTrack(1, "eng", "S_TEXT/ASS", "", false, false),
	}
	got := subGroupWinnerFirst("eng", group)
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want ASS first, got %v", trackIDs(got))
	}
}

func TestSubGroupWinnerFirst_EnglishRegionScore(t *testing.T) {
	// "British" keyword → en-GB which scores higher than generic English
	group := []Track{
		subTrack(0, "eng", "S_TEXT/ASS", "", false, false),
		namedSubTrack(1, "eng", "S_TEXT/ASS", "British"),
	}
	got := subGroupWinnerFirst("eng", group)
	if got[0].ID != 1 {
		t.Errorf("want British first, got %v", trackIDs(got))
	}
}

func TestSubGroupWinnerFirst_ForcedWinnerFirst(t *testing.T) {
	// Two forced tracks; the one with higher codec priority should come first.
	group := []Track{
		subTrack(0, "eng", "S_TEXT/ASS", "", true, false),    // main
		namedSubTrack(1, "eng", "S_HDMV/PGS", "Signs/Songs"), // forced, lower priority
		namedSubTrack(2, "eng", "S_TEXT/ASS", "Signs/Songs"), // forced, higher priority
	}
	got := subGroupWinnerFirst("eng", group)
	// main first (id=0), then forced winner (id=2), then other forced (id=1)
	if !intsEqual(trackIDs(got), []int{0, 2, 1}) {
		t.Errorf("want [0,2,1] got %v", trackIDs(got))
	}
}

// --- orderedAudio ---

func TestOrderedAudio_Empty(t *testing.T) {
	got := orderedAudio(nil, "jpn")
	if len(got) != 0 {
		t.Error("expected empty")
	}
}

func TestOrderedAudio_OriginLangFirst(t *testing.T) {
	tracks := []Track{
		audioTrack(0, "eng", "A_AC3", 6, false),
		audioTrack(1, "jpn", "A_FLAC", 6, false),
	}
	got := orderedAudio(tracks, "jpn")
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want jpn first, got %v", trackIDs(got))
	}
}

func TestOrderedAudio_WithinGroupWinnerFirst_OriginEng(t *testing.T) {
	// Two English tracks; 6ch FLAC should lead even when originLang == "eng"
	tracks := []Track{
		audioTrack(0, "eng", "A_AC3", 2, false),
		audioTrack(1, "eng", "A_FLAC", 6, false),
	}
	got := orderedAudio(tracks, "eng")
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want FLAC first, got %v", trackIDs(got))
	}
}

func TestOrderedAudio_NoOriginLang_WithinGroupReorder(t *testing.T) {
	tracks := []Track{
		audioTrack(0, "eng", "A_AAC", 2, false),
		audioTrack(1, "eng", "A_TRUEHD", 6, false),
	}
	got := orderedAudio(tracks, "")
	if !intsEqual(trackIDs(got), []int{1, 0}) {
		t.Errorf("want TrueHD first, got %v", trackIDs(got))
	}
}

func TestOrderedAudio_MultiGroupAndWithinGroup(t *testing.T) {
	// jpn origin: jpn group first (winner-first within), then eng group (winner-first)
	tracks := []Track{
		audioTrack(0, "eng", "A_AC3", 2, false),
		audioTrack(1, "jpn", "A_AAC", 2, false),
		audioTrack(2, "eng", "A_FLAC", 6, false),
		audioTrack(3, "jpn", "A_TRUEHD", 6, false),
	}
	got := orderedAudio(tracks, "jpn")
	// jpn winner (id=3) first, other jpn (id=1), then eng winner (id=2), other eng (id=0)
	if !intsEqual(trackIDs(got), []int{3, 1, 2, 0}) {
		t.Errorf("want [3,1,2,0] got %v", trackIDs(got))
	}
}

// --- PlanTrim subtitle reordering ---

func TestPlanTrim_SubsReordered(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "eng"),
		audioTrack(1, "eng", "A_AC3", 6, true),
		// Signs track comes before main sub — Plex would pick signs first
		subTrack(2, "eng", "S_TEXT/ASS", "Signs/Songs", false, true),
		subTrack(3, "eng", "S_TEXT/ASS", "", true, false),
	}}
	plan := PlanTrim(info, "")
	if !plan.SubsReordered {
		t.Error("expected SubsReordered=true")
	}
	if !plan.NeedsChange {
		t.Error("expected NeedsChange=true")
	}
	// main sub (id=3) should be first
	if !intsEqual(trackIDs(plan.KeepSubs), []int{3, 2}) {
		t.Errorf("want KeepSubs [3,2], got %v", trackIDs(plan.KeepSubs))
	}
}

func TestPlanTrim_SubsAlreadyOrdered(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "eng"),
		audioTrack(1, "eng", "A_AC3", 6, true),
		subTrack(2, "eng", "S_TEXT/ASS", "", true, false),
		subTrack(3, "eng", "S_TEXT/ASS", "Signs/Songs", false, true),
	}}
	plan := PlanTrim(info, "")
	if plan.SubsReordered {
		t.Error("expected SubsReordered=false when subs already in correct order")
	}
}

func TestPlanTrim_AudioWinnerFirst_NoOriginLang(t *testing.T) {
	info := MKVInfo{Tracks: []Track{
		videoTrack(0, "eng"),
		audioTrack(1, "eng", "A_AC3", 2, false),
		audioTrack(2, "eng", "A_TRUEHD", 6, false),
	}}
	plan := PlanTrim(info, "")
	if !plan.AudioReordered {
		t.Error("expected AudioReordered=true")
	}
	if plan.KeepAudio[0].ID != 2 {
		t.Errorf("want TrueHD first, got id=%d", plan.KeepAudio[0].ID)
	}
}

func TestTrimSummary_SubsReordered(t *testing.T) {
	plan := &TrimPlan{
		KeepSubs: []Track{
			subTrack(3, "eng", "S_TEXT/ASS", "", true, false),
			subTrack(2, "eng", "S_TEXT/ASS", "Signs/Songs", false, true),
		},
		SubsReordered: true,
		NeedsChange:   true,
	}
	summary := TrimSummary(plan)
	if !strings.Contains(summary, "Reorder subs") {
		t.Errorf("expected 'Reorder subs' in summary, got: %s", summary)
	}
}
