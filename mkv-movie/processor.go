package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MKVSyncer updates the internal MKV header date via mkvpropedit.
type MKVSyncer interface {
	SyncHeader(filePath, dateStr string) error
}

type realMKVSyncer struct {
	path string
}

func newRealMKVSyncer() (*realMKVSyncer, error) {
	path, err := exec.LookPath("mkvpropedit")
	if err != nil {
		return nil, err
	}
	return &realMKVSyncer{path: path}, nil
}

func (s *realMKVSyncer) SyncHeader(filePath, dateStr string) error {
	formatted := dateStr
	if len(dateStr) == 4 {
		formatted = dateStr + "-01-01"
	}
	return exec.Command(s.path, filePath,
		"--edit", "info",
		"--set", "date="+formatted+"T00:00:00Z",
	).Run()
}

// Stats counts processing outcomes.
type Stats struct {
	Updated int
	OK      int
	Skipped int
	Errors  int
}

// FileResult is the outcome of processing a single file.
type FileResult int

const (
	ResultUpdated FileResult = iota
	ResultOK
	ResultSkipped
	ResultError
)

// processFile reads the mediainfo date for filePath, compares it to the
// current MKV header and filesystem timestamps, and updates whatever differs.
// Returns the processing outcome.
func processFile(filePath string, mi MediaInfoReader, syncer MKVSyncer, timer FileTimer) FileResult {
	basename := filepath.Base(filePath)

	bestDate, headerDate, err := mi.Query(filePath)
	if err != nil {
		fmt.Printf("[ERR]     %s: mediainfo: %v\n", basename, err)
		return ResultError
	}
	if bestDate == "" {
		fmt.Printf("[SKIP]    %s (no date found in metadata)\n", basename)
		return ResultSkipped
	}

	dt, err := parseDate(bestDate)
	if err != nil {
		fmt.Printf("[ERR]     %s: parse date %q: %v\n", basename, bestDate, err)
		return ResultError
	}
	ts := dt.Unix()

	headerNeedsUpdate := bestDate != headerDate

	info, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("[ERR]     %s: stat: %v\n", basename, err)
		return ResultError
	}
	mtimeNeedsUpdate := math.Abs(float64(info.ModTime().Unix()-ts)) > 1

	ctimeNeedsUpdate := timer.CreationTimeNeedsUpdate(filePath, ts)

	if headerNeedsUpdate && syncer != nil {
		if err := syncer.SyncHeader(filePath, bestDate); err != nil {
			fmt.Printf("[ERR]     %s: sync header: %v\n", basename, err)
		}
	}
	if mtimeNeedsUpdate {
		if err := timer.SetTimes(filePath, dt); err != nil {
			fmt.Printf("[ERR]     %s: set mtime: %v\n", basename, err)
		}
	}
	if ctimeNeedsUpdate {
		if err := timer.SetCreationTime(filePath, dt); err != nil {
			fmt.Printf("[ERR]     %s: set ctime: %v\n", basename, err)
		}
	}

	if headerNeedsUpdate || mtimeNeedsUpdate || ctimeNeedsUpdate {
		fmt.Printf("[UPDATED] %s -> %s\n", basename, bestDate)
		return ResultUpdated
	}
	fmt.Printf("[OK]      %s (no changes needed)\n", basename)
	return ResultOK
}

// parseDate parses a date string that is either "YYYY" or "YYYY-MM-DD".
func parseDate(s string) (time.Time, error) {
	if len(s) == 4 {
		year, err := strconv.Atoi(s)
		if err != nil {
			return time.Time{}, err
		}
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.Local), nil
	}
	// Accept any string starting with YYYY-MM-DD.
	if len(s) >= 10 {
		s = s[:10]
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// walkAndProcess walks root for .mkv files and processes each one.
func walkAndProcess(root string, mi MediaInfoReader, syncer MKVSyncer, timer FileTimer) Stats {
	var stats Stats
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv") {
			return nil
		}
		switch processFile(path, mi, syncer, timer) {
		case ResultUpdated:
			stats.Updated++
		case ResultOK:
			stats.OK++
		case ResultSkipped:
			stats.Skipped++
		case ResultError:
			stats.Errors++
		}
		return nil
	})
	return stats
}
