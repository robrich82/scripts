//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWin32FileTimer_SetTimes_UpdatesMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mkv")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := time.Date(2018, 6, 15, 12, 0, 0, 0, time.UTC)
	timer := &win32FileTimer{}
	if err := timer.SetTimes(path, target); err != nil {
		t.Fatalf("SetTimes: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	diff := info.ModTime().Unix() - target.Unix()
	if diff < -1 || diff > 1 {
		t.Errorf("mtime = %v, want ~%v (diff %d s)", info.ModTime(), target, diff)
	}
}

func TestWin32FileTimer_SetCreationTime_DoesNotError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mkv")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := time.Date(2015, 3, 10, 8, 0, 0, 0, time.UTC)
	timer := &win32FileTimer{}
	if err := timer.SetCreationTime(path, target); err != nil {
		t.Errorf("SetCreationTime: %v", err)
	}
}

func TestWin32FileTimer_CreationTimeNeedsUpdate_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mkv")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// A target very far in the past should need updating relative to a fresh file.
	pastUnix := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	timer := &win32FileTimer{}
	if !timer.CreationTimeNeedsUpdate(path, pastUnix) {
		t.Error("expected CreationTimeNeedsUpdate=true for a distant past target")
	}
}

func TestWin32FileTimer_CreationTimeNeedsUpdate_NonexistentFile(t *testing.T) {
	timer := &win32FileTimer{}
	// A non-existent file should return false (cannot stat, so no update needed).
	result := timer.CreationTimeNeedsUpdate("/nonexistent/path/file.mkv", time.Now().Unix())
	if result {
		t.Error("expected CreationTimeNeedsUpdate=false for a non-existent file")
	}
}

func TestWin32FileTimer_SetCreationTime_NonexistentFile_ReturnsError(t *testing.T) {
	timer := &win32FileTimer{}
	err := timer.SetCreationTime("/nonexistent/path/file.mkv", time.Now())
	if err == nil {
		t.Error("expected error when setting creation time of non-existent file")
	}
}
