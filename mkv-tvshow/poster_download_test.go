package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTempDir points os.CreateTemp at a fresh dir so tests can check for leaks.
func setTempDir(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	t.Setenv("TMPDIR", dir)
	return dir
}

func TestDownloadPoster(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"jpeg", http.StatusOK, "\xff\xd8\xff\xe0\x00\x10JFIF\x00jpegdata", ""},
		{"png", http.StatusOK, "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", ""},
		{"html page served with 200", http.StatusOK, "<!DOCTYPE html><html><body>Sign in to Wi-Fi</body></html>", "not an image"},
		{"server error", http.StatusServiceUnavailable, "busy", "unexpected status 503"},
		{"empty body", http.StatusOK, "", "empty response body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			dir := setTempDir(t)

			path, err := (&realMKVRunner{}).DownloadPoster(srv.URL + "/poster.jpg")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want %q", err, tc.wantErr)
				}
				if path != "" {
					t.Errorf("path = %q on error; want empty", path)
				}
				if entries, _ := os.ReadDir(dir); len(entries) != 0 {
					t.Errorf("temp files left behind: %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if filepath.Clean(filepath.Dir(path)) != filepath.Clean(dir) {
				t.Errorf("poster written to %q; want it in %q", path, dir)
			}
			got, _ := os.ReadFile(path)
			if string(got) != tc.body {
				t.Errorf("poster content = %q; want %q", got, tc.body)
			}
		})
	}
}
