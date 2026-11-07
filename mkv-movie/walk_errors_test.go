package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureStdout returns what fn prints to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	w.Close()
	return string(<-done)
}

// TestWalks_UnreadableRootWarns covers every directory walk: an unreadable
// library is reported rather than looking like an empty one.
func TestWalks_UnreadableRootWarns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "offline")
	want := "[!] Skipping unreadable " + root
	walks := map[string]func(t *testing.T){
		"walkAndProcess": func(t *testing.T) {
			walkAndProcess(root, &fakeTMDBMovieClient{}, nil, &fakeFileTimer{}, nil, makeCache(t), nil, false, time.Time{})
		},
		"runDebandOnly": func(t *testing.T) {
			runDebandOnly(root, map[string]MovieManualEntry{}, &fakeMKVTagger{available: true}, &fakeFileTimer{})
		},
		"planSetRating": func(t *testing.T) {
			if plans, unknown := planSetRating(root, makeCache(t)); len(plans)+len(unknown) != 0 {
				t.Errorf("planSetRating = %v, %v; want nothing", plans, unknown)
			}
		},
	}
	for name, walk := range walks {
		t.Run(name, func(t *testing.T) {
			if out := captureStdout(t, func() { walk(t) }); !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		})
	}
}
