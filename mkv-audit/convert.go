package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	// errMKVExists means <name>.mkv already sits next to the source.
	errMKVExists = errors.New("an MKV with the same name already exists")
	// errUnsupportedContainer means mkvmerge can't read the source's tracks.
	errUnsupportedContainer = errors.New("mkvmerge can't read this container")
)

// MKVConverter remuxes a non-MKV video file to MKV.
type MKVConverter interface {
	// ConvertToMKV writes <name>.mkv next to src, then removes src. A non-empty
	// path with a non-nil error means the MKV was written but a later step
	// (copying timestamps, removing src) failed.
	ConvertToMKV(src string) (string, error)
}

// convertExts are the video containers the walk converts to MKV. .wmv is
// listed so it gets a warning: mkvmerge can't read it.
var convertExts = []string{".avi", ".flv", ".m2ts", ".m4v", ".mov", ".mp4", ".mpeg", ".mpg", ".ts", ".webm", ".wmv"}

// isConvertible reports whether path has one of convertExts.
func isConvertible(path string) bool {
	return slices.Contains(convertExts, strings.ToLower(filepath.Ext(path)))
}

// convertAll converts each convertible path in paths to MKV and returns a new
// list with the MKV in its place. A source that is skipped or fails to
// convert is dropped; a failure is also recorded in stats. Each path appears
// once, so a retry list holding both x.mp4 and x.mkv doesn't scan x.mkv twice.
func convertAll(paths []string, converter MKVConverter, stats *Stats) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	keep := func(p string) {
		if k := pathKey(p); !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	for _, p := range paths {
		if !isConvertible(p) {
			keep(p)
			continue
		}
		base := filepath.Base(p)
		dst, err := converter.ConvertToMKV(p)
		switch {
		case errors.Is(err, errMKVExists):
			fmt.Printf("[WARN]   %s: %s already exists, not converting\n", base, filepath.Base(strings.TrimSuffix(p, filepath.Ext(p))+".mkv"))
		case errors.Is(err, errUnsupportedContainer):
			fmt.Printf("[WARN]   %s: %v, not converting\n", base, err)
		case dst == "":
			fmt.Printf("[ERR]    %s: convert: %v\n", base, err)
			stats.Failed++
			stats.FailedPaths = append(stats.FailedPaths, p)
		default:
			fmt.Printf("[CONVERT] %s → %s\n", base, filepath.Base(dst))
			if err != nil {
				fmt.Printf("[WARN]   %s: %v\n", base, err)
			}
			stats.Converted++
			keep(dst)
		}
	}
	return out
}

// loggingConverter wraps an MKVConverter and prints the planned conversion.
type loggingConverter struct {
	inner MKVConverter
}

func (l *loggingConverter) ConvertToMKV(src string) (string, error) {
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".mkv"
	fmt.Printf("  convert: mkvmerge -o %q %q\n", filepath.Base(dst), filepath.Base(src))
	return l.inner.ConvertToMKV(src)
}

// ConvertToMKV implements MKVConverter on realMKVRunner.
func (r *realMKVRunner) ConvertToMKV(src string) (string, error) {
	return convertToMKV(src, r, r.mux)
}

// mux runs mkvmerge to copy every track of src into a new MKV at dst.
func (r *realMKVRunner) mux(src, dst string) error {
	out, err := exec.Command(r.mkvmerge, "-o", dst, src).CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// Warnings only: the output is complete, but show them, since the
		// source is deleted once the MKV checks out.
		fmt.Printf("[WARN]   %s: mkvmerge warnings:\n%s\n", filepath.Base(src), strings.TrimSpace(string(out)))
		return nil
	}
	if err != nil {
		return fmt.Errorf("mkvmerge: %w\n%s", err, out)
	}
	return nil
}

// convertToMKV remuxes src to <name>.mkv via a temp file, checks the result
// has every source track, gives it src's timestamps so the cutoff filter still
// sees the real date, and then removes src. On any failure before the rename
// src is left untouched and no MKV is created.
func convertToMKV(src string, ident MKVIdentifier, mux func(src, dst string) error) (string, error) {
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".mkv"
	if _, err := os.Lstat(dst); err == nil {
		return "", errMKVExists
	}

	srcStat, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	// Identify parses mkvmerge's JSON even when it exits non-zero, so an
	// unreadable container shows up as no tracks; an error is a real failure.
	srcInfo, err := ident.Identify(src)
	if err != nil {
		return "", fmt.Errorf("identify: %w", err)
	}
	if len(srcInfo.Tracks) == 0 {
		return "", errUnsupportedContainer
	}

	// Short temp name in the same directory, as Remux does: the rename stays
	// on one volume and the path stays within MAX_PATH.
	tmpFile, err := os.CreateTemp(filepath.Dir(src), "mkv-audit-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	_ = os.Remove(tmp) // mkvmerge creates the output file itself

	if err := mux(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	outInfo, err := ident.Identify(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("identify converted file: %w", err)
	}
	if len(outInfo.Tracks) != len(srcInfo.Tracks) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("converted file has %d tracks, source has %d", len(outInfo.Tracks), len(srcInfo.Tracks))
	}
	// os.Rename replaces an existing file, so check again in case dst
	// appeared while mkvmerge ran.
	if _, err := os.Lstat(dst); err == nil {
		_ = os.Remove(tmp)
		return "", errMKVExists
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename converted file: %w", err)
	}

	if err := copyFileTimes(dst, srcStat); err != nil {
		return dst, fmt.Errorf("copy timestamps: %w; kept %s", err, filepath.Base(src))
	}
	if err := os.Remove(src); err != nil {
		return dst, fmt.Errorf("remove original: %w; delete it by hand", err)
	}
	return dst, nil
}

// copyFileTimes gives path the modification time of src, and its creation
// time where the platform records one.
func copyFileTimes(path string, src os.FileInfo) error {
	if err := os.Chtimes(path, time.Time{}, src.ModTime()); err != nil {
		return err
	}
	return copyCreationTime(path, src)
}
