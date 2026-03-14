package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const muBase = "https://api.mangaupdates.com/v1"

// MUClient talks to the MangaUpdates API v1.
type MUClient struct{ hc *http.Client }

func newMUClient() *MUClient {
	return &MUClient{hc: &http.Client{Timeout: 15 * time.Second}}
}

// --- request helpers ---------------------------------------------------------

func (c *MUClient) post(path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Post(muBase+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("MangaUpdates %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *MUClient) get(path string, out any) error {
	resp, err := c.hc.Get(muBase + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("MangaUpdates %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- types -------------------------------------------------------------------

type muSearchResult struct {
	TotalHits int `json:"total_hits"`
	Results   []struct {
		Record muSeries `json:"record"`
	} `json:"results"`
}

type muSeries struct {
	SeriesID int64  `json:"series_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Type     string `json:"type"` // "Manga", "Novel", etc.
	Year     string `json:"year"`
	// Description is omitted: the search endpoint returns it as a plain string
	// but the detail endpoint returns an object; we don't use it either way.
	Status string `json:"status"` // "Ongoing", "Complete", "Hiatus"
	// Licensed is a bool in the API.
	Licensed            bool          `json:"licensed"`
	CompletelyScanlated bool          `json:"completely_scanlated"`
	Publishers          []muPublisher `json:"publishers"`
	RelatedSeries       []muRelated   `json:"related_series"`
	Associated          []struct {
		Title string `json:"title"`
	} `json:"associated"`
}

type muPublisher struct {
	PublisherName string `json:"publisher_name"`
	Type          string `json:"type"` // "Original", "English", etc.
	Notes         string `json:"notes"`
}

type muRelated struct {
	RelationID    int64  `json:"relation_id"`
	RelationTitle string `json:"relation_title"`
	RelationType  string `json:"relation_type"` // "Sequel", "Prequel", "Side story", "Spin-off", etc.
}

type muReleaseSearch struct {
	TotalHits int `json:"total_hits"`
	Results   []struct {
		Record muRelease `json:"record"`
	} `json:"results"`
}

type muRelease struct {
	Title   string `json:"title"`
	Volume  string `json:"volume"`
	Chapter string `json:"chapter"`
	Date    string `json:"release_date"` // "YYYY-MM-DD"
	Groups  []struct {
		Name string `json:"name"`
	} `json:"groups"`
}

// --- public API --------------------------------------------------------------

// SearchSeries searches for a series by title and returns up to 5 results.
// preferType, if non-empty (e.g. "Novel", "Manga"), filters the API results
// to that type and fetches more candidates so the right entry isn't crowded out.
func (c *MUClient) SearchSeries(title string, preferType string) ([]muSeries, error) {
	body := map[string]any{
		"search":  title,
		"perpage": 5,
	}
	if preferType != "" {
		body["type"] = []string{preferType}
		body["perpage"] = 8 // fetch more when filtering so the correct entry isn't cut off
	}
	var res muSearchResult
	err := c.post("/series/search", body, &res)
	if err != nil {
		return nil, err
	}
	out := make([]muSeries, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, r.Record)
	}
	return out, nil
}

// GetSeries fetches full series details by numeric ID.
func (c *MUClient) GetSeries(id int64) (*muSeries, error) {
	var s muSeries
	err := c.get(fmt.Sprintf("/series/%d", id), &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// LatestEnglishRelease returns the most recent official English volume release
// for the series and any releases for future (announced) volumes.
// Returns (latestVolume, latestDate, error).
func (c *MUClient) LatestEnglishRelease(seriesID int64) (volume int, date *time.Time, err error) {
	var res muReleaseSearch
	err = c.post("/releases/search", map[string]any{
		"series_id": seriesID,
		"perpage":   50,
		"orderby":   "date",
		"asc":       "desc",
	}, &res)
	if err != nil {
		return 0, nil, err
	}

	// Walk results, pick the highest-volume official English release.
	for _, r := range res.Results {
		rel := r.Record
		// Skip scanlations — official releases have a publisher group name
		// that matches known publishers (heuristic: not a scanlation group name).
		if !isOfficialRelease(rel) {
			continue
		}
		vol := parseVolume(rel.Volume)
		if vol <= 0 {
			continue
		}
		t, parseErr := time.Parse("2006-01-02", rel.Date)
		if parseErr != nil {
			continue
		}
		if vol > volume {
			volume = vol
			tt := t
			date = &tt
		}
	}
	return volume, date, nil
}

// VolumeRelease returns the release date for a specific volume, or nil if not found.
func (c *MUClient) VolumeRelease(seriesID int64, targetVol int) *time.Time {
	var res muReleaseSearch
	err := c.post("/releases/search", map[string]any{
		"series_id": seriesID,
		"perpage":   50,
		"orderby":   "date",
		"asc":       "asc",
	}, &res)
	if err != nil {
		return nil
	}
	for _, r := range res.Results {
		rel := r.Record
		if !isOfficialRelease(rel) {
			continue
		}
		if parseVolume(rel.Volume) == targetVol {
			t, err := time.Parse("2006-01-02", rel.Date)
			if err == nil {
				tt := t
				return &tt
			}
		}
	}
	return nil
}

// EnglishPublisher returns the first English publisher name for the series, or "".
func EnglishPublisher(s *muSeries) string {
	for _, p := range s.Publishers {
		if strings.EqualFold(p.Type, "English") {
			return p.PublisherName
		}
	}
	return ""
}

// IsComplete reports whether MangaUpdates considers the series finished
// in both its country of origin and in English.
func IsComplete(s *muSeries) bool {
	return strings.EqualFold(s.Status, "Complete")
}

// RelevantRelations returns relations that are direct continuations or
// related works (sequel, prequel, side story, spin-off). Excludes
// "Alternative Version", "Doujinshi", "Coloured Version", etc.
func RelevantRelations(s *muSeries) []muRelated {
	keep := []string{"sequel", "prequel", "side story", "spin-off", "spinoff", "alternate story"}
	var out []muRelated
	for _, r := range s.RelatedSeries {
		lower := strings.ToLower(r.RelationType)
		for _, k := range keep {
			if strings.Contains(lower, k) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// --- helpers -----------------------------------------------------------------

// officialPublishers is a heuristic list of known English manga/LN publishers.
// A release whose group name contains one of these strings is treated as official.
var officialPublishers = []string{
	"yen press", "seven seas", "kodansha", "viz media", "tokyopop",
	"dark horse", "udon", "vertical", "one peace", "j-novel",
	"cross infinite", "sol press", "airship", "tentai books",
	"square enix manga", "square enix", "manga planet", "denpa",
}

func isOfficialRelease(r muRelease) bool {
	for _, g := range r.Groups {
		lower := strings.ToLower(g.Name)
		for _, pub := range officialPublishers {
			if strings.Contains(lower, pub) {
				return true
			}
		}
	}
	return false
}

// muPreferType converts a SeriesType to the MangaUpdates type string used
// when picking the best search result.
func muPreferType(t SeriesType) string {
	if t == TypeLightNovel {
		return "Novel"
	}
	return "Manga"
}

func parseVolume(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}
