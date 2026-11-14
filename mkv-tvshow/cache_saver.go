package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// cacheSaver saves a cache after each show a run processes, so an
// interrupted run keeps what it already resolved. A failed save is printed
// once, retried as the run goes on, and once more by finish.
type cacheSaver struct {
	cache     interface{ Save() error }
	name      string // the cache, as messages name it, e.g. "rating cache"
	path      string // the cache file
	holds     string // what a lost save loses, e.g. "Plex keys and audio languages"
	saveErr   error  // the last save's error; only the first in a run of failures is printed
	lastSaved string // the show after which the cache was last saved
}

func newRatingSaver(cache *RatingCache, holds string) *cacheSaver {
	return &cacheSaver{cache: cache, name: "rating cache", path: cache.path, holds: holds}
}

// save saves the cache; name is the show finish names if a later save fails.
func (s *cacheSaver) save(name string) {
	err := s.cache.Save()
	switch {
	case err != nil && s.saveErr == nil:
		fmt.Printf("[!] Failed to save %s %s: %v\n%s    Retrying as the run goes on; until a save succeeds, changes are kept in memory only.\n",
			s.name, s.path, err, hintLine(err))
	case err == nil && s.saveErr != nil:
		fmt.Printf("[i] %s saved.\n", capitalize(s.name))
	}
	if err == nil {
		s.lastSaved = name
	}
	s.saveErr = err
}

// finish retries a failed last save once, and says what was lost if it fails
// again.
func (s *cacheSaver) finish() {
	if s.saveErr == nil {
		return
	}
	// One more try: on Windows the rename fails while another program
	// (antivirus, an indexer) briefly holds the cache open.
	beforeFinalSave()
	if err := s.cache.Save(); err == nil {
		fmt.Printf("[i] %s saved.\n", capitalize(s.name))
	} else if s.lastSaved == "" {
		fmt.Printf("[!] The %s was never saved; this run's %s are lost: %v\n%s", s.name, s.holds, err, hintLine(err))
	} else {
		fmt.Printf("[!] The %s was last saved after %s; later %s are lost: %v\n%s", s.name, s.lastSaved, s.holds, err, hintLine(err))
	}
	s.saveErr = nil
}

// saveOnce saves a cache a run writes only at its end, retrying once like
// finish, and says what was lost if both tries fail.
func (s *cacheSaver) saveOnce() {
	if s.cache.Save() == nil {
		return
	}
	beforeFinalSave()
	if err := s.cache.Save(); err != nil {
		fmt.Printf("[!] Failed to save %s %s; this run's %s are lost: %v\n%s", s.name, s.path, s.holds, err, hintLine(err))
	}
}

// beforeFinalSave runs before the last save attempt of finish and saveOnce; tests replace it.
var beforeFinalSave = func() { time.Sleep(time.Second) }

// hintLine is accessHint(err) as an indented line, or "" without one.
func hintLine(err error) string {
	if hint := accessHint(err); hint != "" {
		return "    " + hint + "\n"
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// cacheLoses says, per cache file, what deleting it loses.
var cacheLoses = map[string]string{
	"show_cache.json":     "every show's TMDB match, so the next run searches TMDB for each show and asks you to confirm it again",
	"title_cache.json":    "the full episode titles --title-check wrote, so a normal run may set shorter titles from the filenames",
	"rating_cache.json":   "the NZ ratings, including ones you entered, and which files and Plex shows have them applied",
	"pending_dates.json":  "the episodes to retry (no air date yet, or a failed TMDB lookup), so they are rechecked only with --all or once modified",
	"missing_ignore.json": "the shows, seasons and episodes you told --missing to ignore",
}

// exitOnCacheLoadError reports a cache file that failed to load, with a fix
// that suits the error, and exits.
func exitOnCacheLoadError(path string, err error) {
	// A read error already names the file, which the message names anyway.
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Path == path {
		err = pathErr.Err
	}
	hint := accessHint(err)
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	// show_cache.json is read with a json.Decoder, which reports an empty or
	// truncated file as EOF rather than a syntax error.
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		hint = "It is corrupt: restore it from a backup if you have one, or delete it to start over."
		if loses := cacheLoses[filepath.Base(path)]; loses != "" {
			hint += "\nDeleting it loses " + loses + "."
		}
	}
	fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", path, err)
	if hint != "" {
		fmt.Fprintln(os.Stderr, hint)
	}
	os.Exit(1)
}
