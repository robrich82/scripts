package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// cacheSaver saves a cache after each show a run processes, so an
// interrupted run keeps what it already resolved. A failed save is printed
// once, retried as the run goes on, and once more by finish.
type cacheSaver struct {
	cache     interface{ Save() error }
	name      string // the cache, as messages name it, e.g. "rating cache"
	holds     string // what a lost save loses, e.g. "Plex keys and audio languages"
	saveErr   error  // the last save's error; only the first in a run of failures is printed
	lastSaved string // the show after which the cache was last saved
}

func newRatingSaver(cache *RatingCache, holds string) *cacheSaver {
	return &cacheSaver{cache: cache, name: "rating cache", holds: holds}
}

// save saves the cache; name is the show finish names if a later save fails.
func (s *cacheSaver) save(name string) {
	err := s.cache.Save()
	switch {
	case err != nil && s.saveErr == nil:
		fmt.Printf("[!] Failed to save %s: %v\n    Retrying as the run goes on; until a save succeeds, changes are kept in memory only.\n", s.name, err)
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
		fmt.Printf("[!] The %s was never saved; this run's %s are lost: %v\n", s.name, s.holds, err)
	} else {
		fmt.Printf("[!] The %s was last saved after %s; later %s are lost: %v\n", s.name, s.lastSaved, s.holds, err)
	}
	s.saveErr = nil
}

// beforeFinalSave runs before cacheSaver.finish's last save attempt; tests replace it.
var beforeFinalSave = func() { time.Sleep(time.Second) }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// exitOnCacheLoadError reports a cache file that failed to load, with a fix
// that suits the error, and exits.
func exitOnCacheLoadError(file string, err error) {
	hint := "Close any program that has it open, then try again."
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	// show_cache.json is read with a json.Decoder, which reports an empty or
	// truncated file as EOF rather than a syntax error.
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		hint = "It is corrupt: restore it from a backup if you have one, or delete it to start over."
	}
	fmt.Fprintf(os.Stderr, "Error loading %s: %v\n%s\n", file, err, hint)
	os.Exit(1)
}
