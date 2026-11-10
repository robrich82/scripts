package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ErrPlexNotFound is returned by FindShowByTMDBID when no matching show exists
// in any Plex library section.
var ErrPlexNotFound = errors.New("plex: show not found")

// PlexItemType is the numeric type identifier used in Plex API calls.
type PlexItemType int

const (
	PlexTypeShow    PlexItemType = 2
	PlexTypeSeason  PlexItemType = 3
	PlexTypeEpisode PlexItemType = 4
)

// PlexSeason holds the Plex ratingKey and season number for a season item.
type PlexSeason struct {
	RatingKey    string
	SeasonNumber int
}

// PlexEpisode holds the Plex ratingKey, season number, and episode number.
type PlexEpisodeItem struct {
	RatingKey     string
	SeasonNumber  int
	EpisodeNumber int
}

// PlexClient is the interface for all Plex Media Server API calls needed by
// the rating pipeline.
type PlexClient interface {
	// FindShowByTMDBID searches all "show" library sections for an item whose
	// Guid list contains "tmdb://{tmdbID}". Returns the ratingKey on success,
	// or ErrPlexNotFound if no match exists.
	FindShowByTMDBID(tmdbID string) (string, error)
	// FindShowByTitle searches all "show" library sections for an item whose
	// title (normalised) matches title. Used as a fallback when the show uses a
	// non-TMDB agent (e.g. HAMA for anime). Returns ErrPlexNotFound if absent.
	FindShowByTitle(title string) (string, error)
	// FindShowByFilePath searches all "show" library sections for the show that
	// owns the episode file at filePath. It queries episode metadata filtered by
	// part.file and returns the grandparentRatingKey (show ratingKey) of the
	// matching episode. Used as a last resort when TMDB GUID and title matching
	// both fail (e.g. when the Plex title differs from the filesystem folder
	// name). Returns ErrPlexNotFound if no episode with that path exists.
	FindShowByFilePath(filePath string) (string, error)
	// GetSeasons returns the ratingKey and season number for every season of
	// the show identified by showRatingKey.
	GetSeasons(showRatingKey string) ([]PlexSeason, error)
	// GetEpisodes returns the ratingKey and episode/season numbers for every
	// episode of the season identified by seasonRatingKey.
	GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error)
	// GetContentRating returns the NZ content rating currently stored in Plex
	// for the item identified by ratingKey, stripping the "nz/" prefix before
	// parsing. Returns RatingNone when unset or not a recognised NZ rating.
	GetContentRating(ratingKey string) (NZRating, error)
	// SetContentRating updates the contentRating field on a Plex item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error
	// SetAudioLanguage sets the preferred audio language on the show identified
	// by ratingKey. Languages are BCP-47/ISO 639-1 tags (e.g. "ja", "en", "ko").
	// A single-account client applies langs.Default; multiPlexClient gives each
	// account its own language. Applied at the show level only; no locking so the
	// user can override in Plex.
	SetAudioLanguage(ratingKey string, langs AudioLangs) error
	// RefreshLibraries triggers a metadata scan of all Plex TV show library
	// sections so that newly processed files are picked up without manual
	// intervention.
	RefreshLibraries() error
	// RefreshMetadata triggers a metadata refresh for the item identified by
	// ratingKey, causing Plex to re-read embedded metadata from the files.
	RefreshMetadata(ratingKey string) error
	// SetEpisodeMetadata updates the title, sort title, and/or originally
	// available date on a Plex episode item. Non-empty fields are written and
	// locked; empty fields are left unchanged.
	SetEpisodeMetadata(ratingKey, title, originallyAvailableAt string) error
}

// httpPlexClient is the production PlexClient that communicates with a real
// Plex Media Server over HTTP.
type httpPlexClient struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// showKeyCache caches tmdbID → Plex ratingKey within a single run to avoid
	// repeatedly scanning the full library.
	showKeyCache map[string]string
}

