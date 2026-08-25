package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// idLookupTMDBClient never finds anything by search, but resolves a single
// known TMDB ID via GetMovieDetails.
type idLookupTMDBClient struct {
	knownID  string
	details  MovieDetails
	idsAsked []string
}

func (f *idLookupTMDBClient) SearchAndFetch(_, _ string) (string, MovieDetails, error) {
	return "", MovieDetails{}, nil
}

func (f *idLookupTMDBClient) GetMovieDetails(id string) (MovieDetails, error) {
	f.idsAsked = append(f.idsAsked, id)
	if id != f.knownID {
		return MovieDetails{}, fmt.Errorf("no movie with id %s", id)
	}
	return f.details, nil
}

// answerPrompter replies with each answer in turn.
func answerPrompter(answers ...string) func(string) (string, error) {
	i := 0
	return func(string) (string, error) {
		if i >= len(answers) {
			return "", nil
		}
		a := answers[i]
		i++
		return a, nil
	}
}

func TestParseTMDBID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"12345", "12345"},
		{"  12345  ", "12345"},
		{"https://www.themoviedb.org/movie/299534-avengers-endgame", "299534"},
		{"themoviedb.org/movie/299534", "299534"},
		{"tt4154796", ""},
		{"", ""},
		{"not a movie", ""},
	}
	for _, c := range cases {
		if got := parseTMDBID(c.in); got != c.want {
			t.Errorf("parseTMDBID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPromptTMDBID_BlankSkips(t *testing.T) {
	tmdb := &idLookupTMDBClient{knownID: "12345"}
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, nil, answerPrompter(""))
	if _, _, ok := rr.promptTMDBID("Some Movie", "2019", tmdb); ok {
		t.Error("blank answer should skip")
	}
}

func TestPromptTMDBID_NilResolverSkips(t *testing.T) {
	var rr *MovieRatingResolver
	if _, _, ok := rr.promptTMDBID("Some Movie", "2019", &idLookupTMDBClient{}); ok {
		t.Error("nil resolver should skip")
	}
}

func TestPromptTMDBID_RepromptsUntilValid(t *testing.T) {
	tmdb := &idLookupTMDBClient{knownID: "12345", details: testDetails("2019-04-26")}
	rr := newMovieRatingResolver(nil, makeCache(t), false, false, nil,
		answerPrompter("nonsense", "999", "https://www.themoviedb.org/movie/12345-x"))

	id, details, ok := rr.promptTMDBID("Some Movie", "2019", tmdb)
	if !ok {
		t.Fatal("expected a resolved ID")
	}
	if id != "12345" {
		t.Errorf("got id %q, want 12345", id)
	}
	if details.ReleaseDate != "2019-04-26" {
		t.Errorf("got details %+v, want release date 2019-04-26", details)
	}
	// "nonsense" must not reach TMDB; "999" is looked up and rejected.
	if len(tmdb.idsAsked) != 2 || tmdb.idsAsked[0] != "999" {
		t.Errorf("ids looked up = %v, want [999 12345]", tmdb.idsAsked)
	}
}

func TestProcessFile_TMDBNotFound_PromptedIDIsUsedAndCached(t *testing.T) {
	path := makeTempMKVNamed(t, "Obscure.Film.2019.mkv")
	past := time.Date(2010, 1, 1, 0, 0, 0, 0, time.Local)
	os.Chtimes(path, past, past)

	tmdb := &idLookupTMDBClient{knownID: "12345", details: testDetails("2019-04-26")}
	applier := &fakeApplier{}
	cache := makeCache(t)
	rr := newMovieRatingResolver(nil, cache, false, false, nil, answerPrompter("12345"))

	result := processFile(path, tmdb, applier, &fakeFileTimer{}, rr, cache, nil, false)
	if result != ResultUpdated {
		t.Errorf("got %v, want ResultUpdated", result)
	}
	if got := cache.GetTMDBID("Obscure Film:2019"); got != "12345" {
		t.Errorf("cached TMDB ID = %q, want 12345", got)
	}
}

func TestProcessFile_TMDBNotFound_NoPrompterStillSkips(t *testing.T) {
	path := makeTempMKVNamed(t, "Obscure.Film.2019.mkv")
	tmdb := &idLookupTMDBClient{knownID: "12345", details: testDetails("2019-04-26")}
	result := processFile(path, tmdb, nil, &fakeFileTimer{}, nil, makeCache(t), nil, false)
	if result != ResultSkipped {
		t.Errorf("got %v, want ResultSkipped", result)
	}
}
