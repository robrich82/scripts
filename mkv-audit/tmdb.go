package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const tmdbOriginBase = "https://api.themoviedb.org/3"

// OriginInfo holds the original language and primary origin country for a
// piece of content as reported by TMDB.
type OriginInfo struct {
	OrigLang      string // ISO 639-1 code, e.g. "en", "ja"
	OriginCountry string // ISO 3166-1 alpha-2 code, e.g. "US", "GB"
}

// IsZero reports whether the origin info is empty/unset.
func (o OriginInfo) IsZero() bool { return o.OrigLang == "" }

// OrigLangISO returns the ISO 639-2/B code for the original language (e.g. "eng").
func (o OriginInfo) OrigLangISO() string { return normalizeLanguage(o.OrigLang) }

// IETFTag returns the IETF BCP-47 tag derived from OrigLang + OriginCountry,
// e.g. "en-CA". Returns "" when the info is empty or OriginCountry is unset.
func (o OriginInfo) IETFTag() string {
	if o.OrigLang == "" || o.OriginCountry == "" {
		return ""
	}
	return o.OrigLang + "-" + o.OriginCountry
}

// TVSearchCandidate is a single result from a TMDB TV search.
type TVSearchCandidate struct {
	ID   string
	Name string
	Year string
}

// MovieSearchCandidate is a single result from a TMDB movie search.
type MovieSearchCandidate struct {
	ID    string
	Title string
	Year  string
}

// TMDBOriginClient fetches content-origin metadata from TMDB.
type TMDBOriginClient interface {
	// GetTVOriginByID fetches origin info directly via a known TMDB show ID,
	// skipping the search step.
	GetTVOriginByID(id string) (OriginInfo, bool, error)
	// GetMovieOriginByID fetches origin info directly via a known TMDB movie ID.
	GetMovieOriginByID(id string) (OriginInfo, bool, error)
	// SearchTV returns candidate TV shows matching title+year, ordered by relevance.
	SearchTV(title, year string) ([]TVSearchCandidate, error)
	// SearchMovie returns candidate movies matching title+year, ordered by relevance.
	SearchMovie(title, year string) ([]MovieSearchCandidate, error)
}

// httpTMDBOriginClient is the production TMDBOriginClient.
type httpTMDBOriginClient struct {
	apiKey string
	client *http.Client
}

func newHTTPTMDBOriginClient(apiKey string) *httpTMDBOriginClient {
	return &httpTMDBOriginClient{apiKey: apiKey, client: &http.Client{Timeout: 30 * time.Second}}
}

// getTMDB performs a GET and rejects non-2xx responses. TMDB returns a JSON
// error body (e.g. invalid api_key, rate-limit) that would otherwise decode
// into an empty struct and be indistinguishable from "no result found".
func (c *httpTMDBOriginClient) getTMDB(u string) (*http.Response, error) {
	resp, err := c.client.Get(u)
	if err != nil {
		return nil, redactAPIKey(err)
	}
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		return nil, &tmdbStatusError{code: resp.StatusCode, status: resp.Status, body: strings.TrimSpace(string(snippet))}
	}
	return resp, nil
}

// tmdbStatusError is a non-2xx TMDB response.
type tmdbStatusError struct {
	code         int
	status, body string
}

func (e *tmdbStatusError) Error() string {
	return fmt.Sprintf("TMDB request failed: status %s: %s", e.status, e.body)
}

// isTMDBNotFound reports whether err is TMDB's 404 for an ID it doesn't have.
func isTMDBNotFound(err error) bool {
	var se *tmdbStatusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

// GetTVOriginByID fetches origin data from /tv/{id} using a known TMDB show ID.
// An ID TMDB doesn't have (404) is not found rather than an error.
func (c *httpTMDBOriginClient) GetTVOriginByID(id string) (OriginInfo, bool, error) {
	detURL := fmt.Sprintf("%s/tv/%s?api_key=%s", tmdbOriginBase, id, url.QueryEscape(c.apiKey))
	resp, err := c.getTMDB(detURL)
	if isTMDBNotFound(err) {
		return OriginInfo{}, false, nil
	}
	if err != nil {
		return OriginInfo{}, false, err
	}
	defer resp.Body.Close()

	var det struct {
		OriginalLanguage string   `json:"original_language"`
		OriginCountry    []string `json:"origin_country"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&det); err != nil {
		return OriginInfo{}, false, fmt.Errorf("decode TV details: %w", err)
	}
	if det.OriginalLanguage == "" {
		return OriginInfo{}, false, nil
	}
	country := ""
	if len(det.OriginCountry) > 0 {
		country = det.OriginCountry[0]
	}
	return OriginInfo{OrigLang: det.OriginalLanguage, OriginCountry: country}, true, nil
}

// GetMovieOriginByID fetches origin data from /movie/{id} using a known TMDB
// movie ID. An ID TMDB doesn't have (404) is not found rather than an error.
func (c *httpTMDBOriginClient) GetMovieOriginByID(id string) (OriginInfo, bool, error) {
	detURL := fmt.Sprintf("%s/movie/%s?api_key=%s", tmdbOriginBase, id, url.QueryEscape(c.apiKey))
	detResp, err := c.getTMDB(detURL)
	if isTMDBNotFound(err) {
		return OriginInfo{}, false, nil
	}
	if err != nil {
		return OriginInfo{}, false, err
	}
	defer detResp.Body.Close()

	var det struct {
		OriginalLanguage    string `json:"original_language"`
		ProductionCountries []struct {
			ISO31661 string `json:"iso_3166_1"`
		} `json:"production_countries"`
	}
	if err := json.NewDecoder(detResp.Body).Decode(&det); err != nil {
		return OriginInfo{}, false, fmt.Errorf("decode movie details: %w", err)
	}
	if det.OriginalLanguage == "" {
		return OriginInfo{}, false, nil
	}
	country := ""
	if len(det.ProductionCountries) > 0 {
		country = det.ProductionCountries[0].ISO31661
	}
	return OriginInfo{OrigLang: det.OriginalLanguage, OriginCountry: country}, true, nil
}

// SearchTV returns TMDB TV show candidates for the given title and optional year.
func (c *httpTMDBOriginClient) SearchTV(title, year string) ([]TVSearchCandidate, error) {
	params := url.Values{"api_key": {c.apiKey}, "query": {title}, "include_adult": {"true"}}
	if year != "" {
		params.Set("first_air_date_year", year)
	}
	resp, err := c.getTMDB(tmdbOriginBase + "/search/tv?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Results []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			FirstAirDate string `json:"first_air_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode TV search: %w", err)
	}
	out := make([]TVSearchCandidate, 0, len(result.Results))
	for _, r := range result.Results {
		y := ""
		if len(r.FirstAirDate) >= 4 {
			y = r.FirstAirDate[:4]
		}
		out = append(out, TVSearchCandidate{ID: fmt.Sprintf("%d", r.ID), Name: r.Name, Year: y})
	}
	return out, nil
}

