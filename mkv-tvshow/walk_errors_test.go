package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	newProcessor := func(t *testing.T) *Processor {
		return newTestProcessor(t, root, Config{MediaDir: root, ProcessAll: true},
			cacheWith(t, map[string]string{}), &fakeTMDB{}, &fakeMKVRunner{avail: true}, &scriptedPrompter{})
	}
	walks := map[string]func(t *testing.T){
		"Run":           func(t *testing.T) { newProcessor(t).Run() },
		"RunDebandOnly": func(t *testing.T) { newProcessor(t).RunDebandOnly() },
		"TitleChecker.Run": func(t *testing.T) {
			tc := NewTitleChecker(Config{MediaDir: root}, cacheWith(t, map[string]string{}), nil,
				&fakeTMDB{}, &scriptedPrompter{}, nil, newNullTitleCache(t), noopTimer{})
			tc.Run()
		},
		"showFoldersWithMKV": func(t *testing.T) {
			if got := showFoldersWithMKV(root); len(got) != 0 {
				t.Errorf("showFoldersWithMKV = %v, want none", got)
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
