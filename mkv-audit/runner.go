package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// loadFailedCache reads a list of previously failed file paths from disk.
func loadFailedCache() []string {
	data, err := os.ReadFile(failedCacheFile)
	if err != nil {
		return nil
	}
	var paths []string
	json.Unmarshal(data, &paths)
	return paths
}

// saveFailedCache writes the list of failed paths to disk.
func saveFailedCache(paths []string) {
	data, _ := json.MarshalIndent(paths, "", "    ")
	os.WriteFile(failedCacheFile, data, 0644)
}

// RunAll scans root for MKV/MKA files and fixes each one.
func RunAll(root string, ident MKVIdentifier, editor MKVEditor, prompter Prompter) Stats {
	var stats Stats
	unknownCodecs := map[string]bool{}

	dirCfg := loadDirConfig(root)

	// Check for a previous failure cache.
	var filesToScan []string
	oldFails := loadFailedCache()
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

	for _, path := range filesToScan {
		stats.Scanned++
		basename := filepath.Base(path)

		info, err := ident.Identify(path)
		if err != nil {
			fmt.Printf("[ERR]    %s: %v\n", basename, err)
			stats.Failed++
			stats.FailedPaths = append(stats.FailedPaths, path)
			continue
		}

		res := AnalyzeFile(*info, AnalyzeOptions{IETFMap: resolveIETFMap(path, root, dirCfg)})

		for _, c := range res.UnknownCodecs {
			unknownCodecs[c] = true
		}

		if res.Failed {
			fmt.Printf("[FAILED] %s: %s\n", basename, strings.Join(res.FailReasons, ", "))
			stats.Failed++
			stats.FailedPaths = append(stats.FailedPaths, path)
			continue
		}

		if len(res.Edits) == 0 {
			fmt.Printf("[OK]     %s (no changes needed)\n", basename)
			continue
		}

		if err := editor.Apply(path, res.Edits); err != nil {
			fmt.Printf("[ERR]    %s: mkvpropedit: %v\n", basename, err)
			stats.Failed++
			stats.FailedPaths = append(stats.FailedPaths, path)
			continue
		}

		fmt.Printf("[FIXED]  %s\n", basename)
		stats.Fixed++
		stats.LangFixes += res.LocalFixes.Lang
		stats.VideoInherit += res.LocalFixes.Video
		stats.FlagFixes += res.LocalFixes.Flags
		stats.NamesAdded += res.LocalFixes.Names
	}

	// Persist failure list and unknown codecs.
	if len(stats.FailedPaths) > 0 {
		saveFailedCache(stats.FailedPaths)
	} else {
		os.Remove(failedCacheFile)
	}

	if len(unknownCodecs) > 0 {
		var list []string
		for c := range unknownCodecs {
			list = append(list, c)
		}
		data, _ := json.MarshalIndent(list, "", "    ")
		os.WriteFile(unknownCodecsFile, data, 0644)
		fmt.Printf("New formats found: %s\n", unknownCodecsFile)
	}

	return stats
}
