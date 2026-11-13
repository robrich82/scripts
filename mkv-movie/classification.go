package main

import (
	"bytes"
	"errors"
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
// scraper may return no results rather than erroring, causing the caller to
// fall through to the user prompt.
type classificationSource struct {
	baseURL    string
	httpClient *http.Client
}

func newClassificationSource() *classificationSource {
	return &classificationSource{
		baseURL:    classificationBaseURL,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// Name implements RatingSource.
func (c *classificationSource) Name() string { return "NZ Classification Office" }

// FetchRating searches the Classification Office site for title and returns
// the highest NZ rating found among title-matching results.
// Returns (RatingNone, nil) when no matching results are found.
func (c *classificationSource) FetchRating(_, title string) (NZRating, error) {
	results, err := c.scrapeResults(title)
	if err != nil {
		return RatingNone, err
	}

	best := RatingNone
	for _, r := range results {
		if titleMatches(r.Title, title) {
			best = MaxRating(best, r.Rating)
		}
	}
	return best, nil
}

type classificationResult struct {
	Title  string
	Rating NZRating
}

// scrapeResults fetches the search page for title and returns all (title,
// rating) pairs it can extract. It does not apply any title matching.
func (c *classificationSource) scrapeResults(title string) ([]classificationResult, error) {
	searchURL := fmt.Sprintf("%s/find-a-rating/?search=%s",
		c.baseURL, url.QueryEscape(title))

	page, err := c.fetch(searchURL)
	if connectionDropped(err) { // the site sometimes drops a connection; a second request works
		if page, err = c.fetch(searchURL); err != nil {
			err = fmt.Errorf("%w (after one retry)", err)
		}
	}
	if err != nil {
		return nil, err
	}
	return parseClassificationHTML(bytes.NewReader(page)), nil
}

// fetch returns the whole body, so a page cut off short of its stated length
// is an error rather than parsed as complete.
func (c *classificationSource) fetch(pageURL string) ([]byte, error) {
	resp, err := c.httpClient.Get(pageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pageURL, err)
	}
	return page, nil
}

func connectionDropped(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errConnReset)
}

// parseClassificationHTML walks the HTML token stream looking for text nodes
// that are valid NZ ratings, then associates each with the nearest preceding
// title-like text within the same result block.
func parseClassificationHTML(r io.Reader) []classificationResult {
	tokenizer := html.NewTokenizer(r)

	var results []classificationResult
	var lastText string

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return results

		case html.TextToken:
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text == "" {
				continue
			}
			if rating, ok := ParseNZRating(text); ok {
				if lastText != "" {
					results = append(results, classificationResult{
						Title:  lastText,
						Rating: rating,
					})
				}
			} else {
				lastText = text
			}
		}
	}
}

// nonAlphanumRe strips characters that are not letters or digits for fuzzy
// title comparison.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9 ]+`)

// titleMatches returns true when scraped contains the normalised form of query.
func titleMatches(scraped, query string) bool {
	normalize := func(s string) string {
		s = strings.ToLower(s)
		s = nonAlphanumRe.ReplaceAllString(s, " ")
		return strings.Join(strings.Fields(s), " ")
	}
	return strings.Contains(normalize(scraped), normalize(query))
}
