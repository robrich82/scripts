package main

import (
	"fmt"
	"strings"
)

// TrimPlan describes which audio/subtitle tracks to keep and the desired
// audio track order for a single MKV file. Video tracks are always kept.
type TrimPlan struct {
	// KeepAudio lists the audio tracks to retain, in the desired output order.
	KeepAudio []Track
	// KeepSubs lists the subtitle tracks to retain, in original order.
	KeepSubs []Track
	// RemoveAudio lists the audio tracks that will be dropped.
	RemoveAudio []Track
	// RemoveSubs lists the subtitle tracks that will be dropped.
	RemoveSubs []Track
	// AudioReordered is true when KeepAudio is in a different order than the
	// tracks appeared in the source file.
	AudioReordered bool
	// NeedsChange is true when any removal or reordering is required.
	NeedsChange bool
}

// PlanTrim decides which tracks to keep/remove and how to order audio.
//
// Audio keep rules (applied when originLang is non-empty and not "und"):
//   - Keep if lang == originLang (the content's original language)
//   - Keep if lang == "eng" (English is always retained)
//   - Keep if lang == "und" (language unknown; keep to be safe)
//   - If originLang is empty or "und", all audio tracks are kept.
//
// Subtitle keep rules (always applied):
//   - Keep if lang == "eng" or lang == "und"
//   - All other subtitle tracks are removed.
//
// Audio ordering (applied when originLang is known and not "eng"):
//   - Original-language tracks first, then English tracks.
//   - Within each group the original relative order is preserved.
func PlanTrim(info MKVInfo, originLang string) *TrimPlan {
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

	// --- Audio: filter then reorder ---
	var keptAudio []Track
	if originLang == "" || originLang == "und" {
		keptAudio = audioTracks
	} else {
		for _, t := range audioTracks {
			lang := normalizeLanguage(t.Properties.Language)
			if lang == originLang || lang == "eng" || lang == "und" {
				keptAudio = append(keptAudio, t)
			} else {
				plan.RemoveAudio = append(plan.RemoveAudio, t)
			}
		}
	}
	plan.KeepAudio = orderedAudio(keptAudio, originLang)

	// Detect whether ordering actually changed.
	if len(plan.KeepAudio) == len(keptAudio) {
		for i, t := range plan.KeepAudio {
			if t.ID != keptAudio[i].ID {
				plan.AudioReordered = true
				break
			}
		}
	}

	// --- Subtitles: English + und only ---
	for _, t := range subTracks {
		lang := normalizeLanguage(t.Properties.Language)
		if lang == "eng" || lang == "und" {
			plan.KeepSubs = append(plan.KeepSubs, t)
		} else {
			plan.RemoveSubs = append(plan.RemoveSubs, t)
		}
	}

	plan.NeedsChange = len(plan.RemoveAudio) > 0 || len(plan.RemoveSubs) > 0 || plan.AudioReordered
	return plan
}

// orderedAudio returns audio tracks with original-language tracks first and
// English tracks second. Within each group the original relative order is
// preserved. When originLang is empty, "und", or "eng" the input slice is
// returned unchanged.
func orderedAudio(tracks []Track, originLang string) []Track {
	if originLang == "" || originLang == "und" || originLang == "eng" {
		return tracks
	}
	var origGroup, engGroup, otherGroup []Track
	for _, t := range tracks {
		lang := normalizeLanguage(t.Properties.Language)
		switch lang {
		case originLang:
			origGroup = append(origGroup, t)
		case "eng":
			engGroup = append(engGroup, t)
		default:
			otherGroup = append(otherGroup, t)
		}
	}
	out := make([]Track, 0, len(tracks))
	out = append(out, origGroup...)
	out = append(out, engGroup...)
	out = append(out, otherGroup...)
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
