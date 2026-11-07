package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// httpTimeout bounds every outbound HTTP request in this tool so a hung
// connection can't freeze a whole batch run indefinitely.
const httpTimeout = 30 * time.Second

const tmdbAPIBase = "https://api.themoviedb.org/3"
const tmdbImageBase = "https://image.tmdb.org/t/p/w500"

// MovieDetails holds the full metadata fetched from TMDB for a movie.
type MovieDetails struct {
	TMDBID           string
	Title            string
	Overview         string
	ReleaseDate      string // "YYYY-MM-DD" or ""
	PosterPath       string // e.g. "/abc123.jpg"
	OriginalLanguage string // ISO 639-1 code, e.g. "ja", "en"
}

// PosterURL returns the full image URL for the poster, or "" if none.
func (d MovieDetails) PosterURL() string {
	if d.PosterPath == "" {
		return ""
	}
	return tmdbImageBase + d.PosterPath
}

// tmdbMovieSource implements RatingSource using the TMDB API.
// It searches for a movie by title+year, then fetches the NZ certification
// from /movie/{id}/release_dates.
// If apiKey is empty, FetchRating immediately returns (RatingNone, nil).
type tmdbMovieSource struct {
	apiKey     string
	httpClient *http.Client
}

func newTMDBMovieSource(apiKey string) *tmdbMovieSource {
	return &tmdbMovieSource{apiKey: apiKey, httpClient: &http.Client{Timeout: httpTimeout}}
}

// Name implements RatingSource.
func (t *tmdbMovieSource) Name() string { return "TMDB" }

// FetchRating implements RatingSource.
// key is unused (movies have no prior TMDB ID); title is "Title:YEAR" or just the title.
// year is embedded in the title string by the caller as "title:YEAR"; we split it out.
// Actually the caller passes title and year separately via the key parameter convention:
// key = TMDB numeric ID when already known; title = movie title for search.
func (t *tmdbMovieSource) FetchRating(key, title string) (NZRating, error) {
	if t.apiKey == "" {
		return RatingNone, nil
	}

	// key may be a TMDB numeric ID from a previous cache entry — skip search if so.
	var tmdbID string
	var year string
	if isNumeric(key) {
		tmdbID = key
	} else {
		// key is "title:YEAR" — extract year for the search.
		if idx := strings.LastIndex(key, ":"); idx >= 0 {
			year = key[idx+1:]
		}
		var err error
		tmdbID, err = t.searchMovie(title, year)
		if err != nil {
			return RatingNone, fmt.Errorf("TMDB search %q: %w", title, err)
		}
		if tmdbID == "" {
			return RatingNone, nil // no match found
		}
	}

	return t.nzCertification(tmdbID)
}

// SearchMovieID searches TMDB for title+year and returns the numeric TMDB ID
// of the best match, or "" if nothing found. Exported for testing.
func (t *tmdbMovieSource) SearchMovieID(title, year string) (string, error) {
	return t.searchMovie(title, year)
}

