package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// unusedDrive returns a drive letter with nothing mounted, standing in for an
// offline drive.
func unusedDrive(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are Windows-only")
	}
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Stat(string(c) + `:\`); err != nil {
			return string(c) + ":"
		}
	}
	t.Skip("no unused drive letter")
	return ""
}

func TestRunClean_OfflineDriveKeepsOverrides(t *testing.T) {
	drive := unusedDrive(t)

	t.Setenv("APPDATA", t.TempDir())
	cfgDir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	online := t.TempDir()
	if err := os.Mkdir(filepath.Join(online, "Kept (2020)"), 0o755); err != nil {
		t.Fatal(err)
	}
	offline := drive + `\TV`

	cfg := &GlobalConfig{Libraries: []Library{
		{Name: "Online", Path: online, Overrides: []DirOverride{
			{Path: "Kept (2020)", OriginLang: "jpn"},
			{Path: "Deleted (2019)", OriginLang: "jpn"},
		}},
		{Name: "Offline", Path: offline, Overrides: []DirOverride{
			{Path: "Show A (2001)", OriginLang: "kor"},
			{Path: "Show B (2002)", OriginLang: "jpn"},
		}},
	}}
	if err := saveGlobalConfig(cfg); err != nil {
		t.Fatal(err)
	}

	runClean()

	data, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got GlobalConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	paths := func(lib Library) []string {
		var out []string
		for _, o := range lib.Overrides {
			out = append(out, o.Path)
		}
		return out
	}
	if p := paths(got.Libraries[0]); len(p) != 1 || p[0] != "Kept (2020)" {
		t.Errorf("online library overrides = %v, want only the existing directory", p)
	}
	if p := paths(got.Libraries[1]); len(p) != 2 {
		t.Errorf("offline library overrides = %v, want both kept", p)
	}
}
