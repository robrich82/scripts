package main

import (
	"fmt"
	"slices"
	"strings"
)

// AnalysisResult is the output of analyzing a single MKV file.
type AnalysisResult struct {
	// Edits is the flat list of mkvpropedit arguments to apply (may be empty).
	Edits []string
	// Failed is true when the track layout is ambiguous and cannot be auto-fixed.
	Failed bool
	// FailReasons describes why the file was marked failed.
	FailReasons []string
	// LocalFixes counts what types of changes are included in Edits.
	LocalFixes LocalFixes
	// UnknownCodecs collects codec IDs not in our priority maps.
	UnknownCodecs []string
	// UpdatedTracks is the in-memory track state after all normalisations. Use
	// this instead of the original MKVInfo.Tracks when making decisions that
	// depend on corrected language tags (e.g. trim planning).
	UpdatedTracks []Track
}

// LocalFixes counts individual fix types within one file analysis.
type LocalFixes struct {
	Lang  int
	IETF  int
	Video int
	Flags int
	Names int
}

// normalizeLanguage aggressively maps regional codes (en-US, ja_JP) to
// ISO 639-2 three-letter codes.
func normalizeLanguage(code string) string {
	if code == "" {
		return "und"
	}
	c := strings.ToLower(strings.ReplaceAll(code, "_", "-"))
	switch {
	case strings.HasPrefix(c, "en") || c == "eng": // includes enm (Middle English)
		return "eng"
	case strings.HasPrefix(c, "ja") || strings.HasPrefix(c, "jp") || c == "jap" || c == "japn":
		return "jpn"
	case strings.HasPrefix(c, "ko") || strings.HasPrefix(c, "kr") || c == "kor":
		return "kor"
	case strings.HasPrefix(c, "fr"):
		return "fre"
	case strings.HasPrefix(c, "ru") || c == "rus":
		return "rus"
	default:
		return c
	}
}

// getIETFLanguage converts an ISO 639-2 code to its IETF/BCP-47 tag using
// the global ietfMap. Unknown codes are passed through unchanged.
func getIETFLanguage(iso string) string {
	if tag, ok := ietfMap[iso]; ok {
		return tag
	}
	return iso
}

// AnalyzeOptions carries per-file settings for AnalyzeFile.
type AnalyzeOptions struct {
	// IETFMap overrides the global ietfMap for this file. nil = use global.
	IETFMap map[string]string
	// OriginLang is the ISO 639-2/B code of the content's original language
	// as reported by TMDB (e.g. "eng", "jpn"). When set, flag-original is
	// normalised on all tracks: 1 for tracks in that language, 0 for others.
	// "" disables flag-original processing.
	OriginLang string
	// UndLang is the ISO 639-2/B code to use as a last-resort fallback for
	// tracks whose language is "und" and cannot be inferred from context.
	// Derived from Library.UndLang / DirOverride.UndLang in config.json.
	// "" disables the fallback.
	UndLang string
}

// effectiveIETFTag resolves the IETF tag for an ISO 639-2 code using either
// opts.IETFMap (if set) or the global ietfMap.
func effectiveIETFTag(norm string, opts AnalyzeOptions) string {
	m := ietfMap
	if opts.IETFMap != nil {
		m = opts.IETFMap
	}
	if tag, ok := m[norm]; ok {
		return tag
	}
	return norm
}

// applyRegionKeyword checks a track name for regional descriptors (e.g.
// "British", "Australian") and returns a region-specific IETF override when
// the keyword's language prefix matches the base tag's language prefix.
// If no keyword matches, baseIETF is returned unchanged.
func applyRegionKeyword(baseIETF, trackName string) string {
	lower := strings.ToLower(trackName)
	baseLang := strings.SplitN(baseIETF, "-", 2)[0]
	for keyword, override := range trackRegionKeywords {
		if strings.Contains(lower, keyword) {
			overrideLang := strings.SplitN(override, "-", 2)[0]
			if baseLang == overrideLang {
				return override
			}
		}
	}
	return baseIETF
}