func (t *tmdbMovieSource) searchMovie(title, year string) (string, error) {
	u := fmt.Sprintf("%s/search/movie?api_key=%s&query=%s&language=en-US&include_adult=true",
		tmdbAPIBase, url.QueryEscape(t.apiKey), url.QueryEscape(title))
	if year != "" {
		u += "&year=" + url.QueryEscape(year)
	}

	resp, err := t.httpClient.Get(u)
	if err != nil {
		return "", redactAPIKey(err)
	}
	defer resp.Body.Close()

	var result struct {
		Results []struct {
			ID          int    `json:"id"`
			ReleaseDate string `json:"release_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode search response: %w", err)
	}

	for _, r := range result.Results {
		// Match by year if provided — release_date is "YYYY-MM-DD".
		if year == "" || strings.HasPrefix(r.ReleaseDate, year) {
			return fmt.Sprintf("%d", r.ID), nil
		}
	}
	return "", nil
}

// nzCertification fetches the NZ release certification for a TMDB movie ID.
func (t *tmdbMovieSource) nzCertification(tmdbID string) (NZRating, error) {
	u := fmt.Sprintf("%s/movie/%s/release_dates?api_key=%s",
		tmdbAPIBase, tmdbID, url.QueryEscape(t.apiKey))

	resp, err := t.httpClient.Get(u)
	if err != nil {
		return RatingNone, redactAPIKey(err)
	}
	defer resp.Body.Close()

	var result struct {
		Results []struct {
			ISO   string `json:"iso_3166_1"`
			Dates []struct {
				Cert string `json:"certification"`
				Type int    `json:"type"`
			} `json:"release_dates"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return RatingNone, fmt.Errorf("decode release_dates: %w", err)
	}

	for _, country := range result.Results {
		if country.ISO != "NZ" {
			continue
		}
		// Prefer theatrical release (type 3); fall back to any non-empty cert.
		var fallback string
		for _, d := range country.Dates {
			if d.Cert == "" {
				continue
			}
			if d.Type == 3 {
				if r, ok := ParseNZRating(d.Cert); ok {
					return r, nil
				}
			}
			if fallback == "" {
				fallback = d.Cert
			}
		}
		if fallback != "" {
			if r, ok := ParseNZRating(fallback); ok {
				return r, nil
			}
		}
	}
	return RatingNone, nil
}

// GetMovieDetails fetches title, overview, release_date, and poster_path from TMDB.
func (t *tmdbMovieSource) GetMovieDetails(tmdbID string) (MovieDetails, error) {
	u := fmt.Sprintf("%s/movie/%s?api_key=%s&language=en-US",
		tmdbAPIBase, tmdbID, url.QueryEscape(t.apiKey))

	resp, err := t.httpClient.Get(u)
	if err != nil {
		return MovieDetails{}, redactAPIKey(err)
	}
	defer resp.Body.Close()

	var result struct {
		Title            string `json:"title"`
		Overview         string `json:"overview"`
		ReleaseDate      string `json:"release_date"`
		PosterPath       string `json:"poster_path"`
		OriginalLanguage string `json:"original_language"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return MovieDetails{}, fmt.Errorf("decode movie details: %w", err)
	}
	return MovieDetails{
		TMDBID:           tmdbID,
		Title:            result.Title,
		Overview:         result.Overview,
		ReleaseDate:      result.ReleaseDate,
		PosterPath:       result.PosterPath,
		OriginalLanguage: result.OriginalLanguage,
	}, nil
}

// SearchAndFetch searches TMDB for title+year, then fetches full movie details.
// Returns ("", MovieDetails{}, nil) if no match is found.
func (t *tmdbMovieSource) SearchAndFetch(title, year string) (string, MovieDetails, error) {
	if t.apiKey == "" {
		return "", MovieDetails{}, nil
	}
	tmdbID, err := t.searchMovie(title, year)
	if err != nil {
		return "", MovieDetails{}, fmt.Errorf("search %q: %w", title, err)
	}
	if tmdbID == "" {
		return "", MovieDetails{}, nil
	}
	details, err := t.GetMovieDetails(tmdbID)
	if err != nil {
		return tmdbID, MovieDetails{}, err
	}
	return tmdbID, details, nil
}

// isNumeric returns true when s contains only digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// yearTokenRe matches a bare 4-digit year in the range 1900–2099.
var yearTokenRe = regexp.MustCompile(`(?:19|20)\d{2}`)

// lastYearMatch returns the last such year in filename that is bounded by
// non-digits, and the index in filename where it starts (or -1 if none).
// Scanning for the LAST candidate stops a number embedded in the title (e.g.
// "1917") from being mistaken for the release year that follows it, as in
// "1917.2019.1080p.BluRay". A regex that consumed the surrounding delimiters
// could not match two adjacent years (they share a delimiter), so the boundary
// check is done manually here instead.
func lastYearMatch(filename string) (year string, start int) {
	s := " " + filename + " " // pad so boundary lookups never go out of range
	year, start = "", -1
	for _, loc := range yearTokenRe.FindAllStringIndex(s, -1) {
		if !isASCIIDigit(s[loc[0]-1]) && !isASCIIDigit(s[loc[1]]) {
			year = s[loc[0]:loc[1]]
			start = loc[0] - 1 // map the padded index back to a filename index
		}
	}
	return year, start
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// reverseChars maps Unicode lookalike characters back to their ASCII originals.
// These are used as filesystem-safe substitutes for characters illegal in filenames.
var reverseChars = []struct{ from, to string }{
	{"꞉", ":"},
	{"╱", "/"},
	{"╲", `\`},
	{"│", "|"},
	{"＊", "*"},
	{"？", "?"},
	{"＂", `"`},
	{"＜", "<"},
	{"＞", ">"},
}

// titleFromFilename derives a human-readable movie title from a filename.
// It strips the extension, removes the year and everything after it, then
// replaces dots and underscores with spaces.
func titleFromFilename(filename, year string) string {
	// Strip extension.
	if idx := strings.LastIndex(filename, "."); idx > 0 {
		filename = filename[:idx]
	}

	// If year is known, truncate at first occurrence of the year pattern.
	// Patterns with a trailing separator handle mid-stem years; patterns without
	// handle a year at the very end of the (already extension-stripped) stem.
	if year != "" {
		patterns := []string{
			"." + year + ".",
			"_" + year + "_",
			"(" + year + ")",
			"[" + year + "]",
			" " + year + " ",
			// year at end of stem:
			"." + year,
			"_" + year,
			" " + year,
		}
		for _, p := range patterns {
			if idx := strings.Index(filename, p); idx >= 0 {
				filename = filename[:idx]
				break
			}
		}
	} else {
		// Try to detect a year automatically, using the last year so a number
		// in the title isn't mistaken for the release year.
		if _, start := lastYearMatch(filename); start > 0 {
			filename = strings.TrimRight(filename[:start], ". _([")
		}
	}

	// Replace separators with spaces.
	filename = strings.ReplaceAll(filename, ".", " ")
	filename = strings.ReplaceAll(filename, "_", " ")

	// Restore Unicode lookalike characters to their ASCII originals.
	for _, r := range reverseChars {
		filename = strings.ReplaceAll(filename, r.from, r.to)
	}
	return strings.TrimSpace(filename)
}

// redactAPIKey blanks the api_key in a *url.Error, whose message otherwise
// prints the whole request URL, key included.
func redactAPIKey(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	if u, perr := url.Parse(ue.URL); perr == nil && u.Query().Has("api_key") {
		q := u.Query()
		q.Set("api_key", "REDACTED")
		u.RawQuery = q.Encode()
		ue.URL = u.String()
	}
	return err
}
