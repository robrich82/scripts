package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrPlexMovieNotFound is returned by FindMovieByTMDBID when no matching movie
// exists in any Plex library section.
var ErrPlexMovieNotFound = errors.New("plex: movie not found")

// flexBool unmarshals JSON booleans (true/false) or integers (0/1) into a bool.
// Plex API versions differ: some return selected/forced as int, others as bool.
type flexBool bool

func (f *flexBool) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*f = flexBool(b)
		return nil
	}
	var i int
	if err := json.Unmarshal(data, &i); err == nil {
		*f = flexBool(i != 0)
		return nil
	}
	return fmt.Errorf("flexBool: cannot unmarshal %s", data)
}

// PlexMovieClient is the interface for Plex API calls needed by the movie
// rating pipeline.
type PlexMovieClient interface {
	// FindMovieByTMDBID searches all "movie" library sections for an item whose
	// Guid contains "tmdb://{tmdbID}". Returns the ratingKey on success, or
	// ErrPlexMovieNotFound if no match exists.
	FindMovieByTMDBID(tmdbID string) (string, error)
	// FindMovieByTitle searches all "movie" library sections for an item whose
	// title (normalized) matches title and whose year matches year.
	// Returns ErrPlexMovieNotFound if no match exists.
	FindMovieByTitle(title, year string) (string, error)
	// GetContentRating returns the current NZ content rating stored in Plex for
	// the item identified by ratingKey, or RatingNone if unset or unrecognised.
	GetContentRating(ratingKey string) (NZRating, error)
	// SetContentRating updates the contentRating field on a Plex movie item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, rating NZRating) error
	// SetMovieStreams selects the audio stream matching audioLangTag (ISO 639-1,
	// e.g. "ja") and the first non-forced English subtitle stream on the movie's
	// first media Part. Returns whether audio and/or subtitle were changed.
	SetMovieStreams(ratingKey, audioLangTag string) (audioSet, subtitleSet bool, err error)
	// RefreshLibraries triggers a metadata scan of all Plex movie library
	// sections so that newly processed files are picked up without manual
	// intervention.
	RefreshLibraries() error
}

// httpPlexMovieClient is the production PlexMovieClient that communicates with
// a real Plex Media Server over HTTP.
type httpPlexMovieClient struct {
	baseURL       string
	token         string
	httpClient    *http.Client
	movieKeyCache map[string]string // tmdbID → Plex ratingKey
}

func newHTTPPlexMovieClient(baseURL, token string) *httpPlexMovieClient {
	return &httpPlexMovieClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         token,
		httpClient:    &http.Client{},
		movieKeyCache: make(map[string]string),
	}
}

func (c *httpPlexMovieClient) plexGet(path string) (*http.Response, error) {
	u := c.baseURL + path
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	u += sep + "X-Plex-Token=" + url.QueryEscape(c.token)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return c.httpClient.Do(req)
}

// FindMovieByTMDBID implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTMDBID(tmdbID string) (string, error) {
	if key, ok := c.movieKeyCache[tmdbID]; ok {
		return key, nil
	}

	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return "", fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return "", fmt.Errorf("plex: decode sections: %w", err)
	}

	wantGUID := "tmdb://" + tmdbID
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "movie" {
			continue
		}
		key, err := c.findMovieInSection(section.Key, wantGUID, tmdbID)
		if err != nil {
			return "", err
		}
		if key != "" {
			c.movieKeyCache[tmdbID] = key
			return key, nil
		}
	}
	return "", ErrPlexMovieNotFound
}

func (c *httpPlexMovieClient) findMovieInSection(sectionKey, wantGUID, tmdbID string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=1&includeGuids=1")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string          `json:"ratingKey"`
				Guid      json.RawMessage `json:"Guid"`
				GuidLower string          `json:"guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if plexMovieItemHasTMDBID(item.Guid, item.GuidLower, wantGUID, tmdbID) {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// plexMovieItemHasTMDBID mirrors mkv-tvshow's plexItemHasTMDBID.
func plexMovieItemHasTMDBID(guidRaw json.RawMessage, guidLower, wantGUID, tmdbID string) bool {
	if len(guidRaw) > 0 && guidRaw[0] == '[' {
		var guids []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(guidRaw, &guids) == nil {
			for _, g := range guids {
				if g.ID == wantGUID {
					return true
				}
			}
		}
		return false
	}
	if len(guidRaw) > 0 {
		var s string
		if json.Unmarshal(guidRaw, &s) == nil && plexMovieGuidMatchesTMDBID(s, tmdbID) {
			return true
		}
	}
	return plexMovieGuidMatchesTMDBID(guidLower, tmdbID)
}

func plexMovieGuidMatchesTMDBID(s, tmdbID string) bool {
	_, rest, ok := strings.Cut(s, "://"+tmdbID)
	return ok && (rest == "" || rest[0] < '0' || rest[0] > '9')
}

// FindMovieByTitle implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTitle(title, year string) (string, error) {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return "", fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return "", fmt.Errorf("plex: decode sections: %w", err)
	}

	normTitle := normalizePlexTitle(title)
	wantYear := 0
	if year != "" {
		wantYear, _ = strconv.Atoi(year)
	}
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "movie" {
			continue
		}
		key, err := c.findMovieInSectionByTitle(section.Key, normTitle, wantYear)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexMovieNotFound
}

func (c *httpPlexMovieClient) findMovieInSectionByTitle(sectionKey, normTitle string, wantYear int) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=1")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
				Year      int    `json:"year"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if normalizePlexTitle(item.Title) != normTitle {
			continue
		}
		if wantYear != 0 && abs(item.Year-wantYear) > 1 {
			continue
		}
		return item.RatingKey, nil
	}
	return "", nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// normalizePlexTitle returns a lowercase alphanumeric-only string for fuzzy title matching.
func normalizePlexTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// GetContentRating implements PlexMovieClient.
// It returns the NZ content rating currently stored in Plex for ratingKey,
// stripping the "nz/" prefix before parsing. Returns RatingNone when unset
// or not a recognised NZ rating.
func (c *httpPlexMovieClient) GetContentRating(ratingKey string) (NZRating, error) {
	resp, err := c.plexGet("/library/metadata/" + ratingKey)
	if err != nil {
		return RatingNone, fmt.Errorf("plex: get metadata %s: %w", ratingKey, err)
	}
	defer resp.Body.Close()

	var metaResp struct {
		MediaContainer struct {
			Metadata []struct {
				ContentRating string `json:"contentRating"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&metaResp); err != nil {
		return RatingNone, fmt.Errorf("plex: decode metadata %s: %w", ratingKey, err)
	}
	if len(metaResp.MediaContainer.Metadata) == 0 {
		return RatingNone, nil
	}
	raw := strings.TrimPrefix(metaResp.MediaContainer.Metadata[0].ContentRating, "nz/")
	rating, _ := ParseNZRating(raw)
	return rating, nil
}

// RefreshLibraries implements PlexMovieClient. It scans all "movie" library
// sections, triggering a metadata refresh so Plex picks up any new or changed
// files without requiring a manual library scan.
func (c *httpPlexMovieClient) RefreshLibraries() error {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return fmt.Errorf("plex: decode sections: %w", err)
	}

	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "movie" {
			continue
		}
		refreshResp, err := c.plexGet("/library/sections/" + section.Key + "/refresh")
		if err != nil {
			return fmt.Errorf("plex: refresh section %s: %w", section.Key, err)
		}
		refreshResp.Body.Close()
	}
	return nil
}

// SetMovieStreams implements PlexMovieClient. It fetches the movie's first
// media Part, selects the audio stream matching audioLangTag (e.g. "ja"),
// and selects the first non-forced English subtitle stream. Only calls the
// Plex API when the desired stream is not already selected.
func (c *httpPlexMovieClient) SetMovieStreams(ratingKey, audioLangTag string) (audioSet, subtitleSet bool, err error) {
	resp, err := c.plexGet("/library/metadata/" + ratingKey)
	if err != nil {
		return false, false, fmt.Errorf("plex: get streams %s: %w", ratingKey, err)
	}
	defer resp.Body.Close()

	var meta struct {
		MediaContainer struct {
			Metadata []struct {
				Media []struct {
					Part []struct {
						ID     int `json:"id"`
						Stream []struct {
							ID           int      `json:"id"`
							StreamType   int      `json:"streamType"`
							Selected     flexBool `json:"selected"`
							Forced       flexBool `json:"forced"`
							LanguageTag  string   `json:"languageTag"`
							LanguageCode string   `json:"languageCode"`
						} `json:"Stream"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, false, fmt.Errorf("plex: decode streams %s: %w", ratingKey, err)
	}
	if len(meta.MediaContainer.Metadata) == 0 ||
		len(meta.MediaContainer.Metadata[0].Media) == 0 ||
		len(meta.MediaContainer.Metadata[0].Media[0].Part) == 0 {
		return false, false, fmt.Errorf("plex: no media part for %s", ratingKey)
	}
	part := meta.MediaContainer.Metadata[0].Media[0].Part[0]

	// Select audio stream matching audioLangTag (skip if already selected).
	var audioID int
	audioOK := false
	for _, s := range part.Stream {
		if s.StreamType != 2 || !streamLangMatches(s.LanguageTag, s.LanguageCode, audioLangTag) {
			continue
		}
		if bool(s.Selected) {
			audioOK = true
			break
		}
		if audioID == 0 {
			audioID = s.ID
		}
	}
	if !audioOK {
		if audioID == 0 {
			return false, false, fmt.Errorf("plex: no %s audio stream in %s", audioLangTag, ratingKey)
		}
		if err := c.plexSetStream(part.ID, "audioStreamID", audioID); err != nil {
			return false, false, fmt.Errorf("plex: set audio stream: %w", err)
		}
		audioSet = true
	}

	// Select first non-forced English subtitle (skip if already selected).
	var subtitleID int
	subtitleOK := false
	for _, s := range part.Stream {
		if s.StreamType != 3 || !streamLangMatches(s.LanguageTag, s.LanguageCode, "en") || bool(s.Forced) {
			continue
		}
		if bool(s.Selected) {
			subtitleOK = true
			break
		}
		if subtitleID == 0 {
			subtitleID = s.ID
		}
	}
	if !subtitleOK && subtitleID != 0 {
		if err := c.plexSetStream(part.ID, "subtitleStreamID", subtitleID); err != nil {
			return audioSet, false, fmt.Errorf("plex: set subtitle stream: %w", err)
		}
		subtitleSet = true
	}

	return audioSet, subtitleSet, nil
}

// iso6392ToTag maps ISO 639-2 three-letter codes to ISO 639-1 two-letter BCP 47 tags.
var iso6392ToTag = map[string]string{
	"eng": "en", "jpn": "ja", "kor": "ko",
	"zho": "zh", "chi": "zh",
	"fre": "fr", "fra": "fr",
	"ger": "de", "deu": "de",
	"spa": "es", "ita": "it", "por": "pt",
	"rus": "ru", "ara": "ar",
}

// streamLangMatches reports whether a Plex stream matches the given ISO 639-1
// tag. It checks languageTag (BCP 47, possibly with a region suffix like "ja-JP")
// first, then falls back to languageCode (ISO 639-2) via iso6392ToTag.
func streamLangMatches(languageTag, languageCode, want string) bool {
	if languageTag != "" {
		return languageTag == want || strings.HasPrefix(languageTag, want+"-")
	}
	return iso6392ToTag[languageCode] == want
}

func (c *httpPlexMovieClient) plexSetStream(partID int, param string, streamID int) error {
	u := fmt.Sprintf("%s/library/parts/%d?%s=%d&X-Plex-Token=%s",
		c.baseURL, partID, param, streamID, url.QueryEscape(c.token))
	req, err := http.NewRequest(http.MethodPut, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// altMovieErrReporter is implemented by multiPlexMovieClient. After a
// successful SetMovieStreams call, callers can retrieve any error from the alt token.
type altMovieErrReporter interface {
	getAltErr() error
}

// multiPlexMovieClient dispatches SetMovieStreams to both primary and alt,
// routing all other calls to primary only. Used when PLEX_ALT_TOKEN is set so
// that stream selection is applied for both Plex users.
type multiPlexMovieClient struct {
	primary PlexMovieClient
	alt     PlexMovieClient
	altErr  error
}

func (m *multiPlexMovieClient) getAltErr() error { return m.altErr }

// plexMovieAltErrSummary converts a Plex alt-token error to a short human-readable
// string, collapsing noisy HTTP 404 responses to "not in library".
func plexMovieAltErrSummary(err error) string {
	if strings.Contains(err.Error(), "status 404") {
		return "not in library"
	}
	return err.Error()
}

// newMultiPlexMovieClient returns a PlexMovieClient that calls SetMovieStreams
// on both primary and alt. If alt is nil, primary is returned directly.
func newMultiPlexMovieClient(primary, alt PlexMovieClient) PlexMovieClient {
	if alt == nil {
		return primary
	}
	return &multiPlexMovieClient{primary: primary, alt: alt}
}

func (m *multiPlexMovieClient) FindMovieByTMDBID(tmdbID string) (string, error) {
	return m.primary.FindMovieByTMDBID(tmdbID)
}
func (m *multiPlexMovieClient) FindMovieByTitle(title, year string) (string, error) {
	return m.primary.FindMovieByTitle(title, year)
}
func (m *multiPlexMovieClient) GetContentRating(ratingKey string) (NZRating, error) {
	return m.primary.GetContentRating(ratingKey)
}
func (m *multiPlexMovieClient) SetContentRating(ratingKey string, rating NZRating) error {
	return m.primary.SetContentRating(ratingKey, rating)
}
func (m *multiPlexMovieClient) SetMovieStreams(ratingKey, audioLangTag string) (audioSet, subtitleSet bool, err error) {
	m.altErr = nil
	audioSet, subtitleSet, err = m.primary.SetMovieStreams(ratingKey, audioLangTag)
	if err != nil {
		return
	}
	_, _, m.altErr = m.alt.SetMovieStreams(ratingKey, audioLangTag)
	return
}
func (m *multiPlexMovieClient) RefreshLibraries() error { return m.primary.RefreshLibraries() }

// SetContentRating implements PlexMovieClient.
func (c *httpPlexMovieClient) SetContentRating(ratingKey string, rating NZRating) error {
	plexRating := "nz/" + string(rating)
	path := fmt.Sprintf("/library/metadata/%s?type=1&id=%s&contentRating.value=%s&contentRating.locked=1&X-Plex-Token=%s",
		ratingKey, ratingKey, url.QueryEscape(plexRating), url.QueryEscape(c.token))

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: set content rating: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex: set content rating got status %d", resp.StatusCode)
	}
	return nil
}
