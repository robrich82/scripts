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
// once, retried as the run goes on, and once more by finish. A nil
// *cacheSaver, for a cache the run has none of, saves nothing.
type cacheSaver struct {
	cache     interface{ Save() error }
	name      string // the cache, as messages name it, e.g. "rating cache"
	path      string // the cache file
	holds     string // what a lost save loses, e.g. "Plex keys and audio languages"
	saveErr   error  // the last save's error; only the first in a run of failures is printed
	lastSaved string // the show after which the cache was last saved
}

func newShowSaver(c *ShowCache) *cacheSaver {
	if c == nil {
		return nil
	}
	return &cacheSaver{cache: c, name: "show cache", path: c.path, holds: "show matches"}
}

func newRatingSaver(c *RatingCache, holds string) *cacheSaver {
	if c == nil {
		return nil
	}
	return &cacheSaver{cache: c, name: "rating cache", path: c.path, holds: holds}
}

func newTitleSaver(c *TitleCache) *cacheSaver {
	if c == nil {
		return nil
	}
	return &cacheSaver{cache: c, name: "title cache", path: c.path, holds: "full titles"}
}

func newIgnoreSaver(c *MissingIgnoreList) *cacheSaver {
	if c == nil {
		return nil
	}
	return &cacheSaver{cache: c, name: "ignore list", path: c.path, holds: "ignore choices"}
}

func newRetrySaver(c *RetryList) *cacheSaver {
	if c == nil {
		return nil
	}
	return &cacheSaver{cache: c, name: "retry list", path: c.path, holds: "changes to the episodes to retry"}
}

// save saves the cache; name is the show finish names if a later save fails.
func (s *cacheSaver) save(name string) {
	if s == nil {
		return
	}
	err := s.cache.Save()
	switch {
	case err != nil && s.saveErr == nil:
		fmt.Printf("[!] Failed to save %s %s: %v\n%s    Retrying as the run goes on; until a save succeeds, changes are kept in memory only.\n",
			s.name, s.path, withoutPaths(err), hintLine(err))
	case err == nil && s.saveErr != nil:
		fmt.Printf("[i] %s saved.\n", capitalize(s.name))
	}
	if err == nil {
		s.lastSaved = name
	}
	s.saveErr = err
}

// finish retries a failed last save once, says on stderr what was lost if it
// fails again, and reports whether it did.
func (s *cacheSaver) finish() (lost bool) {
	if s == nil || s.saveErr == nil {
		return false
	}
	// One more try: on Windows the rename fails while another program
	// (antivirus, an indexer) briefly holds the cache open.
	beforeFinalSave()
	err := s.cache.Save()
	switch {
	case err == nil:
		fmt.Printf("[i] %s saved.\n", capitalize(s.name))
	case s.lastSaved == "":
		fmt.Fprintf(os.Stderr, "[!] The %s %s was never saved; this run's %s are lost: %v\n%s",
			s.name, s.path, s.holds, withoutPaths(err), hintLine(err))
	default:
		fmt.Fprintf(os.Stderr, "[!] The %s %s was last saved after %s; later %s are lost: %v\n%s",
			s.name, s.path, s.lastSaved, s.holds, withoutPaths(err), hintLine(err))
	}
	s.saveErr = nil
	return err != nil
}

// saveOnce saves a cache a run writes only at its end, retrying once like
// finish, says on stderr what was lost if both tries fail, and reports
// whether they did.
func (s *cacheSaver) saveOnce() (lost bool) {
	if s == nil || s.cache.Save() == nil {
		return false
	}
	beforeFinalSave()
	err := s.cache.Save()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] Failed to save %s %s; this run's %s are lost: %v\n%s",
			s.name, s.path, s.holds, withoutPaths(err), hintLine(err))
	}
	return err != nil
}

// beforeFinalSave runs before the last save attempt of finish and saveOnce; tests replace it.
var beforeFinalSave = func() { time.Sleep(time.Second) }

// withoutPaths drops the file names a failed save's error carries (the temp
// file, and the cache a rename targets): the message names the cache already.
func withoutPaths(err error) error {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// hintLine is the save hint for err as an indented line, or "" without one.
func hintLine(err error) string {
	if hint := accessHint(err, true); hint != "" {
		return "    " + hint + "\n"
	}
	return ""
}

// lostSavesLine is the line a run's summary ends with when n cache saves
// were lost, or "" when none were.
func lostSavesLine(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("Cache saves failed: %d (see the [!] lines above); exiting with status 1.\n", n)
}

// exitIfSavesLost prints lostSavesLine to stderr and exits 1 when n > 0.
func exitIfSavesLost(n int) {
	if line := lostSavesLine(n); line != "" {
		fmt.Fprint(os.Stderr, line)
		os.Exit(1)
	}
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
	hint := accessHint(err, false)
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
