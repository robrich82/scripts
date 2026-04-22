package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ShowResult is a single entry from the TMDB TV search endpoint.
type ShowResult struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	FirstAirDate string `json:"first_air_date"`
}

// Episode holds metadata for a single TV episode from TMDB.
type Episode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	Overview      string `json:"overview"`
}

// SeasonResult holds poster and episode list for a TMDB season.
type SeasonResult struct {
	PosterPath string    `json:"poster_path"`
	AirDate    string    `json:"air_date"`
	Episodes   []Episode `json:"episodes"`
}

// ShowSeason holds summary metadata for a single season as returned by
// the TMDB /tv/{id} endpoint's "seasons" array.
type ShowSeason struct {
	SeasonNumber int    `json:"season_number"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
	Name         string `json:"name"`
}

// ShowDetails holds top-level show metadata from TMDB.
type ShowDetails struct {
	PosterPath       string       `json:"poster_path"`
	FirstAirDate     string       `json:"first_air_date"`
	Name             string       `json:"name"`
	Seasons          []ShowSeason `json:"seasons"`
	OriginalLanguage string       `json:"original_language"`
}

// TMDBClient is the interface for all TMDB API calls made by this tool.
type TMDBClient interface {
	SearchTV(query string) ([]ShowResult, error)
	GetShow(id string) (ShowDetails, error)
	GetSeason(showID string, season int) (SeasonResult, error)
	// GetContentRatings returns the NZ content rating for a TV series.
	// Returns (RatingNone, nil) when no NZ rating is present in TMDB.
	GetContentRatings(showID string) (NZRating, error)
}

// httpTMDBClient is the production TMDBClient that makes real HTTP requests.
type httpTMDBClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func newHTTPTMDBClient(apiKey, baseURL string) *httpTMDBClient {
	return &httpTMDBClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

func (c *httpTMDBClient) SearchTV(query string) ([]ShowResult, error) {
	params := url.Values{"api_key": {c.apiKey}, "query": {query}}
	resp, err := c.client.Get(c.baseURL + "/search/tv?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Results []ShowResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Results, nil
}

func (c *httpTMDBClient) GetShow(id string) (ShowDetails, error) {
	params := url.Values{"api_key": {c.apiKey}}
	resp, err := c.client.Get(fmt.Sprintf("%s/tv/%s?%s", c.baseURL, id, params.Encode()))
	if err != nil {
		return ShowDetails{}, err
	}
	defer resp.Body.Close()

	var d ShowDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return ShowDetails{}, err
	}
	return d, nil
}

func (c *httpTMDBClient) GetContentRatings(showID string) (NZRating, error) {
	params := url.Values{"api_key": {c.apiKey}}
	resp, err := c.client.Get(
		fmt.Sprintf("%s/tv/%s/content_ratings?%s", c.baseURL, showID, params.Encode()),
	)
	if err != nil {
		return RatingNone, err
	}
	defer resp.Body.Close()

	var data struct {
		Results []struct {
			ISO31661 string `json:"iso_3166_1"`
			Rating   string `json:"rating"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return RatingNone, err
	}
	for _, r := range data.Results {
		if r.ISO31661 == "NZ" {
			if rating, ok := ParseNZRating(r.Rating); ok {
				return rating, nil
			}
		}
	}
	return RatingNone, nil
}

// Name implements RatingSource.
func (c *httpTMDBClient) Name() string { return "TMDB" }

// FetchShowRating implements RatingSource by calling GetContentRatings.
func (c *httpTMDBClient) FetchShowRating(tmdbID, _ string) (NZRating, error) {
	return c.GetContentRatings(tmdbID)
}

// FetchEpisodeRating implements RatingSource. TMDB has no per-episode NZ ratings.
func (c *httpTMDBClient) FetchEpisodeRating(_, _ string, _, _ int) (NZRating, error) {
	return RatingNone, nil
}

func (c *httpTMDBClient) GetSeason(showID string, season int) (SeasonResult, error) {
	params := url.Values{"api_key": {c.apiKey}}
	resp, err := c.client.Get(
		fmt.Sprintf("%s/tv/%s/season/%d?%s", c.baseURL, showID, season, params.Encode()),
	)
	if err != nil {
		return SeasonResult{}, err
	}
	defer resp.Body.Close()

	var sr SeasonResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return SeasonResult{}, err
	}
	return sr, nil
}
