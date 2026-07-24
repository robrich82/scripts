package main

import (
	"fmt"
	"strings"
)

// TrimPlan describes which audio/subtitle tracks to keep and the desired
// track order for a single MKV file. Video tracks are always kept.
type TrimPlan struct {
	// KeepAudio lists the audio tracks to retain, in the desired output order.
	KeepAudio []Track
	// KeepSubs lists the subtitle tracks to retain, in the desired output order.
	KeepSubs []Track
	// RemoveAudio lists the audio tracks that will be dropped.
	RemoveAudio []Track
	// RemoveSubs lists the subtitle tracks that will be dropped.
	RemoveSubs []Track
	// AudioReordered is true when KeepAudio is in a different order than the
	// tracks appeared in the source file.
	AudioReordered bool
	// SubsReordered is true when KeepSubs is in a different order than the
	// tracks appeared in the source file.
	SubsReordered bool
	// NeedsChange is true when any removal or reordering is required.
	NeedsChange bool
}

// PlanTrim decides which tracks to keep/remove and how to order audio and subtitles.
//
// accountLangs lists the ISO 639-2/B audio languages configured for the Plex
// accounts in the sibling tools' manual_data.json ("language", "alt-language",
// "alt2-language"). Someone watches in each of them, so their audio is kept
// alongside the original language.
//
// Audio keep rules (applied when originLang is non-empty and not "und"):
//   - Keep if lang == originLang (the content's original language)
//   - Keep if lang is one of accountLangs (someone watches in that language)
//   - Keep if lang == "und" (language unknown; keep to be safe)
//   - If originLang is empty or "und", all audio tracks are kept.
//
// Subtitle keep rules (always applied):
//   - Keep if lang == "und"
//   - Keep non-forced "eng" subtitles
//   - Keep forced/signs "eng" subtitles only when English audio is also kept,
//     i.e. English is the original language or an account watches the English
//     dub and so needs the signs/songs track. With nobody watching in English
//     they are dead weight, so they are removed.
//   - All other subtitle tracks are removed.
//
// Audio ordering:
//   - Within each language group the best-quality track (most channels, then
//     codec priority) is placed first, with commentary/SDH tracks last.
//   - When originLang is known and non-English, the original-language group
//     appears before English.
//
// Subtitle ordering:
//   - Within each language group the best main subtitle (by region score and
//     codec priority) is placed first, with forced/signs tracks last.
func PlanTrim(info MKVInfo, originLang string, accountLangs []string) *TrimPlan {
	plan := &TrimPlan{}

	var audioTracks, subTracks []Track
	for _, t := range info.Tracks {
		switch t.Type {
		case "audio":
			audioTracks = append(audioTracks, t)
		case "subtitles":
			subTracks = append(subTracks, t)
		}
	}

	// Languages whose audio someone actually watches: the content's original
	// language plus every Plex account's configured preference.
	unknownOrigin := originLang == "" || originLang == "und"
	keepLang := map[string]bool{"und": true}
	if !unknownOrigin {
		keepLang[originLang] = true
	} else {
		// Nothing is known about what is watched, so keep English as before
		// rather than guessing.
		keepLang["eng"] = true
	}
	for _, lang := range accountLangs {
		if lang != "" {
			keepLang[lang] = true
		}
	}

	// --- Audio: filter then reorder ---
	var keptAudio []Track
	if unknownOrigin {
		keptAudio = audioTracks
	} else {
		for _, t := range audioTracks {
			if keepLang[normalizeLanguage(t.Properties.Language)] {
				keptAudio = append(keptAudio, t)
			} else {
				plan.RemoveAudio = append(plan.RemoveAudio, t)
			}
		}
	}
	plan.KeepAudio = orderedAudio(keptAudio, originLang)

	// Detect whether audio ordering actually changed.
	if len(plan.KeepAudio) == len(keptAudio) {
		for i, t := range plan.KeepAudio {
			if t.ID != keptAudio[i].ID {
				plan.AudioReordered = true
				break
			}
		}
	}

	// --- Subtitles: English + und only, with within-lang winner-first reordering.
	// Forced/signs English subtitles are kept only when English audio is kept,
	// since they exist to caption on-screen text for someone watching in English.
	var keptSubs []Track
	for _, t := range subTracks {
		lang := normalizeLanguage(t.Properties.Language)
		forced := containsAny(t.Properties.TrackName, forcedKeywords)
		switch {
		case lang == "und",
			lang == "eng" && (!forced || keepLang["eng"]):
			keptSubs = append(keptSubs, t)
		default:
			plan.RemoveSubs = append(plan.RemoveSubs, t)
		}
	}

	// Group kept subs by language and reorder within each group (winner first).
	var subGroupOrder []string
	seenSubGroup := map[string]bool{}
	subGroups := map[string][]Track{}
	for _, t := range keptSubs {
		lang := normalizeLanguage(t.Properties.Language)
		if !seenSubGroup[lang] {
			seenSubGroup[lang] = true
			subGroupOrder = append(subGroupOrder, lang)
		}
		subGroups[lang] = append(subGroups[lang], t)
	}
	for lang := range subGroups {
		subGroups[lang] = subGroupWinnerFirst(lang, subGroups[lang])
	}
	for _, lang := range subGroupOrder {
		plan.KeepSubs = append(plan.KeepSubs, subGroups[lang]...)
	}

	// Detect whether subtitle ordering actually changed.
	for i, t := range plan.KeepSubs {
		if t.ID != keptSubs[i].ID {
			plan.SubsReordered = true
			break
		}
	}

	plan.NeedsChange = len(plan.RemoveAudio) > 0 || len(plan.RemoveSubs) > 0 || plan.AudioReordered || plan.SubsReordered
	return plan
}

