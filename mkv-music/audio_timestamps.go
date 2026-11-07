package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var audioTimestampExts = map[string]bool{
	".flac": true,
	".wma":  true,
	".mp3":  true,
	".m4a":  true,
	".mka":  true,
	".dsf":  true,
}

// AudioTimestampStats holds counters for the audio timestamp pass.
type AudioTimestampStats struct {
	Updated    int
	SkippedOld int
	NoDate     int
	Failed     int
}

// SetAudioTimestamps walks root, reads each audio file's embedded date tag and
// sets OS file timestamps. Files older than cutoffDate are skipped.
func SetAudioTimestamps(root string, cutoffDate time.Time, timer FileTimer, exiftoolPath string) AudioTimestampStats {
	var stats AudioTimestampStats
	// The callback never returns an error, so neither does WalkDir.
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("[AUDIO] Skipping unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !audioTimestampExts[ext] {
			return nil
		}
		if !cutoffDate.IsZero() {
			info, err := d.Info()
			if err == nil && info.ModTime().Before(cutoffDate) {
				stats.SkippedOld++
				return nil
			}
		}
		dt, ok := readAudioFileDate(path, ext, exiftoolPath)
		if !ok {
			fmt.Printf("[AUDIO] No date tag: %s\n", filepath.Base(path))
			stats.NoDate++
			return nil
		}
		if err := applyFileTimestamps(path, dt, timer); err != nil {
			fmt.Printf("[AUDIO] Error: %s: %v\n", filepath.Base(path), err)
			stats.Failed++
			return nil
		}
		fmt.Printf("[AUDIO] Updated: %s → %s\n", filepath.Base(path), dt.Format("2006-01-02"))
		stats.Updated++
		return nil
	})
	return stats
}

// readAudioFileDate returns the embedded date for an audio file.
// Non-MKA formats are read via go-taglib (supports WMA, FLAC, MP3, M4A, DSF, etc.).
// MKA uses exiftool via stdin since TagLib doesn't support Matroska.
func readAudioFileDate(path, ext, exiftoolPath string) (time.Time, bool) {
	if ext != ".mka" {
		tags, err := readAudioTags(path)
		if err == nil && tags.Date != "" {
			if dt, ok := parseAudioDate(tags.Date); ok {
				return dt, true
			}
		}
		return time.Time{}, false
	}
	for _, tagName := range []string{
		"Matroska:DateTimeOriginal-en-US",
		"Matroska:DateTimeOriginal-und",
		"ContentCreateDate",
		"DateTimeOriginal",
		"Date",
		"Year",
	} {
		dt, ok := exiftoolReadTag(exiftoolPath, path, tagName)
		if ok {
			return dt, true
		}
	}
	return time.Time{}, false
}

// exiftoolReadTag runs exiftool on path for a single tag, piping the file via
// stdin to avoid filename encoding issues on Windows.
func exiftoolReadTag(exiftoolPath, path, tagName string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	cmd := exec.Command(exiftoolPath, "-s3", "-"+tagName, "-")
	cmd.Stdin = f
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return time.Time{}, false
	}
	dt, err := parseExifDate(s)
	if err != nil {
		return time.Time{}, false
	}
	return dt, true
}

// parseExifDate normalises the various date string formats exiftool returns.
func parseExifDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	// Normalise YYYY:MM:DD separator → YYYY-MM-DD
	if len(s) >= 10 && s[4] == ':' && s[7] == ':' {
		s = s[:4] + "-" + s[5:7] + "-" + s[8:]
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"2006-01",
		"2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date: %q", s)
}

// applyFileTimestamps sets both mtime and creation time on path to dt.
func applyFileTimestamps(path string, dt time.Time, timer FileTimer) error {
	if err := timer.SetTimes(path, dt); err != nil {
		return err
	}
	if timer.CreationTimeNeedsUpdate(path, dt.Unix()) {
		return timer.SetCreationTime(path, dt)
	}
	return nil
}
