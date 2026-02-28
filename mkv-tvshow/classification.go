package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const classificationBaseURL = "https://www.classificationoffice.govt.nz"

// classificationSource fetches NZ content ratings from the Classification
// Office website by scraping the "find-a-rating" search page.
// This is inherently fragile — if the site changes its HTML structure the
// scraper may return no results rather than erroring, which causes the caller
// to fall through to the user prompt.
type classificationSource struct {
	baseURL    string
	httpClient *http.Client
}

func newClassificationSource() *classificationSource {
	return &classificationSource{
		baseURL:    classificationBaseURL,
		httpClient: &http.Client{},
	}
}

// Name implements RatingSource.
func (c *classificationSource) Name() string { return "NZ Classification Office" }

// FetchShowRating searches the Classification Office site for showName and
// returns the highest NZ rating found among title-matching results.
// Returns (RatingNone, nil) when no matching results are found.
func (c *classificationSource) FetchShowRating(_, showName string) (NZRating, error) {
	results, err := c.scrapeResults(showName)
	if err != nil {
		return RatingNone, err
	}

	best := RatingNone
	for _, r := range results {
		if titleMatches(r.Title, showName) {
			best = MaxRating(best, r.Rating)
		}
	}
	return best, nil
}

// FetchEpisodeRating implements RatingSource. The Classification Office search
// page does not reliably separate individual episodes, so this always returns
// (RatingNone, nil), causing the caller to fall back to FetchShowRating.
func (c *classificationSource) FetchEpisodeRating(_, _ string, _, _ int) (NZRating, error) {
	return RatingNone, nil
}

type classificationResult struct {
	Title  string
	Rating NZRating
}

// scrapeResults fetches the search page for showName and returns all (title,
// rating) pairs it can extract. It does not apply any title matching.
func (c *classificationSource) scrapeResults(showName string) ([]classificationResult, error) {
	searchURL := fmt.Sprintf("%s/find-a-rating/?search=%s",
		c.baseURL, url.QueryEscape(showName))

	resp, err := c.httpClient.Get(searchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseClassificationHTML(resp.Body)
}

// parseClassificationHTML walks the HTML token stream looking for text nodes
// that are valid NZ ratings, then associates each with the nearest preceding
// title-like text within the same result block.
//
// The Classification Office site renders results as a list of items that
// typically contain a title and a rating label. Because the exact class names
// can change, we use a heuristic: any text-node value that ParseNZRating
// accepts is treated as a rating; the most recent non-empty, non-rating text
// sibling (or near-sibling) is treated as the title.
func parseClassificationHTML(r io.Reader) ([]classificationResult, error) {
	tokenizer := html.NewTokenizer(r)

	var results []classificationResult
	var lastText string // most recent non-rating text seen

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			// End of document or read error — return whatever we have.
			return results, nil

		case html.TextToken:
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text == "" {
				continue
			}
			if rating, ok := ParseNZRating(text); ok {
				// Found a rating — pair it with the most recent title text.
				if lastText != "" {
					results = append(results, classificationResult{
						Title:  lastText,
						Rating: rating,
					})
				}
			} else {
				// Not a rating — treat as a potential title.
				lastText = text
			}
		}
	}
}

// nonAlphanumRe strips characters that are not letters or digits for fuzzy
// title comparison.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9 ]+`)

// titleMatches returns true when scraped contains the normalised form of query.
// Normalisation: lowercase, strip non-alphanumeric, collapse spaces.
func titleMatches(scraped, query string) bool {
	normalize := func(s string) string {
		s = strings.ToLower(s)
		s = nonAlphanumRe.ReplaceAllString(s, " ")
		return strings.Join(strings.Fields(s), " ")
	}
	return strings.Contains(normalize(scraped), normalize(query))
}
