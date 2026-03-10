package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func classificationHTMLWith(title, rating string) string {
	return `<html><body><div class="result">` +
		`<h3>` + title + `</h3>` +
		`<span class="rating">` + rating + `</span>` +
		`</div></body></html>`
}

func TestClassificationSource_FetchRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(classificationHTMLWith("The Dark Knight", "M")))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchRating("", "The Dark Knight")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingM {
		t.Errorf("got %q; want M", rating)
	}
}

func TestClassificationSource_FetchRating_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(classificationHTMLWith("Some Other Film", "R16")))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchRating("", "The Dark Knight")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("expected RatingNone for non-matching title, got %q", rating)
	}
}

func TestClassificationSource_FetchRating_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: &http.Client{}}
	_, err := src.FetchRating("", "Some Movie")
	if err == nil {
		t.Error("expected network error")
	}
}

func TestParseClassificationHTML_ExtractsRatings(t *testing.T) {
	body := strings.NewReader(`<html><body>
		<h3>My Film</h3><span>R16</span>
		<h3>Another Film</h3><span>PG</span>
	</body></html>`)

	results := parseClassificationHTML(body)
	if len(results) != 2 {
		t.Fatalf("got %d results; want 2", len(results))
	}
	if results[0].Rating != RatingR16 {
		t.Errorf("result[0].Rating = %q; want R16", results[0].Rating)
	}
	if results[1].Rating != RatingPG {
		t.Errorf("result[1].Rating = %q; want PG", results[1].Rating)
	}
}

func TestTitleMatches(t *testing.T) {
	tests := []struct {
		scraped, query string
		want           bool
	}{
		{"The Dark Knight", "The Dark Knight", true},
		{"the dark knight", "The Dark Knight", true},
		{"The Dark Knight: Extended", "The Dark Knight", true},
		{"Some Other Film", "The Dark Knight", false},
		{"My Film - Director's Cut", "My Film", true},
		{"My Film", "My Film Director Cut", false},
	}
	for _, tc := range tests {
		got := titleMatches(tc.scraped, tc.query)
		if got != tc.want {
			t.Errorf("titleMatches(%q, %q) = %v; want %v", tc.scraped, tc.query, got, tc.want)
		}
	}
}
