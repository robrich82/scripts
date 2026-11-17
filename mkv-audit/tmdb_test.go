package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// stubTMDB answers each request path with a status and body; any other path
// is a 500.
type stubTMDB map[string]struct {
	code int
	body string
}

func (s stubTMDB) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, ok := s[r.URL.Path]
	if !ok {
		resp.code, resp.body = http.StatusInternalServerError, `{"status_message":"unexpected"}`
	}
	return &http.Response{StatusCode: resp.code, Status: http.StatusText(resp.code),
		Body: io.NopCloser(strings.NewReader(resp.body)), Header: http.Header{}, Request: r}, nil
}

func stubClient(s stubTMDB) *httpTMDBOriginClient {
	return &httpTMDBOriginClient{apiKey: "k", client: &http.Client{Transport: s}}
}

// TMDB's 404 for an ID it doesn't have is not found, for movies and shows;
// any other failed status stays an error.
func TestHTTPTMDBOriginClient_NotFoundIsNoMatch(t *testing.T) {
	notFound := struct {
		code int
		body string
	}{http.StatusNotFound, `{"status_code":34,"status_message":"The resource you requested could not be found."}`}
	c := stubClient(stubTMDB{"/3/movie/5": notFound, "/3/tv/5": notFound})

	for name, get := range map[string]func(string) (OriginInfo, bool, error){
		"movie": c.GetMovieOriginByID, "tv": c.GetTVOriginByID,
	} {
		if _, found, err := get("5"); found || err != nil {
			t.Errorf("%s 404: found = %v, err = %v; want not found and no error", name, found, err)
		}
		if _, _, err := get("6"); err == nil || !strings.Contains(err.Error(), "status") {
			t.Errorf("%s 500: err = %v; want the status as an error", name, err)
		}
	}
}

// Through the real client, a mapped ID TMDB answers 404 for falls back to the next.
func TestFetchOrigins_HTTPNotFoundFallsBack(t *testing.T) {
	root, paths := siblingLibrary(t, "Amelie (2001)", "a.mkv", "b.mkv", "c.mkv")
	writeSibling(t, movieRatingCachePath(), mustJSON(t, map[string]map[string]string{"files": {paths[0]: "5", paths[1]: "5", paths[2]: "194"}}))
	before, _ := os.ReadFile(movieRatingCachePath())
	c := stubClient(stubTMDB{
		"/3/movie/5":   {http.StatusNotFound, `{"status_code":34}`},
		"/3/movie/194": {http.StatusOK, `{"original_language":"fr","production_countries":[{"iso_3166_1":"FR"}]}`},
	})

	out := captureStdout(t, func() {
		if origins, _ := fetchOrigins(paths, root, "movie", c, noPrompter{t}); origins["Amelie (2001)"] != (OriginInfo{OrigLang: "fr", OriginCountry: "FR"}) {
			t.Errorf("origins = %v; want fr-FR from ID 194", origins)
		}
	})
	if want := "no original language from TMDB for id 5, trying id 194"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	assertCacheUnchanged(t, before)
}
