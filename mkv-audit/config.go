package main

// targetLangs is the set of ISO 639-2 language codes the tool enforces flags for.
var targetLangs = []string{"eng", "jpn", "kor"}

// forbiddenKeywords identify supplementary tracks that are never the primary
// dialogue track (commentary, accessibility, etc.). Regional English variants
// such as "British" or "Australian" are intentionally excluded from this list
// and are handled via engSubRegionPriority instead.
var forbiddenKeywords = []string{
	"commentary", "sdh", "description", "cc", "regional",
}

// forcedKeywords identify Signs/Songs/Forced subtitle tracks.
var forcedKeywords = []string{"signs", "songs", "forced"}

// codecPriorityAudio maps audio codec IDs to priority scores.
// Higher = preferred. Bitstream lossless > PCM lossless > high-bitrate lossy.
var codecPriorityAudio = map[string]int{
	"A_TRUEHD":      10,
	"A_DTS/LOSSLESS": 10,
	"A_FLAC":        9,
	"A_PCM/INT/LIT": 8,
	"A_OPUS":        6,
	"A_DTS":         5,
	"A_EAC3":        4,
	"A_AC3":         3,
	"A_AAC":         2,
	"A_VORBIS":      1,
}

// subPriority maps subtitle codec IDs to priority scores.
// Stylized (Anime) > HD image > universal text.
var subPriority = map[string]int{
	"S_TEXT/ASS":   10,
	"S_TEXT/SSA":   10,
	"S_HDMV/PGS":   8,
	"S_TEXT/WEBVTT": 6,
	"S_TEXT/UTF8":  5,
	"S_TEXT/ASCII": 5,
	"S_DVBSUB":     3,
	"S_VOBSUB":     3,
}

// ietfMap converts ISO 639-2 codes to IETF/BCP-47 tags including region.
var ietfMap = map[string]string{
	"eng": "en-US",
	"jpn": "ja-JP",
	"kor": "ko-KR",
	"fre": "fr-FR",
	"rus": "ru-RU",
	"und": "und",
}

// trackRegionKeywords maps track-name keywords to IETF region overrides.
// When a track's name contains one of these words and the language prefix
// matches, the IETF tag is overridden to the region-specific value.
var trackRegionKeywords = map[string]string{
	"british":     "en-GB",
	"american":    "en-US",
	"australian":  "en-AU",
	"canadian":    "en-CA",
	"irish":       "en-IE",
	"scottish":    "en-GB",
	"new zealand": "en-NZ",
}

// engSubRegionPriority ranks English subtitle IETF region tags by preference.
// Higher = more preferred. Unmapped tags (en-US, en-CA, generic) default to 1.
// Preference: NZ > GB ≈ IE > AU > US/CA/generic.
var engSubRegionPriority = map[string]int{
	"en-NZ": 4,
	"en-GB": 3,
	"en-IE": 3,
	"en-AU": 2,
}
