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
	newProcessor := func() *Processor {
		return newTestProcessor(t, root, Config{MediaDir: root, ProcessAll: true},
			cacheWith(t, map[string]string{}), &fakeTMDB{}, &fakeMKVRunner{avail: true}, &scriptedPrompter{})
	}
	walks := map[string]func(){
		"Run":           func() { newProcessor().Run() },
		"RunDebandOnly": func() { newProcessor().RunDebandOnly() },
		"showFoldersWithMKV": func() {
			if got := showFoldersWithMKV(root); len(got) != 0 {
				t.Errorf("showFoldersWithMKV = %v, want none", got)
			}
		},
	}
	for name, walk := range walks {
		t.Run(name, func(t *testing.T) {
			if out := captureStdout(t, walk); !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		})
	}
}