// audioGroupWinnerFirst returns a copy of group with the best-quality audio
// track moved to the front. Commentary and SDH tracks are placed after clean
// tracks. Within clean or flagged tracks the original relative order is kept.
// Selection criteria mirror AnalyzeFile: most channels, then codec priority.
func audioGroupWinnerFirst(group []Track) []Track {
	if len(group) <= 1 {
		return group
	}
	var clean, flagged []Track
	for _, t := range group {
		p := t.Properties
		if !containsAny(p.TrackName, forbiddenKeywords) && !p.HearingImpaired && !p.Commentary {
			clean = append(clean, t)
		} else {
			flagged = append(flagged, t)
		}
	}
	candidates := clean
	if len(candidates) == 0 {
		candidates = group
		flagged = nil
	}

	winnerPos := 0
	for j := 1; j < len(candidates); j++ {
		wp, cp := candidates[winnerPos].Properties, candidates[j].Properties
		if cp.AudioChannels > wp.AudioChannels ||
			(cp.AudioChannels == wp.AudioChannels &&
				codecPriorityAudio[cp.CodecID] > codecPriorityAudio[wp.CodecID]) {
			winnerPos = j
		}
	}

	out := make([]Track, 0, len(group))
	out = append(out, candidates[winnerPos])
	for j, t := range candidates {
		if j != winnerPos {
			out = append(out, t)
		}
	}
	out = append(out, flagged...)
	return out
}

// subGroupWinnerFirst returns a copy of group with the best main subtitle track
// moved to the front, followed by other main tracks, then forced/signs tracks.
// Selection criteria mirror AnalyzeFile: region score (English only), then codec priority.
func subGroupWinnerFirst(lang string, group []Track) []Track {
	if len(group) <= 1 {
		return group
	}
	var mainCands, forced []Track
	for _, t := range group {
		if containsAny(t.Properties.TrackName, forcedKeywords) {
			forced = append(forced, t)
		} else if !containsAny(t.Properties.TrackName, forbiddenKeywords) &&
			!t.Properties.HearingImpaired && !t.Properties.Commentary {
			mainCands = append(mainCands, t)
		}
	}
	// Fallback: if no clean main candidates, use all non-forced tracks.
	if len(mainCands) == 0 {
		for _, t := range group {
			if !containsAny(t.Properties.TrackName, forcedKeywords) {
				mainCands = append(mainCands, t)
			}
		}
	}
	if len(mainCands) == 0 {
		return group // only forced tracks
	}

	subWinnerBetter := func(wp, cp TrackProperties, lang string) bool {
		wScore, cScore := 1, 1
		if lang == "eng" {
			wScore = engSubtitleRegionScore(wp.TrackName)
			cScore = engSubtitleRegionScore(cp.TrackName)
		}
		if cScore != wScore {
			return cScore > wScore
		}
		if subPriority[cp.CodecID] != subPriority[wp.CodecID] {
			return subPriority[cp.CodecID] > subPriority[wp.CodecID]
		}
		if cp.ForcedTrack != wp.ForcedTrack {
			return cp.ForcedTrack
		}
		return cp.DefaultTrack && !wp.DefaultTrack
	}

	winnerPos := 0
	for j := 1; j < len(mainCands); j++ {
		if subWinnerBetter(mainCands[winnerPos].Properties, mainCands[j].Properties, lang) {
			winnerPos = j
		}
	}

	// Within forced tracks, move the best one first (same region+codec+default criteria).
	if len(forced) > 1 {
		forcedWinner := 0
		for j := 1; j < len(forced); j++ {
			if subWinnerBetter(forced[forcedWinner].Properties, forced[j].Properties, lang) {
				forcedWinner = j
			}
		}
		sortedForced := make([]Track, 0, len(forced))
		sortedForced = append(sortedForced, forced[forcedWinner])
		for j, t := range forced {
			if j != forcedWinner {
				sortedForced = append(sortedForced, t)
			}
		}
		forced = sortedForced
	}

	out := make([]Track, 0, len(group))
	out = append(out, mainCands[winnerPos])
	for j, t := range mainCands {
		if j != winnerPos {
			out = append(out, t)
		}
	}
	out = append(out, forced...)
	return out
}

