package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		fmt.Fprint(w, classificationHTMLWith("The Dark Knight", "M"))
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
		fmt.Fprint(w, classificationHTMLWith("Some Other Film", "R16"))
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

// dropServer drops its first drops connections the way mode says, then serves
// a page rating "The Dark Knight" M. *requests counts every request.
func dropServer(t *testing.T, mode string, drops int, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(requests.Add(1)) > drops {
			fmt.Fprint(w, classificationHTMLWith("The Dark Knight", "M"))
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		switch mode {
		case "reset":
			_ = conn.(*net.TCPConn).SetLinger(0)
		case "truncated":
			fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n<html><body><h3>The Dark")
			_ = buf.Flush()
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// #173: a dropped connection, including a page cut off mid-read, is retried
// once; dropped twice, the search fails rather than parsing a partial page.
func TestClassificationSource_RetriesDroppedConnectionOnce(t *testing.T) {
	for _, mode := range []string{"closed", "reset", "truncated"} {
		for _, drops := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", mode, drops), func(t *testing.T) {
				var requests atomic.Int32
				srv := dropServer(t, mode, drops, &requests)
				src := &classificationSource{baseURL: srv.URL, httpClient: &http.Client{}}

				rating, err := src.FetchRating("", "The Dark Knight")

				if requests.Load() != 2 {
					t.Errorf("%d requests; want 2", requests.Load())
				}
				if drops == 1 && (err != nil || rating != RatingM) {
					t.Errorf("got %q, %v; want M from the retry", rating, err)
				}
				if drops == 2 && (err == nil || !connectionDropped(err) ||
					!strings.Contains(err.Error(), "/find-a-rating/") || !strings.HasSuffix(err.Error(), "(after one retry)")) {
					t.Errorf("got %q, %v; want the dropped-connection error naming the page and the retry", rating, err)
				}
			})
		}
	}
}

// A timeout mid-page isn't a dropped connection, so it isn't retried.
func TestClassificationSource_TimeoutNotRetried(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, "<html><body>")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	src := &classificationSource{baseURL: srv.URL, httpClient: &http.Client{Timeout: 100 * time.Millisecond}}

	_, err := src.FetchRating("", "The Dark Knight")

	if err == nil || connectionDropped(err) || requests.Load() != 1 {
		t.Errorf("err %v after %d requests; want a timeout after 1", err, requests.Load())
	}
}
