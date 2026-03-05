package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const failedCacheFile = "failed_files.json"
const unknownCodecsFile = "unknown_codecs.json"

// MKVIdentifier reads track metadata from an MKV file.
type MKVIdentifier interface {
	Identify(filePath string) (*MKVInfo, error)
}

// MKVEditor applies edits to an MKV file via mkvpropedit.
type MKVEditor interface {
	Apply(filePath string, edits []string) error
}

type realMKVRunner struct {
	mkvmerge    string
	mkvpropedit string
}

func newRealMKVRunner() (*realMKVRunner, error) {
	merge, err := exec.LookPath("mkvmerge")
	if err != nil {
		return nil, fmt.Errorf("mkvmerge not found in PATH")
	}
	prop, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, fmt.Errorf("mkvpropedit not found in PATH")
	}
	return &realMKVRunner{mkvmerge: merge, mkvpropedit: prop}, nil
}

func (r *realMKVRunner) Identify(filePath string) (*MKVInfo, error) {
	out, err := exec.Command(r.mkvmerge, "--identify", "--identification-format", "json", filePath).Output()
	if err != nil {
		// mkvmerge exits non-zero on warnings; try parsing anyway.
		if len(out) == 0 {
			return nil, err
		}
	}
	var info MKVInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (r *realMKVRunner) Apply(filePath string, edits []string) error {
	args := append([]string{filePath}, edits...)
	return exec.Command(r.mkvpropedit, args...).Run()
}

// loadFailedCache reads a list of previously failed file paths from stateDir.
func loadFailedCache(stateDir string) []string {
	data, err := os.ReadFile(filepath.Join(stateDir, failedCacheFile))
	if err != nil {
		return nil
	}
	var paths []string
	json.Unmarshal(data, &paths)
	return paths
}

// saveFailedCache writes the list of failed paths to stateDir.
func saveFailedCache(stateDir string, paths []string) {
	data, _ := json.MarshalIndent(paths, "", "    ")
	os.WriteFile(filepath.Join(stateDir, failedCacheFile), data, 0644)
}

// RunAll scans root for MKV/MKA files and fixes each one.
// stateDir is where failed_files.json and unknown_codecs.json are stored.
// lib is the active library config (may be nil for no overrides).
func RunAll(root, stateDir string, lib *Library, ident MKVIdentifier, editor MKVEditor, prompter Prompter) Stats {
	var stats Stats
	unknownCodecs := map[string]bool{}

	// Check for a previous failure cache.
	var filesToScan []string
	oldFails := loadFailedCache(stateDir)
	if len(oldFails) > 0 {
		ans, _ := prompter.Ask(fmt.Sprintf("Found %d previously failed files. Run only these? (y/n): ", len(oldFails)))
		if strings.ToLower(strings.TrimSpace(ans)) == "y" {
			filesToScan = oldFails
		}
	}

	if len(filesToScan) == 0 {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			lower := strings.ToLower(d.Name())
			if strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mka") {
				filesToScan = append(filesToScan, path)
			}
			return nil
		})
	}

	sem := make(chan struct{}, runtime.NumCPU())
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, path := range filesToScan {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()

			basename := filepath.Base(p)
			var buf strings.Builder
			var local Stats
			var localCodecs []string

			info, err := ident.Identify(p)
			if err != nil {
				fmt.Fprintf(&buf, "[ERR]    %s: %v\n", basename, err)
				local.Failed++
				local.FailedPaths = append(local.FailedPaths, p)
			} else {
				res := AnalyzeFile(*info, AnalyzeOptions{IETFMap: resolveIETFMap(p, root, lib)})
				localCodecs = append(localCodecs, res.UnknownCodecs...)
				if res.Failed {
					fmt.Fprintf(&buf, "[FAILED] %s: %s\n", basename, strings.Join(res.FailReasons, ", "))
					local.Failed++
					local.FailedPaths = append(local.FailedPaths, p)
				} else if len(res.Edits) == 0 {
					fmt.Fprintf(&buf, "[OK]     %s (no changes needed)\n", basename)
				} else if err := editor.Apply(p, res.Edits); err != nil {
					fmt.Fprintf(&buf, "[ERR]    %s: mkvpropedit: %v\n", basename, err)
					local.Failed++
					local.FailedPaths = append(local.FailedPaths, p)
				} else {
					fmt.Fprintf(&buf, "[FIXED]  %s\n", basename)
					local.Fixed++
					local.LangFixes += res.LocalFixes.Lang
					local.VideoInherit += res.LocalFixes.Video
					local.FlagFixes += res.LocalFixes.Flags
					local.NamesAdded += res.LocalFixes.Names
				}
			}

			mu.Lock()
			fmt.Print(buf.String())
			stats.Scanned++
			stats.Failed += local.Failed
			stats.FailedPaths = append(stats.FailedPaths, local.FailedPaths...)
			stats.Fixed += local.Fixed
			stats.LangFixes += local.LangFixes
			stats.VideoInherit += local.VideoInherit
			stats.FlagFixes += local.FlagFixes
			stats.NamesAdded += local.NamesAdded
			for _, c := range localCodecs {
				unknownCodecs[c] = true
			}
			mu.Unlock()
		}(path)
	}

	wg.Wait()

	// Persist failure list and unknown codecs.
	if len(stats.FailedPaths) > 0 {
		os.MkdirAll(stateDir, 0755)
		saveFailedCache(stateDir, stats.FailedPaths)
	} else {
		os.Remove(filepath.Join(stateDir, failedCacheFile))
	}

	if len(unknownCodecs) > 0 {
		var list []string
		for c := range unknownCodecs {
			list = append(list, c)
		}
		os.MkdirAll(stateDir, 0755)
		outPath := filepath.Join(stateDir, unknownCodecsFile)
		data, _ := json.MarshalIndent(list, "", "    ")
		os.WriteFile(outPath, data, 0644)
		fmt.Printf("New formats found: %s\n", outPath)
	}

	return stats
}
