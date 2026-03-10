package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
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

// TMDBOriginClient fetches content-origin metadata from TMDB.
type TMDBOriginClient interface {
	// GetTVOrigin searches for a TV show by title and optional year and
	// returns the TMDB-reported original language and first origin country.
	// Returns (OriginInfo{}, false, nil) when no result is found.
	GetTVOrigin(title, year string) (OriginInfo, bool, error)
	// GetMovieOrigin does the same for movies.
	GetMovieOrigin(title, year string) (OriginInfo, bool, error)
}

// httpTMDBOriginClient is the production TMDBOriginClient.
type httpTMDBOriginClient struct {
	apiKey string
	client *http.Client
}

func newHTTPTMDBOriginClient(apiKey string) *httpTMDBOriginClient {
	return &httpTMDBOriginClient{apiKey: apiKey, client: &http.Client{}}
}

// GetTVOrigin searches /search/tv which returns both original_language and
// origin_country in each result, so only one API call is needed.
func (c *httpTMDBOriginClient) GetTVOrigin(title, year string) (OriginInfo, bool, error) {
	params := url.Values{"api_key": {c.apiKey}, "query": {title}}
	if year != "" {
		params.Set("first_air_date_year", year)
	}
	resp, err := c.client.Get(tmdbOriginBase + "/search/tv?" + params.Encode())
	if err != nil {
		return OriginInfo{}, false, err
	}
	defer resp.Body.Close()

	var result struct {
		Results []struct {
			OriginalLanguage string   `json:"original_language"`
			OriginCountry    []string `json:"origin_country"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return OriginInfo{}, false, fmt.Errorf("decode TV search: %w", err)
	}
	if len(result.Results) == 0 {
		return OriginInfo{}, false, nil
	}
	r := result.Results[0]
	country := ""
	if len(r.OriginCountry) > 0 {
		country = r.OriginCountry[0]
	}
	return OriginInfo{OrigLang: r.OriginalLanguage, OriginCountry: country}, true, nil
}

// GetMovieOrigin searches /search/movie (for original_language and ID), then
// fetches /movie/{id} for production_countries to determine the origin country.
func (c *httpTMDBOriginClient) GetMovieOrigin(title, year string) (OriginInfo, bool, error) {
	params := url.Values{"api_key": {c.apiKey}, "query": {title}}
	if year != "" {
		params.Set("year", year)
	}
	resp, err := c.client.Get(tmdbOriginBase + "/search/movie?" + params.Encode())
	if err != nil {
		return OriginInfo{}, false, err
	}
	defer resp.Body.Close()

	var searchResult struct {
		Results []struct {
			ID               int    `json:"id"`
			OriginalLanguage string `json:"original_language"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return OriginInfo{}, false, fmt.Errorf("decode movie search: %w", err)
	}
	if len(searchResult.Results) == 0 {
		return OriginInfo{}, false, nil
	}
	hit := searchResult.Results[0]

	// Fetch movie details to get production_countries.
	detURL := fmt.Sprintf("%s/movie/%d?api_key=%s",
		tmdbOriginBase, hit.ID, url.QueryEscape(c.apiKey))
	detResp, err := c.client.Get(detURL)
	if err != nil {
		return OriginInfo{}, false, err
	}
	defer detResp.Body.Close()

	var det struct {
		ProductionCountries []struct {
			ISO31661 string `json:"iso_3166_1"`
		} `json:"production_countries"`
	}
	if err := json.NewDecoder(detResp.Body).Decode(&det); err != nil {
		return OriginInfo{}, false, fmt.Errorf("decode movie details: %w", err)
	}
	country := ""
	if len(det.ProductionCountries) > 0 {
		country = det.ProductionCountries[0].ISO31661
	}
	return OriginInfo{OrigLang: hit.OriginalLanguage, OriginCountry: country}, true, nil
}

// dirYearRe matches a " (YYYY)" suffix at the end of a directory name.
var dirYearRe = regexp.MustCompile(`\s+\((\d{4})\)\s*$`)

// dirTitleYear parses a content-root directory name (e.g. "Show Name (2023)")
// into a search title and year. Strips any file extension first so flat movie
// filenames work too.
func dirTitleYear(dirName string) (title, year string) {
	// Strip file extension if present (flat movie libraries use filenames as roots).
	if idx := strings.LastIndex(dirName, "."); idx > 0 {
		dirName = dirName[:idx]
	}
	m := dirYearRe.FindStringSubmatchIndex(dirName)
	if m == nil {
		return strings.TrimSpace(dirName), ""
	}
	year = dirName[m[2]:m[3]]
	title = strings.TrimSpace(dirName[:m[0]])
	return
}
