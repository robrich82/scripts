package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestTMDB(handler http.Handler) (*httpTMDBClient, *httptest.Server) {
	srv := httptest.NewServer(handler)
	c := newHTTPTMDBClient("test-key", srv.URL)
	return c, srv
}

func TestHTTPTMDBClient_SearchTV_Results(t *testing.T) {
	body := `{"results":[{"id":123,"name":"Test Show","first_air_date":"2020-01-01"}]}`
	c, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	results, err := c.SearchTV("Test Show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].ID != 123 || results[0].Name != "Test Show" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestHTTPTMDBClient_SearchTV_Empty(t *testing.T) {
	c, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	results, err := c.SearchTV("Nothing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestHTTPTMDBClient_SearchTV_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately to force a connection error
	c := newHTTPTMDBClient("key", srv.URL)

	_, err := c.SearchTV("anything")
	if err == nil {
		t.Error("expected network error")
	}
}

func TestHTTPTMDBClient_GetShow(t *testing.T) {
	body := `{"poster_path":"/abc.jpg"}`
	c, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	details, err := c.GetShow("123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.PosterPath != "/abc.jpg" {
		t.Errorf("got PosterPath %q, want \"/abc.jpg\"", details.PosterPath)
	}
}

func TestHTTPTMDBClient_GetSeason(t *testing.T) {
	body := `{
		"poster_path":"/season.jpg",
		"episodes":[
			{"episode_number":1,"air_date":"2020-04-01","overview":"Ep 1 summary"},
			{"episode_number":2,"air_date":"2020-04-08","overview":"Ep 2 summary"}
		]
	}`
	c, srv := newTestTMDB(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	sr, err := c.GetSeason("123", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sr.PosterPath != "/season.jpg" {
		t.Errorf("got PosterPath %q, want \"/season.jpg\"", sr.PosterPath)
	}
	if len(sr.Episodes) != 2 {
		t.Fatalf("got %d episodes, want 2", len(sr.Episodes))
	}
	if sr.Episodes[0].Overview != "Ep 1 summary" {
		t.Errorf("unexpected episode overview: %q", sr.Episodes[0].Overview)
	}
}
