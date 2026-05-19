package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// MKVRemuxer creates a new MKV file with a subset/reordering of tracks.
type MKVRemuxer interface {
	Remux(filePath string, plan *TrimPlan, info MKVInfo) error
}

// Remux implements MKVRemuxer on realMKVRunner. It uses mkvmerge to produce a
// new file containing only the tracks in plan.KeepAudio and plan.KeepSubs, in
// the desired order, then atomically replaces the original file.
func (r *realMKVRunner) Remux(filePath string, plan *TrimPlan, info MKVInfo) error {
	// Use a short-named temp file in the same directory so rename works across
	// the same drive, but the path stays within MAX_PATH even for long filenames.
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), "mkv-audit-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath) // mkvmerge creates the output file itself

	args := []string{"-o", tmpPath}

	// Audio track selection.
	if len(plan.KeepAudio) == 0 {
		args = append(args, "--no-audio")
	} else {
		ids := make([]string, len(plan.KeepAudio))
		for i, t := range plan.KeepAudio {
			ids[i] = strconv.Itoa(t.ID)
		}
		args = append(args, "--audio-tracks", strings.Join(ids, ","))
	}

	// Subtitle track selection.
	if len(plan.KeepSubs) == 0 {
		args = append(args, "--no-subtitles")
	} else {
		ids := make([]string, len(plan.KeepSubs))
		for i, t := range plan.KeepSubs {
			ids[i] = strconv.Itoa(t.ID)
		}
		args = append(args, "--subtitle-tracks", strings.Join(ids, ","))
	}

	// Build track order: video tracks first (original order), then kept audio
	// (desired order), then kept subtitles.
	var orderParts []string
	for _, t := range info.Tracks {
		if t.Type == "video" {
			orderParts = append(orderParts, fmt.Sprintf("0:%d", t.ID))
		}
	}
	for _, t := range plan.KeepAudio {
		orderParts = append(orderParts, fmt.Sprintf("0:%d", t.ID))
	}
	for _, t := range plan.KeepSubs {
		orderParts = append(orderParts, fmt.Sprintf("0:%d", t.ID))
	}
	if len(orderParts) > 0 {
		args = append(args, "--track-order", strings.Join(orderParts, ","))
	}

	args = append(args, filePath)

	if out, err := exec.Command(r.mkvmerge, args...).CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("mkvmerge: %w\n%s", err, out)
	}

	// Atomic-ish replacement. Windows does not support rename-over-existing,
	// so remove the original first then rename the temp file into place.
	if err := os.Remove(filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("remove original: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename remuxed file: %w (temp file left at %s)", err, tmpPath)
	}
	return nil
}
