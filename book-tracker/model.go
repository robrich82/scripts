package main

import (
	"strings"
	"time"
)

// SeriesType is "Manga" or "Light Novel".
type SeriesType string

const (
	TypeManga      SeriesType = "Manga"
	TypeLightNovel SeriesType = "Light Novel"
)

// SeriesStatus describes the current tracking state.
type SeriesStatus string

const (
	StatusActive     SeriesStatus = "active"      // licensed in English, tracking releases
	StatusJPOnly     SeriesStatus = "jp-only"     // no English licence yet
	StatusOnlineOnly SeriesStatus = "online-only" // English only via online chapters, not volumes
	StatusCompleted  SeriesStatus = "completed"   // series done AND all volumes in Calibre
)

// Reminder is attached to a series that needs periodic manual checking.
type Reminder struct {
	Message      string     `json:"message"`
	IntervalDays int        `json:"interval_days"`
	LastReminded *time.Time `json:"last_reminded,omitempty"`
}

// Series is a single tracked manga or light novel series.
type Series struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Type   SeriesType   `json:"type"`
	Status SeriesStatus `json:"status"`

	// Next expected English volume.
	NextVolume     int        `json:"next_volume"`
	NextVolumeDate *time.Time `json:"next_volume_date,omitempty"` // nil == unknown

	Notes string `json:"notes,omitempty"`

	// "want both" – user wants to track LN and Manga editions separately.
	// RelatedID points to the companion Series entry.
	WantBoth       bool       `json:"want_both,omitempty"`
	RelatedID      string     `json:"related_id,omitempty"`
	OriginalFormat SeriesType `json:"original_format,omitempty"`

	// UnofficialInCalibre: Calibre has fan-translated copies; don't count
	// them as officially owned.
	UnofficialInCalibre bool `json:"unofficial_in_calibre,omitempty"`

	// Calibre matching. CalibreSeries overrides Name for library lookup.
	CalibreSeries string `json:"calibre_series,omitempty"`
	// OwnedVolumes is the set of volume numbers found in Calibre.
	OwnedVolumes []int `json:"owned_volumes,omitempty"`

	// External API IDs.
	MangaUpdatesID string `json:"mangaupdates_id,omitempty"`
	AniListID      int    `json:"anilist_id,omitempty"`

	// LastAPICheck is when we last fetched from MangaUpdates.
	LastAPICheck *time.Time `json:"last_api_check,omitempty"`

	// MUComplete is true when MangaUpdates reports the series as complete.
	MUComplete bool `json:"mu_complete,omitempty"`

	// IgnoredSuggestions is the list of MangaUpdates series IDs we should
	// not prompt about again (user declined).
	IgnoredSuggestions []string `json:"ignored_suggestions,omitempty"`

	// Reminder for online-only or other periodic check needs.
	Reminder *Reminder `json:"reminder,omitempty"`
}

// CalibreKey returns the name used to match against Calibre series.
func (s *Series) CalibreKey() string {
	if s.CalibreSeries != "" {
		return s.CalibreSeries
	}
	return s.Name
}

// CompletedSeries is an archived entry that is fully done and owned.
type CompletedSeries struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Type          SeriesType `json:"type"`
	TotalVolumes  int        `json:"total_volumes"`
	Notes         string     `json:"notes,omitempty"`
	CompletedDate time.Time  `json:"completed_date"`

	MangaUpdatesID string `json:"mangaupdates_id,omitempty"`
	AniListID      int    `json:"anilist_id,omitempty"`

	// Reminder still applies (e.g. waiting for fan-compiled volumes).
	Reminder *Reminder `json:"reminder,omitempty"`
}

// Store holds the full in-memory state.
type Store struct {
	Series    []*Series          `json:"series"`
	Completed []*CompletedSeries `json:"completed"`
}

// FindSeriesByID returns the series with the given ID, or nil.
func (st *Store) FindSeriesByID(id string) *Series {
	for _, s := range st.Series {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// FindSeriesByName returns the first series whose name matches (case-insensitive).
func (st *Store) FindSeriesByName(name string) *Series {
	lower := strings.ToLower(name)
	for _, s := range st.Series {
		if strings.ToLower(s.Name) == lower {
			return s
		}
	}
	return nil
}
