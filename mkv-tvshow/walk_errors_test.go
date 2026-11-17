package main

import (
	"io"
	"io/fs"
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
	newProcessor := func(t *testing.T) *Processor {
		return newTestProcessor(t, root, Config{MediaDir: root, ProcessAll: true},
			cacheWith(t, map[string]string{}), &fakeTMDB{}, &fakeMKVRunner{avail: true}, &scriptedPrompter{})
	}
	walks := map[string]func(t *testing.T){
		"Run":           func(t *testing.T) { newProcessor(t).Run() },
		"RunDebandOnly": func(t *testing.T) { newProcessor(t).RunDebandOnly() },
		"TitleChecker.Run": func(t *testing.T) {
			tc := NewTitleChecker(Config{MediaDir: root}, cacheWith(t, map[string]string{}), nil,
				&fakeTMDB{}, &scriptedPrompter{}, nil, newNullTitleCache(t), noopTimer{}, nil)
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

// statFailingEntry is a DirEntry whose Info fails, as when a file vanishes
// between the directory read and the stat.
type statFailingEntry struct{ fs.DirEntry }

func (statFailingEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

func TestOlderThan(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"old.mkv", "new.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	old := cutoff.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.mkv"), old, old); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range entries {
		want := d.Name() == "old.mkv"
		if got := olderThan(d, cutoff); got != want {
			t.Errorf("olderThan(%s) = %v, want %v", d.Name(), got, want)
		}
		if olderThan(statFailingEntry{d}, cutoff) {
			t.Errorf("olderThan(%s) with failing Info = true, want false so the file is processed", d.Name())
		}
	}
}
