package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// classificationHTMLWith returns a minimal HTML page that embeds a title and
// rating as plain text nodes, mimicking what the real site might produce.
func classificationHTMLWith(title, rating string) string {
	return `<html><body><div class="result">` +
		`<h3>` + title + `</h3>` +
		`<span class="rating">` + rating + `</span>` +
		`</div></body></html>`
}

func TestClassificationSource_FetchShowRating_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(classificationHTMLWith("Attack on Titan", "R16")))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchShowRating("", "Attack on Titan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingR16 {
		t.Errorf("got %q; want R16", rating)
	}
}

func TestClassificationSource_FetchShowRating_CaseInsensitiveMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(classificationHTMLWith("attack on titan", "PG")))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchShowRating("", "Attack on Titan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingPG {
		t.Errorf("got %q; want PG", rating)
	}
}

func TestClassificationSource_FetchShowRating_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Result is for a completely different show.
		w.Write([]byte(classificationHTMLWith("Some Other Show", "M")))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchShowRating("", "Attack on Titan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("expected RatingNone for non-matching title, got %q", rating)
	}
}

func TestClassificationSource_FetchShowRating_EmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>No results found.</p></body></html>"))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchShowRating("", "Attack on Titan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingNone {
		t.Errorf("expected RatingNone for empty page, got %q", rating)
	}
}

func TestClassificationSource_FetchShowRating_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately to force a connection error

	src := &classificationSource{baseURL: srv.URL, httpClient: &http.Client{}}
	_, err := src.FetchShowRating("", "Some Show")
	if err == nil {
		t.Error("expected network error")
	}
}

func TestClassificationSource_FetchEpisodeRating_AlwaysEmpty(t *testing.T) {
	src := newClassificationSource()
	r, err := src.FetchEpisodeRating("123", "Some Show", 1, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != RatingNone {
		t.Errorf("expected RatingNone, got %q", r)
	}
}

func TestClassificationSource_MultipleResults_ReturnsHighest(t *testing.T) {
	// Page with two results for the same show, different ratings.
	body := classificationHTMLWith("My Show Series 1", "PG") +
		classificationHTMLWith("My Show Series 2", "R16")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	src := &classificationSource{baseURL: srv.URL, httpClient: srv.Client()}
	rating, err := src.FetchShowRating("", "My Show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rating != RatingR16 {
		t.Errorf("got %q; want R16 (highest across matching results)", rating)
	}
}

func TestTitleMatches(t *testing.T) {
	tests := []struct {
		scraped, query string
		want           bool
	}{
		{"Attack on Titan", "Attack on Titan", true},
		{"attack on titan", "Attack on Titan", true},
		{"Attack on Titan: Final Season", "Attack on Titan", true},
		{"Some Other Show", "Attack on Titan", false},
		{"My Show - Series 1", "My Show", true},
		{"My Show", "My Show - Series 1", false}, // query longer than scraped
	}
	for _, tc := range tests {
		got := titleMatches(tc.scraped, tc.query)
		if got != tc.want {
			t.Errorf("titleMatches(%q, %q) = %v; want %v", tc.scraped, tc.query, got, tc.want)
		}
	}
}

func TestParseClassificationHTML_ExtractsRatings(t *testing.T) {
	html := strings.NewReader(`<html><body>
		<h3>My Show</h3><span>R16</span>
		<h3>Another Show</h3><span>PG</span>
	</body></html>`)

	results, err := parseClassificationHTML(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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