// SearchMovie returns TMDB movie candidates for the given title and optional year.
func (c *httpTMDBOriginClient) SearchMovie(title, year string) ([]MovieSearchCandidate, error) {
	params := url.Values{"api_key": {c.apiKey}, "query": {title}, "include_adult": {"true"}}
	if year != "" {
		params.Set("year", year)
	}
	resp, err := c.getTMDB(tmdbOriginBase + "/search/movie?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Results []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode movie search: %w", err)
	}
	out := make([]MovieSearchCandidate, 0, len(result.Results))
	for _, r := range result.Results {
		y := ""
		if len(r.ReleaseDate) >= 4 {
			y = r.ReleaseDate[:4]
		}
		out = append(out, MovieSearchCandidate{ID: fmt.Sprintf("%d", r.ID), Title: r.Title, Year: y})
	}
	return out, nil
}

// dirYearRe matches a " (YYYY)" suffix at the end of a directory name.
var dirYearRe = regexp.MustCompile(`\s+\((\d{4})\)\s*$`)

// fsCharReplacements maps filesystem-safe substitute characters (used by
// mkv-tvshow / mkv-movie when naming files on Windows) back to their real
// equivalents for use in TMDB search queries.
var fsCharReplacements = []struct{ from, to string }{
	{"꞉", ":"}, // U+A789 modifier letter colon → colon
	{"？", "?"}, // U+FF1F fullwidth question mark → question mark
	{"╱", "/"}, // U+2571 box drawings light diagonal → slash
	{"│", "|"}, // U+2502 box drawings light vertical → pipe
	{"＊", "*"}, // U+FF0A fullwidth asterisk → asterisk
	{"＜", "<"}, // U+FF1C fullwidth less-than → less-than
	{"＞", ">"}, // U+FF1E fullwidth greater-than → greater-than
	{"＂", `"`}, // two single quotes → double quote
}

// normalizeTitleChars converts filesystem-safe substitute characters in a
// directory or filename back to the real punctuation expected by TMDB.
func normalizeTitleChars(s string) string {
	for _, r := range fsCharReplacements {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	return s
}

// isFileExtension returns true when s looks like a bare file extension:
// 2–5 ASCII letters, no spaces or digits.
func isFileExtension(s string) bool {
	if len(s) < 2 || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// dirTitleYear parses a content-root directory name (e.g. "Show Name (2023)")
// into a search title and year. Strips any file extension first so flat movie
// filenames work too. Filesystem-safe substitute characters (e.g. ꞉ for :)
// are converted back to their real equivalents before returning.
func dirTitleYear(dirName string) (title, year string) {
	// Strip file extension if present (flat movie libraries use filenames as roots).
	// Only strip if the suffix looks like a real extension: no spaces, 2-5 alpha chars.
	if idx := strings.LastIndex(dirName, "."); idx > 0 {
		ext := dirName[idx+1:]
		if isFileExtension(ext) {
			dirName = dirName[:idx]
		}
	}
	dirName = normalizeTitleChars(dirName)
	m := dirYearRe.FindStringSubmatchIndex(dirName)
	if m == nil {
		return strings.TrimSpace(dirName), ""
	}
	year = dirName[m[2]:m[3]]
	title = strings.TrimSpace(dirName[:m[0]])
	return
}

// apiKeyParam matches the api_key value in a request URL. A regexp rather than
// url.Parse, which fails or misplaces the query when an ID holds % ? or #.
var apiKeyParam = regexp.MustCompile(`(?i)(api_key=)[^&#\s"]*`)

// redactAPIKey replaces the api_key value in a *url.Error with REDACTED, since
// its message otherwise prints the whole request URL, key included.
func redactAPIKey(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = apiKeyParam.ReplaceAllString(ue.URL, "${1}REDACTED")
	}
	return err
}
