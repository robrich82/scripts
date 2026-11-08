package main

import (
	"encoding/json"
	"time"
)

// flexInt64 unmarshals a JSON number or an RFC3339 string into a Unix timestamp (seconds).
// Newer mkvmerge versions emit date_utc as an ISO 8601 string instead of an integer.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexInt64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	*f = flexInt64(t.Unix())
	return nil
}

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
			DateUTC *flexInt64 `json:"date_utc"`
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
	Converted    int
	SkippedOld   int
	LangFixes    int
	IETFFixes    int
	VideoInherit int
	FlagFixes    int
	NamesAdded   int
	DateFixes    int
	FailedPaths  []string
}
