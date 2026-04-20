package main

// TrackProperties mirrors the "properties" object from mkvmerge --identify JSON.
type TrackProperties struct {
	Language         string `json:"language"`
	LanguageIETF     string `json:"language_ietf"`
	DefaultTrack     bool   `json:"default_track"`
	ForcedTrack      bool   `json:"forced_track"`
	HearingImpaired  bool   `json:"flag_hearing_impaired"`
	Commentary       bool   `json:"flag_commentary"`
	OriginalLanguage bool   `json:"flag_original"`
	TrackName        string `json:"track_name"`
	CodecID          string `json:"codec_id"`
	AudioChannels    int    `json:"audio_channels"`
}

// Track is a single audio, video, or subtitle track from mkvmerge.
type Track struct {
	ID         int             `json:"id"`
	Type       string          `json:"type"` // "video" | "audio" | "subtitles"
	Properties TrackProperties `json:"properties"`
}

// MKVInfo is the top-level structure from mkvmerge --identify --identification-format json.
type MKVInfo struct {
	Container struct {
		Properties struct {
			DateUTC *int64 `json:"date_utc"`
		} `json:"properties"`
	} `json:"container"`
	Tracks []Track `json:"tracks"`
}

// Stats accumulates results across an entire scan.
type Stats struct {
	Scanned      int
	Fixed        int
	Failed       int
	Trimmed      int
	SkippedOld   int
	LangFixes    int
	IETFFixes    int
	VideoInherit int
	FlagFixes    int
	NamesAdded   int
	DateFixes    int
	FailedPaths  []string
}