// newPlexHTTPClient drops X-Plex-Token from a redirect that leaves the Plex
// server's host: Go only strips Authorization and Cookie on its own.
func newPlexHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("X-Plex-Token")
			}
			return nil
		},
	}
}

func newHTTPPlexClient(baseURL, token string) *httpPlexClient {
	return &httpPlexClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		httpClient:   newPlexHTTPClient(),
		showKeyCache: make(map[string]string),
	}
}

// plexGet performs a GET request to path (relative to baseURL) with an
// Accept: application/json header. The Plex token goes in a header, not the
// URL, so transport errors (which print the URL) can't leak it.
func (c *httpPlexClient) plexGet(path string) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	// Reject non-2xx so a 401/404/500 surfaces as an error instead of being
	// decoded into a zero-value struct that looks like an empty success.
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("plex: GET %s: unexpected status %s", path, resp.Status)
	}
	return resp, nil
}

// FindShowByTMDBID implements PlexClient.
func (c *httpPlexClient) FindShowByTMDBID(tmdbID string) (string, error) {
	if key, ok := c.showKeyCache[tmdbID]; ok {
		return key, nil
	}

	// 1. Discover all library sections.
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

	// 2. For each "show" section, scan all shows for the TMDB GUID.
	wantGUID := "tmdb://" + tmdbID
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findInSection(section.Key, wantGUID)
		if err != nil {
			return "", err
		}
		if key != "" {
			c.showKeyCache[tmdbID] = key
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

// findInSection scans a single library section for a show with the given GUID.
func (c *httpPlexClient) findInSection(sectionKey, wantGUID string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=2")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	// Guid may be a JSON array (modern Plex TV agent) or a plain string
	// (legacy metadata agents). Use RawMessage so we can try both.
	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string          `json:"ratingKey"`
				Guid      json.RawMessage `json:"Guid"`
				GuidLower string          `json:"guid"` // legacy lowercase plain-string guid
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	// tmdbID is the bare numeric ID extracted from wantGUID ("tmdb://12345" → "12345").
	tmdbID := strings.TrimPrefix(wantGUID, "tmdb://")

	for _, item := range allResp.MediaContainer.Metadata {
		if plexItemHasTMDBID(item.Guid, item.GuidLower, wantGUID, tmdbID) {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// plexItemHasTMDBID reports whether a Plex metadata item matches the given
// TMDB ID. It handles three GUID representations:
//
//  1. Modern (Plex TV agent): Guid is a JSON array, e.g. [{"id":"tmdb://12345"}]
//  2. Legacy Guid string:     Guid is a plain JSON string containing "://tmdbID"
//  3. Legacy lowercase guid:  guid field is a plain string, e.g.
//     "com.plexapp.agents.themoviedb://12345?lang=en"
func plexItemHasTMDBID(guidRaw json.RawMessage, guidLower, wantGUID, tmdbID string) bool {
	// Modern format: array of objects.
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

	// Legacy: Guid is a plain JSON string.
	if len(guidRaw) > 0 {
		var s string
		if json.Unmarshal(guidRaw, &s) == nil && guidStringMatchesTMDBID(s, tmdbID) {
			return true
		}
	}

	// Legacy: lowercase guid field.
	return guidStringMatchesTMDBID(guidLower, tmdbID)
}

// guidStringMatchesTMDBID returns true when s contains "://tmdbID" followed
// by a non-digit (or end of string), preventing partial-ID false positives.
func guidStringMatchesTMDBID(s, tmdbID string) bool {
	target := "://" + tmdbID
	_, rest, ok := strings.Cut(s, target)
	if !ok {
		return false
	}
	return rest == "" || rest[0] < '0' || rest[0] > '9'
}

// GetSeasons implements PlexClient.
func (c *httpPlexClient) GetSeasons(showRatingKey string) ([]PlexSeason, error) {
	resp, err := c.plexGet("/library/metadata/" + showRatingKey + "/children")
	if err != nil {
		return nil, fmt.Errorf("plex: get seasons for %s: %w", showRatingKey, err)
	}
	defer resp.Body.Close()

	var childResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Type      string `json:"type"`
				Index     int    `json:"index"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&childResp); err != nil {
		return nil, fmt.Errorf("plex: decode seasons for %s: %w", showRatingKey, err)
	}

	var seasons []PlexSeason
	for _, m := range childResp.MediaContainer.Metadata {
		if m.Type == "season" {
			seasons = append(seasons, PlexSeason{RatingKey: m.RatingKey, SeasonNumber: m.Index})
		}
	}
	return seasons, nil
}

// GetEpisodes implements PlexClient.
func (c *httpPlexClient) GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error) {
	resp, err := c.plexGet("/library/metadata/" + seasonRatingKey + "/children")
	if err != nil {
		return nil, fmt.Errorf("plex: get episodes for %s: %w", seasonRatingKey, err)
	}
	defer resp.Body.Close()

	var childResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey   string `json:"ratingKey"`
				Type        string `json:"type"`
				ParentIndex int    `json:"parentIndex"`
				Index       int    `json:"index"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&childResp); err != nil {
		return nil, fmt.Errorf("plex: decode episodes for %s: %w", seasonRatingKey, err)
	}

	var episodes []PlexEpisodeItem
	for _, m := range childResp.MediaContainer.Metadata {
		if m.Type == "episode" {
			episodes = append(episodes, PlexEpisodeItem{
				RatingKey:     m.RatingKey,
				SeasonNumber:  m.ParentIndex,
				EpisodeNumber: m.Index,
			})
		}
	}
	return episodes, nil
}

// FindShowByTitle implements PlexClient. It scans all "show" library sections
// for an item whose normalised title matches title, regardless of which
// metadata agent is in use. This is a fallback for when FindShowByTMDBID fails
// because the show uses a non-TMDB agent (e.g. HAMA for anime).
func (c *httpPlexClient) FindShowByTitle(title string) (string, error) {
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

	normTitle := normalizeShowTitle(title)
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findShowInSectionByTitle(section.Key, normTitle)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

func (c *httpPlexClient) findShowInSectionByTitle(sectionKey, normTitle string) (string, error) {
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=2")
	if err != nil {
		return "", fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}

	for _, item := range allResp.MediaContainer.Metadata {
		if normalizeShowTitle(item.Title) == normTitle {
			return item.RatingKey, nil
		}
	}
	return "", nil
}

// FindShowByFilePath implements PlexClient. It queries each show library
// section for episodes (type=4) whose Part file matches filePath exactly,
// then returns the grandparentRatingKey of the first match — the show's Plex
// ratingKey. This is the most reliable fallback for shows whose Plex title
// differs from the filesystem folder name (e.g. anime using HAMA agent).
func (c *httpPlexClient) FindShowByFilePath(filePath string) (string, error) {
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

	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type != "show" {
			continue
		}
		key, err := c.findShowInSectionByFilePath(section.Key, filePath)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	return "", ErrPlexNotFound
}

func (c *httpPlexClient) findShowInSectionByFilePath(sectionKey, filePath string) (string, error) {
	path := "/library/sections/" + sectionKey + "/all?type=4&part.file=" + url.QueryEscape(filePath)
	resp, err := c.plexGet(path)
	if err != nil {
		return "", fmt.Errorf("plex: list section %s by file: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []struct {
				GrandparentRatingKey string `json:"grandparentRatingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return "", fmt.Errorf("plex: decode section %s file results: %w", sectionKey, err)
	}

	if len(allResp.MediaContainer.Metadata) > 0 {
		return allResp.MediaContainer.Metadata[0].GrandparentRatingKey, nil
	}
	return "", nil
}

// normalizeShowTitle returns a lowercase alphanumeric-only string for fuzzy
// title matching, consistent with normalizePlexTitle in mkv-movie.
func normalizeShowTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// GetContentRating implements PlexClient.
func (c *httpPlexClient) GetContentRating(ratingKey string) (NZRating, error) {
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

// RefreshLibraries implements PlexClient. It scans all "show" library
// sections, triggering a metadata refresh so Plex picks up any new or changed
// files without requiring a manual library scan.
func (c *httpPlexClient) RefreshLibraries() error {
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
		if section.Type != "show" {
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

// RefreshMetadata implements PlexClient. It sends a GET to the refresh
// endpoint for a specific library item, causing Plex to re-read embedded
// metadata from the files on disk.
func (c *httpPlexClient) RefreshMetadata(ratingKey string) error {
	resp, err := c.plexGet("/library/metadata/" + ratingKey + "/refresh")
	if err != nil {
		return fmt.Errorf("plex: refresh metadata %s: %w", ratingKey, err)
	}
	resp.Body.Close()
	return nil
}

// SetEpisodeMetadata implements PlexClient.
func (c *httpPlexClient) SetEpisodeMetadata(ratingKey, title, originallyAvailableAt string) error {
	if title == "" && originallyAvailableAt == "" {
		return nil
	}
	path := fmt.Sprintf("/library/metadata/%s?type=%d&id=%s",
		ratingKey, PlexTypeEpisode, ratingKey)
	if title != "" {
		path += "&title.value=" + url.QueryEscape(title) + "&title.locked=1"
		path += "&titleSort.value=" + url.QueryEscape(title) + "&titleSort.locked=1"
	}
	if originallyAvailableAt != "" {
		path += "&originallyAvailableAt.value=" + url.QueryEscape(originallyAvailableAt) + "&originallyAvailableAt.locked=1"
	}

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: set episode metadata: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex: set episode metadata got status %d", resp.StatusCode)
	}
	return nil
}

// plexAudioBCP47 maps ISO 639-1 language codes to the region-qualified BCP-47
// tags Plex's per-item audioLanguage preference expects (e.g. "ja" -> "ja-JP").
// Plex Web sends these region-qualified tags; a bare code (e.g. "ja") is stored
// but not shown as a selected option in the Settings dialog, so it renders as
// "Account default" even though it applies. Values from Plex Web's enum.
var plexAudioBCP47 = map[string]string{
	"ar": "ar-SA", "bg": "bg-BG", "ca": "ca-ES", "zh": "zh-CN",
	"hr": "hr-HR", "cs": "cs-CZ", "da": "da-DK", "nl": "nl-NL",
	"en": "en-US", "et": "et-EE", "fi": "fi-FI", "fr": "fr-FR",
	"de": "de-DE", "el": "el-GR", "he": "he-IL", "hi": "hi-IN",
	"hu": "hu-HU", "id": "id-ID", "it": "it-IT", "ja": "ja-JP",
	"ko": "ko-KR", "lv": "lv-LV", "lt": "lt-LT", "nb": "nb-NO",
	"fa": "fa-IR", "pl": "pl-PL", "pt": "pt-BR", "ro": "ro-RO",
	"ru": "ru-RU", "sk": "sk-SK", "es": "es-ES", "sv": "sv-SE",
	"th": "th-TH", "tr": "tr-TR", "uk": "uk-UA", "vi": "vi-VN",
}

// plexAudioLangTag returns the region-qualified BCP-47 tag Plex expects for an
// audio language code. Values already carrying a region (containing "-") pass
// through unchanged, and unmapped codes fall back to the input.
func plexAudioLangTag(code string) string {
	if code == "" || strings.Contains(code, "-") {
		return code
	}
	if tag, ok := plexAudioBCP47[strings.ToLower(code)]; ok {
		return tag
	}
	return code
}

// SetAudioLanguage implements PlexClient. It uses the per-item prefs endpoint
// so the preference is stored per-user for this show only. This client speaks
// for a single account, so it applies langs.Default and ignores per-alt
// overrides. The code is sent as a region-qualified BCP-47 tag (see
// plexAudioLangTag) to match Plex Web, so the value shows correctly in the
// per-item Settings dialog rather than as "Account default".
func (c *httpPlexClient) SetAudioLanguage(ratingKey string, langs AudioLangs) error {
	path := fmt.Sprintf("/library/metadata/%s/prefs?audioLanguage=%s",
		ratingKey, url.QueryEscape(plexAudioLangTag(langs.Default)))

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: set audio language: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return ErrPlexNotFound
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("plex: set audio language got status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// multiAccountReporter is implemented by multiPlexClient, the client that fans
// SetAudioLanguage out to more than one Plex account. It lets the caller record
// what was applied per account and report any per-account failures.
type multiAccountReporter interface {
	appliedSignature(langs AudioLangs) string
	altErrSuffix() string
}

// appliedLangSignature returns the value to cache as "already applied" for a
// title: the region-qualified tag actually sent to the primary account, plus one
// entry per configured alt account, e.g. "ja-JP|alt=ja-JP|alt2=en-US". Alt
// accounts are listed even when they inherit the default, so adding
// PLEX_ALT2_TOKEN invalidates the cache and the new account is written on the
// next run. Recording the region-qualified tag (not the bare code) also
// invalidates caches written before that change, so the corrected value is
// re-applied once.
func appliedLangSignature(client PlexClient, langs AudioLangs) string {
	if r, ok := client.(multiAccountReporter); ok {
		return r.appliedSignature(langs)
	}
	return plexAudioLangTag(langs.Default)
}

// Labels for the additional Plex accounts. Each is both the manual_data.json
// key prefix ("alt-language") and the name used when reporting errors.
const (
	altAccount  = "alt"
	alt2Account = "alt2"
)

// AudioLangs holds the preferred audio language for each Plex account. Default
// applies to the primary account and to any alt account with no override of its
// own; Alts is keyed by account label (altAccount, alt2Account).
type AudioLangs struct {
	Default string
	Alts    map[string]string
}

// forAccount returns the language to apply for the named alt account, falling
// back to Default when that account has no override.
func (a AudioLangs) forAccount(name string) string {
	if lang := a.Alts[name]; lang != "" {
		return lang
	}
	return a.Default
}

// appliedSignature implements multiAccountReporter. Every configured alt
// account is listed with the region-qualified tag it actually receives — the
// mapped form of Default, unless it has an "alt-language" / "alt2-language"
// override of its own.
func (m *multiPlexClient) appliedSignature(langs AudioLangs) string {
	var b strings.Builder
	b.WriteString(plexAudioLangTag(langs.Default))
	for _, a := range m.alts {
		b.WriteString("|")
		b.WriteString(a.name)
		b.WriteString("=")
		b.WriteString(plexAudioLangTag(langs.forAccount(a.name)))
	}
	return b.String()
}

// namedPlexClient pairs an additional Plex account's client with its label.
type namedPlexClient struct {
	name   string
	client PlexClient
}

// altPlexTokenEnv lists the optional additional Plex account tokens, in the
// order their labels are reported.
var altPlexTokenEnv = []struct{ name, env string }{
	{altAccount, "PLEX_ALT_TOKEN"},
	{alt2Account, "PLEX_ALT2_TOKEN"},
}

// altPlexClients builds a client for each additional Plex account token present
// in the environment. Returns nil when none are set.
func altPlexClients(plexURL string) []namedPlexClient {
	var alts []namedPlexClient
	for _, a := range altPlexTokenEnv {
		if token := os.Getenv(a.env); token != "" {
			alts = append(alts, namedPlexClient{name: a.name, client: newHTTPPlexClient(plexURL, token)})
		}
	}
	return alts
}

// multiPlexClient dispatches SetAudioLanguage to the primary and every alt
// account, routing all other calls to primary only. Used when PLEX_ALT_TOKEN
// and/or PLEX_ALT2_TOKEN are set so the audio language preference is applied
// for every Plex user.
type multiPlexClient struct {
	primary PlexClient
	alts    []namedPlexClient
	altErrs []error // parallel to alts; nil entry means that account succeeded
}

// altErrSuffix formats the errors from the most recent SetAudioLanguage call
// for display, e.g. "; alt: not in library". Returns "" when every alt account
// succeeded.
func (m *multiPlexClient) altErrSuffix() string {
	var parts []string
	for i, err := range m.altErrs {
		if err != nil {
			parts = append(parts, m.alts[i].name+": "+plexAltErrSummary(err))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

// plexAltErrSummary converts a Plex alt-token error to a short human-readable
// string, collapsing noisy HTTP 404 responses to "not in library".
func plexAltErrSummary(err error) string {
	if strings.Contains(err.Error(), "status 404") {
		return "not in library"
	}
	return err.Error()
}

// newMultiPlexClient returns a PlexClient that calls SetAudioLanguage on the
// primary and every alt. If there are no alts, primary is returned directly.
func newMultiPlexClient(primary PlexClient, alts ...namedPlexClient) PlexClient {
	if len(alts) == 0 {
		return primary
	}
	return &multiPlexClient{primary: primary, alts: alts}
}

func (m *multiPlexClient) FindShowByTMDBID(tmdbID string) (string, error) {
	return m.primary.FindShowByTMDBID(tmdbID)
}
func (m *multiPlexClient) FindShowByTitle(title string) (string, error) {
	return m.primary.FindShowByTitle(title)
}
func (m *multiPlexClient) FindShowByFilePath(filePath string) (string, error) {
	return m.primary.FindShowByFilePath(filePath)
}
func (m *multiPlexClient) GetSeasons(showRatingKey string) ([]PlexSeason, error) {
	return m.primary.GetSeasons(showRatingKey)
}
func (m *multiPlexClient) GetEpisodes(seasonRatingKey string) ([]PlexEpisodeItem, error) {
	return m.primary.GetEpisodes(seasonRatingKey)
}
func (m *multiPlexClient) GetContentRating(ratingKey string) (NZRating, error) {
	return m.primary.GetContentRating(ratingKey)
}
func (m *multiPlexClient) SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error {
	return m.primary.SetContentRating(ratingKey, itemType, rating)
}
func (m *multiPlexClient) SetAudioLanguage(ratingKey string, langs AudioLangs) error {
	m.altErrs = make([]error, len(m.alts))
	if err := m.primary.SetAudioLanguage(ratingKey, langs); err != nil {
		return err
	}
	for i, a := range m.alts {
		m.altErrs[i] = a.client.SetAudioLanguage(ratingKey, AudioLangs{Default: langs.forAccount(a.name)})
	}
	return nil
}
func (m *multiPlexClient) RefreshLibraries() error { return m.primary.RefreshLibraries() }
func (m *multiPlexClient) RefreshMetadata(ratingKey string) error {
	return m.primary.RefreshMetadata(ratingKey)
}
func (m *multiPlexClient) SetEpisodeMetadata(ratingKey, title, originallyAvailableAt string) error {
	return m.primary.SetEpisodeMetadata(ratingKey, title, originallyAvailableAt)
}

// SetContentRating implements PlexClient. It sends a PUT to the metadata
// endpoint setting contentRating and locking the field.
func (c *httpPlexClient) SetContentRating(ratingKey string, itemType PlexItemType, rating NZRating) error {
	// Plex identifies content rating systems by a country prefix (e.g. "nz/M",
	// "nz/R16"). Without the prefix the value is stored as a raw string and
	// won't be recognised as an NZ classification rating.
	plexRating := "nz/" + string(rating)
	path := fmt.Sprintf("/library/metadata/%s?type=%d&id=%s&contentRating.value=%s&contentRating.locked=1",
		ratingKey, itemType, ratingKey, url.QueryEscape(plexRating))

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)

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