// orderedAudio returns audio tracks in the desired playback order:
//   - Within each language group the best-quality track (by audioGroupWinnerFirst)
//     is placed first.
//   - When originLang is known and non-English, the original-language group
//     appears before English, which appears before other languages.
//   - When originLang is empty, "und", or "eng", groups retain their original
//     appearance order from the source file.
func orderedAudio(tracks []Track, originLang string) []Track {
	if len(tracks) == 0 {
		return tracks
	}

	// Group by language, recording first-appearance order.
	var groupOrder []string
	seenGroup := map[string]bool{}
	groups := map[string][]Track{}
	for _, t := range tracks {
		lang := normalizeLanguage(t.Properties.Language)
		if !seenGroup[lang] {
			seenGroup[lang] = true
			groupOrder = append(groupOrder, lang)
		}
		groups[lang] = append(groups[lang], t)
	}

	// Within each group, move the best-quality track to the front.
	for lang := range groups {
		groups[lang] = audioGroupWinnerFirst(groups[lang])
	}

	// When original language is known and non-English, reorder groups:
	// original-language first, then English, then others.
	if originLang != "" && originLang != "und" && originLang != "eng" {
		var newOrder []string
		if seenGroup[originLang] {
			newOrder = append(newOrder, originLang)
		}
		if seenGroup["eng"] {
			newOrder = append(newOrder, "eng")
		}
		for _, lang := range groupOrder {
			if lang != originLang && lang != "eng" {
				newOrder = append(newOrder, lang)
			}
		}
		groupOrder = newOrder
	}

	out := make([]Track, 0, len(tracks))
	for _, lang := range groupOrder {
		out = append(out, groups[lang]...)
	}
	return out
}

// TrimSummary returns a human-readable description of what the trim plan will do.
func TrimSummary(plan *TrimPlan) string {
	var sb strings.Builder
	if len(plan.RemoveAudio) > 0 {
		sb.WriteString("  Remove audio:  ")
		for i, t := range plan.RemoveAudio {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(TrackDesc(t))
		}
		sb.WriteString("\n")
	}
	if len(plan.RemoveSubs) > 0 {
		sb.WriteString("  Remove subs:   ")
		for i, t := range plan.RemoveSubs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(TrackDesc(t))
		}
		sb.WriteString("\n")
	}
	if plan.AudioReordered {
		sb.WriteString("  Reorder audio: ")
		for i, t := range plan.KeepAudio {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(TrackDesc(t))
		}
		sb.WriteString("\n")
	}
	if plan.SubsReordered {
		sb.WriteString("  Reorder subs:  ")
		for i, t := range plan.KeepSubs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(TrackDesc(t))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// TrackDesc returns a short human-readable description of a track.
func TrackDesc(t Track) string {
	p := t.Properties
	lang := p.Language
	if lang == "" {
		lang = "und"
	}
	if p.TrackName != "" {
		return fmt.Sprintf("%s (%s)", lang, p.TrackName)
	}
	if t.Type == "audio" && p.CodecID != "" {
		if p.AudioChannels > 0 {
			return fmt.Sprintf("%s (%s %dch)", lang, p.CodecID, p.AudioChannels)
		}
		return fmt.Sprintf("%s (%s)", lang, p.CodecID)
	}
	if t.Type == "subtitles" && p.CodecID != "" {
		return fmt.Sprintf("%s (%s)", lang, p.CodecID)
	}
	return lang
}