// inferForcedSubLanguage checks whether a Signs/Songs/Forced subtitle track
// name starts with a language keyword from trackNameLanguageHints (e.g.
// "English [Signs/Songs]" → "eng"). Returns "" when no match is found.
func inferForcedSubLanguage(trackName string) string {
	lower := strings.ToLower(trackName)
	for keyword, iso := range trackNameLanguageHints {
		if strings.HasPrefix(lower, keyword) {
			return iso
		}
	}
	return ""
}

// containsAny returns true if name (lowercased) contains any of the keywords.
func containsAny(name string, keywords []string) bool {
	lower := strings.ToLower(name)
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// isForcedSubName reports whether a subtitle track name marks a Signs/Songs/
// Forced track. A "dialogue" qualifier (e.g. "Dialogue & Signs") marks a full
// track that merely includes signs, unless "forced" is explicit.
func isForcedSubName(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "forced") {
		return true
	}
	return containsAny(lower, forcedKeywords) && !strings.Contains(lower, "dialog")
}

// engSubtitleRegionScore returns a preference score for English subtitle
// track selection when multiple regional variants are available. It detects
// the effective IETF region via trackRegionKeywords and looks up the score in
// engSubRegionPriority. Unmapped regions (en-US, en-CA, untagged) return 1.
func engSubtitleRegionScore(trackName string) int {
	ietf := applyRegionKeyword("en", trackName)
	if score, ok := engSubRegionPriority[ietf]; ok {
		return score
	}
	return 1
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// trackEdit returns the mkvpropedit args to set one or more properties on a track.
func trackEdit(id int, kvPairs ...string) []string {
	args := []string{"--edit", fmt.Sprintf("track:%d", id+1)}
	for i := 0; i+1 < len(kvPairs); i += 2 {
		args = append(args, "--set", kvPairs[i]+"="+kvPairs[i+1])
	}
	return args
}

// AnalyzeFile inspects the track layout of an MKV file and returns the
// mkvpropedit edits needed to normalise it. The function is pure (no I/O)
// so it is fully unit-testable. opts.IETFMap overrides the global ietfMap
// for per-directory region settings; pass AnalyzeOptions{} for defaults.
func AnalyzeFile(info MKVInfo, opts AnalyzeOptions) AnalysisResult {
	var res AnalysisResult

	allTracks := info.Tracks
	// Work on copies of Properties so we can mutate language after normalising.
	tracks := make([]Track, len(allTracks))
	copy(tracks, allTracks)

	// --- 1. Language normalisation ---
	for i := range tracks {
		props := &tracks[i].Properties
		norm := normalizeLanguage(props.Language)

		// For subtitle tracks, infer the language from the track name when it
		// starts with a known language word (e.g. "English Subtitles" mislabelled
		// as jpn → correct to eng). Applies to all subtitle tracks, not just
		// forced/Signs/Songs, so releases that tag English subs as jpn are caught.
		if tracks[i].Type == "subtitles" {
			if inferred := inferForcedSubLanguage(props.TrackName); inferred != "" && inferred != norm {
				norm = inferred
			}
		}

		normIETF := effectiveIETFTag(norm, opts)
		// Apply per-track region detection from the track name (e.g. "British" → en-GB).
		normIETF = applyRegionKeyword(normIETF, props.TrackName)

		effMap := ietfMap
		if opts.IETFMap != nil {
			effMap = opts.IETFMap
		}
		_, hasIETFMapping := effMap[norm]
		needsLang := props.Language != norm
		// Only update language-ietf for languages we have an explicit mapping
		// for. Languages outside ietfMap (e.g. "ara", "ger", "chi") already
		// carry a correct IETF tag set by the muxer; touching them with our
		// ISO-639-2 fallback would write a wrong value and cause a loop.
		needsIETF := hasIETFMapping && props.LanguageIETF != normIETF
		if needsLang || needsIETF {
			var pairs []string
			if needsLang {
				pairs = append(pairs, "language", norm)
			}
			if (needsLang && hasIETFMapping) || needsIETF {
				pairs = append(pairs, "language-ietf", normIETF)
			}
			res.Edits = append(res.Edits, trackEdit(tracks[i].ID, pairs...)...)
			props.Language = norm
			if hasIETFMapping {
				props.LanguageIETF = normIETF
			}
			if needsLang {
				res.LocalFixes.Lang++
			} else if needsIETF {
				res.LocalFixes.IETF++
			}
		}
	}

	// --- 1b. Infer language for "und" subtitle tracks ---
	// When all other subtitle tracks in the file share a single language, assign
	// that language to any "und" subtitle whose language wasn't resolved above.
	{
		subLangs := map[string]bool{}
		for _, t := range tracks {
			if t.Type == "subtitles" && t.Properties.Language != "und" {
				subLangs[t.Properties.Language] = true
			}
		}
		if len(subLangs) == 1 {
			var inferredLang string
			for k := range subLangs {
				inferredLang = k
			}
			effMap := ietfMap
			if opts.IETFMap != nil {
				effMap = opts.IETFMap
			}
			for i := range tracks {
				props := &tracks[i].Properties
				if tracks[i].Type != "subtitles" || props.Language != "und" {
					continue
				}
				normIETF := effectiveIETFTag(inferredLang, opts)
				normIETF = applyRegionKeyword(normIETF, props.TrackName)
				_, hasIETFMapping := effMap[inferredLang]
				pairs := []string{"language", inferredLang}
				if hasIETFMapping {
					pairs = append(pairs, "language-ietf", normIETF)
				}
				res.Edits = append(res.Edits, trackEdit(tracks[i].ID, pairs...)...)
				props.Language = inferredLang
				if hasIETFMapping {
					props.LanguageIETF = normIETF
				}
				res.LocalFixes.Lang++
			}
		}
	}

	// --- 1c. Promote remaining "und" tracks via und_lang config (last resort) ---
	// Runs after 1b so that per-file inference (other subtitle tracks) always
	// takes priority over the library/directory fallback.
	if opts.UndLang != "" {
		promoted := opts.UndLang
		effMap := ietfMap
		if opts.IETFMap != nil {
			effMap = opts.IETFMap
		}
		for i := range tracks {
			props := &tracks[i].Properties
			if props.Language != "und" {
				continue
			}
			normIETF := effectiveIETFTag(promoted, opts)
			normIETF = applyRegionKeyword(normIETF, props.TrackName)
			_, hasIETFMapping := effMap[promoted]
			pairs := []string{"language", promoted}
			if hasIETFMapping {
				pairs = append(pairs, "language-ietf", normIETF)
			}
			res.Edits = append(res.Edits, trackEdit(tracks[i].ID, pairs...)...)
			props.Language = promoted
			if hasIETFMapping {
				props.LanguageIETF = normIETF
			}
			res.LocalFixes.Lang++
		}
	}

	// --- 2. Hearing-impaired and commentary flag detection ---
	// Set flag-hearing-impaired / flag-commentary on tracks whose name signals
	// their purpose but whose MKV flag is not yet set. Tracks that already
	// carry these flags are left unchanged but are also captured in-memory so
	// the default-selection logic can exclude them.
	for i := range tracks {
		props := &tracks[i].Properties
		if !props.HearingImpaired && containsAny(props.TrackName, sdhKeywords) {
			res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-hearing-impaired", "1")...)
			props.HearingImpaired = true
			res.LocalFixes.Flags++
		}
		if !props.Commentary && containsAny(props.TrackName, commentaryKeywords) {
			res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-commentary", "1")...)
			props.Commentary = true
			res.LocalFixes.Flags++
		}
	}

	// --- 3. Video language inheritance ---
	var videoTracks, audioTracks []int
	for i, t := range tracks {
		switch t.Type {
		case "video":
			videoTracks = append(videoTracks, i)
		case "audio":
			audioTracks = append(audioTracks, i)
		}
	}
	if len(videoTracks) > 0 && len(audioTracks) > 0 {
		firstAudioLang := tracks[audioTracks[0]].Properties.Language
		firstAudioIETF := effectiveIETFTag(firstAudioLang, opts)
		// effectiveIETFTag returns the input unchanged when no mapping exists,
		// so firstAudioIETF != firstAudioLang iff there is a known IETF mapping.
		firstAudioHasIETF := firstAudioIETF != firstAudioLang
		for _, vi := range videoTracks {
			vp := &tracks[vi].Properties
			needsLang := vp.Language != firstAudioLang && firstAudioLang != "und"
			needsIETF := firstAudioHasIETF && vp.LanguageIETF != firstAudioIETF
			if needsLang || needsIETF {
				var pairs []string
				if needsLang {
					pairs = append(pairs, "language", firstAudioLang)
				}
				if firstAudioHasIETF {
					pairs = append(pairs, "language-ietf", firstAudioIETF)
				}
				res.Edits = append(res.Edits, trackEdit(tracks[vi].ID, pairs...)...)
				vp.Language = firstAudioLang
				if firstAudioHasIETF {
					vp.LanguageIETF = firstAudioIETF
				}
				res.LocalFixes.Video++
			}
		}
	}

	// --- 4. flag-original normalisation ---
	// Runs after video language inheritance so video tracks already carry the
	// inherited audio language when we evaluate flag-original. This prevents a
	// two-pass loop where run 1 inherits the language and run 2 then sets the flag.
	// Skipped when OriginLang is empty or "und".
	if opts.OriginLang != "" && opts.OriginLang != "und" {
		for i := range tracks {
			props := &tracks[i].Properties
			trackLang := normalizeLanguage(props.Language)
			wantOriginal := trackLang == opts.OriginLang
			if wantOriginal && !props.OriginalLanguage {
				res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-original", "1")...)
				props.OriginalLanguage = true
				res.LocalFixes.Flags++
			} else if !wantOriginal && props.OriginalLanguage {
				res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-original", "0")...)
				props.OriginalLanguage = false
				res.LocalFixes.Flags++
			}
		}
	}

	// --- 5. Flag & naming logic per target language ---
	for _, lang := range targetLangs {
		// Collect audio tracks for this language.
		var langAudios []int
		for i, t := range tracks {
			if t.Type == "audio" && normalizeLanguage(t.Properties.Language) == lang {
				langAudios = append(langAudios, i)
			}
		}
		if len(langAudios) > 0 {
			for _, i := range langAudios {
				if _, known := codecPriorityAudio[tracks[i].Properties.CodecID]; !known {
					res.UnknownCodecs = append(res.UnknownCodecs, "Audio: "+tracks[i].Properties.CodecID)
				}
			}

			// Exclude commentary/SDH tracks to find the clean candidates.
			var cleanAudio []int
			for _, i := range langAudios {
				p := tracks[i].Properties
				if !containsAny(p.TrackName, forbiddenKeywords) && !p.HearingImpaired && !p.Commentary {
					cleanAudio = append(cleanAudio, i)
				}
			}
			candidates := cleanAudio
			if len(candidates) == 0 {
				candidates = langAudios
			}

			// Winner: most channels, then highest codec priority.
			winnerIdx := candidates[0]
			for _, i := range candidates[1:] {
				wp := tracks[winnerIdx].Properties
				ip := tracks[i].Properties
				if ip.AudioChannels > wp.AudioChannels ||
					(ip.AudioChannels == wp.AudioChannels &&
						codecPriorityAudio[ip.CodecID] > codecPriorityAudio[wp.CodecID]) {
					winnerIdx = i
				}
			}

			for _, i := range langAudios {
				targetDef := 0
				if i == winnerIdx {
					targetDef = 1
				}
				if boolToInt(tracks[i].Properties.DefaultTrack) != targetDef {
					res.Edits = append(res.Edits, trackEdit(tracks[i].ID,
						"flag-default", fmt.Sprintf("%d", targetDef))...)
					res.LocalFixes.Flags++
				}
			}
		}

		// Collect subtitle tracks for this language.
		var subTracks []int
		for i, t := range tracks {
			if t.Type == "subtitles" && normalizeLanguage(t.Properties.Language) == lang {
				subTracks = append(subTracks, i)
			}
		}
		if len(subTracks) == 0 {
			continue
		}

		for _, i := range subTracks {
			if _, known := subPriority[tracks[i].Properties.CodecID]; !known {
				res.UnknownCodecs = append(res.UnknownCodecs, "Subtitle: "+tracks[i].Properties.CodecID)
			}
		}

		var mainCands, forcedCands []int
		for _, i := range subTracks {
			p := tracks[i].Properties
			if isForcedSubName(p.TrackName) {
				forcedCands = append(forcedCands, i)
			} else if !containsAny(p.TrackName, forbiddenKeywords) && !p.HearingImpaired && !p.Commentary {
				mainCands = append(mainCands, i)
			}
		}
		// Fallback: if every non-forced subtitle matched a forbidden keyword
		// (e.g. the only English sub is labelled "British"), treat all
		// non-forced tracks as candidates — same as the audio fallback above.
		if len(mainCands) == 0 {
			for _, i := range subTracks {
				if !isForcedSubName(tracks[i].Properties.TrackName) {
					mainCands = append(mainCands, i)
				}
			}
		}

		if len(mainCands) > 0 {
			// Select best main subtitle. For English, prefer by regional
			// score first (NZ > GB > AU > US/generic), then codec priority.
			winnerIdx := mainCands[0]
			for _, i := range mainCands[1:] {
				wp := tracks[winnerIdx].Properties
				ip := tracks[i].Properties
				wScore, iScore := 1, 1
				if lang == "eng" {
					wScore = engSubtitleRegionScore(wp.TrackName)
					iScore = engSubtitleRegionScore(ip.TrackName)
				}
				if iScore > wScore ||
					(iScore == wScore && subPriority[ip.CodecID] > subPriority[wp.CodecID]) {
					winnerIdx = i
				}
			}
			// Tiebreaker: if the winner is not already the default, preserve the
			// existing default when it scores equally — avoids unnecessary churn
			// between tracks that are otherwise indistinguishable.
			if !tracks[winnerIdx].Properties.DefaultTrack {
				for _, i := range mainCands {
					if !tracks[i].Properties.DefaultTrack {
						continue
					}
					wp := tracks[winnerIdx].Properties
					ip := tracks[i].Properties
					wScore, iScore := 1, 1
					if lang == "eng" {
						wScore = engSubtitleRegionScore(wp.TrackName)
						iScore = engSubtitleRegionScore(ip.TrackName)
					}
					if iScore == wScore && subPriority[ip.CodecID] == subPriority[wp.CodecID] {
						winnerIdx = i
						break
					}
				}
			}
			if boolToInt(tracks[winnerIdx].Properties.DefaultTrack) != 1 ||
				boolToInt(tracks[winnerIdx].Properties.ForcedTrack) != 0 {
				res.Edits = append(res.Edits, trackEdit(tracks[winnerIdx].ID,
					"flag-default", "1", "flag-forced", "0")...)
				res.LocalFixes.Flags++
			}
			// Clear default on all other non-forced subs.
			for _, i := range subTracks {
				if i != winnerIdx {
					inForced := slices.Contains(forcedCands, i)
					if !inForced && tracks[i].Properties.DefaultTrack {
						res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-default", "0")...)
						res.LocalFixes.Flags++
					}
				}
			}

		} else if len(forcedCands) > 0 && len(mainCands) == 0 {
			// No main candidates — use the best forced track as the main.
			// Prefer the one already marked default, or fall back to first.
			winnerIdx := forcedCands[0]
			for _, i := range forcedCands {
				if tracks[i].Properties.DefaultTrack {
					winnerIdx = i
					break
				}
			}
			if boolToInt(tracks[winnerIdx].Properties.DefaultTrack) != 1 ||
				boolToInt(tracks[winnerIdx].Properties.ForcedTrack) != 0 {
				res.Edits = append(res.Edits, trackEdit(tracks[winnerIdx].ID,
					"flag-default", "1", "flag-forced", "0")...)
				res.LocalFixes.Flags++
			}
			for _, i := range forcedCands {
				if i != winnerIdx {
					if tracks[i].Properties.DefaultTrack || tracks[i].Properties.ForcedTrack {
						res.Edits = append(res.Edits, trackEdit(tracks[i].ID,
							"flag-default", "0", "flag-forced", "0")...)
						res.LocalFixes.Flags++
					}
				}
			}

		} else if len(subTracks) > 1 {
			// No explicit main or forced candidates — use codec priority.
			winnerIdx := subTracks[0]
			for _, i := range subTracks[1:] {
				if subPriority[tracks[i].Properties.CodecID] > subPriority[tracks[winnerIdx].Properties.CodecID] {
					winnerIdx = i
				}
			}
			if subPriority[tracks[winnerIdx].Properties.CodecID] == 0 {
				res.Failed = true
				res.FailReasons = append(res.FailReasons, fmt.Sprintf("No clear %s main track", lang))
			} else {
				if boolToInt(tracks[winnerIdx].Properties.DefaultTrack) != 1 ||
					boolToInt(tracks[winnerIdx].Properties.ForcedTrack) != 0 {
					res.Edits = append(res.Edits, trackEdit(tracks[winnerIdx].ID,
						"flag-default", "1", "flag-forced", "0")...)
					res.LocalFixes.Flags++
				}
				for _, i := range subTracks {
					if i != winnerIdx && tracks[i].Properties.DefaultTrack {
						res.Edits = append(res.Edits, trackEdit(tracks[i].ID, "flag-default", "0")...)
						res.LocalFixes.Flags++
					}
				}
			}
		}

		// Handle forced/Signs tracks (single or multiple with mains).
		// Guard: only run when mainCands is non-empty. When mainCands is empty
		// the else-if branch above already promoted the forced track to main
		// (forced=0, default=1). Running this block in that state would flip
		// forced back to 1, then the else-if branch would flip it back to 0
		// on the next pass — an infinite fix loop.
		if len(forcedCands) == 1 && len(mainCands) > 0 {
			fi := forcedCands[0]
			isOnlySub := 0
			if len(subTracks) == 1 {
				isOnlySub = 1
			}
			if boolToInt(tracks[fi].Properties.DefaultTrack) != isOnlySub ||
				boolToInt(tracks[fi].Properties.ForcedTrack) != 1 {
				res.Edits = append(res.Edits, trackEdit(tracks[fi].ID,
					"flag-default", fmt.Sprintf("%d", isOnlySub),
					"flag-forced", "1")...)
				res.LocalFixes.Flags++
			}
			if tracks[fi].Properties.TrackName == "" {
				res.Edits = append(res.Edits, trackEdit(tracks[fi].ID, "name", "Signs/Songs")...)
				res.LocalFixes.Names++
			}
		} else if len(forcedCands) > 1 && len(mainCands) > 0 {
			// Multiple Signs/Songs + main tracks: keep one forced as default+forced.
			var defaultForcedIdx int = -1
			for _, i := range forcedCands {
				if tracks[i].Properties.DefaultTrack {
					defaultForcedIdx = i
					break
				}
			}
			if defaultForcedIdx >= 0 {
				if boolToInt(tracks[defaultForcedIdx].Properties.DefaultTrack) != 1 ||
					boolToInt(tracks[defaultForcedIdx].Properties.ForcedTrack) != 1 {
					res.Edits = append(res.Edits, trackEdit(tracks[defaultForcedIdx].ID,
						"flag-default", "1", "flag-forced", "1")...)
					res.LocalFixes.Flags++
				}
				for _, i := range forcedCands {
					if i != defaultForcedIdx {
						if tracks[i].Properties.DefaultTrack || tracks[i].Properties.ForcedTrack {
							res.Edits = append(res.Edits, trackEdit(tracks[i].ID,
								"flag-default", "0", "flag-forced", "0")...)
							res.LocalFixes.Flags++
						}
					}
				}
			}
		}
	}

	res.UpdatedTracks = tracks
	return res
}
