package main

// targetLangs is the set of ISO 639-2 language codes the tool enforces flags for.
var targetLangs = []string{"eng", "jpn", "kor"}

// forbiddenKeywords identify tracks that are NOT the primary dialogue track.
var forbiddenKeywords = []string{
	"commentary", "sdh", "description", "cc",
	"british", "canadian", "australian", "regional",
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

// ietfMap converts ISO 639-2 codes to IETF/BCP-47 tags.
var ietfMap = map[string]string{
	"eng": "en",
	"jpn": "ja",
	"kor": "ko",
	"fra": "fr",
	"rus": "ru",
	"und": "und",
}
