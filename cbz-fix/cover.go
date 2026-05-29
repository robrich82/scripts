package main

import (
	"archive/zip"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// updateCoverCBZ checks for a cover.jpg sibling and, when it is a higher
// resolution than the cover entry inside the CBZ, replaces it.
//
// When one dimension is strictly greater and the other strictly less, prompt
// is called to ask the user what to do.  In dry-run mode prompt is never
// called; the file is instead reported as "needs attention" (skipped=false).
func updateCoverCBZ(cbzPath string, dryRun, verbose, noBackup bool, prompt func(q string) (bool, error)) (skipped bool, err error) {
	dir := filepath.Dir(cbzPath)
	newCoverPath := filepath.Join(dir, "cover.jpg")

	if _, statErr := os.Stat(newCoverPath); os.IsNotExist(statErr) {
		return true, nil
	} else if statErr != nil {
		return false, fmt.Errorf("stat cover.jpg: %w", statErr)
	}

	newW, newH, err := imageDimsFile(newCoverPath)
	if err != nil {
		return false, err
	}

	zr, err := zip.OpenReader(cbzPath)
	if err != nil {
		return false, fmt.Errorf("open: %w", err)
	}

	var coverEntry *zip.File
	for _, f := range zr.File {
		base := strings.ToLower(filepath.Base(f.Name))
		if strings.HasPrefix(base, "cover.") && isImageFile(f.Name) {
			coverEntry = f
			break
		}
	}
	if coverEntry == nil {
		zr.Close()
		return true, nil
	}

	oldW, oldH, err := imageDimsZip(coverEntry)
	if err != nil {
		zr.Close()
		return false, err
	}

	bothBetter := newW >= oldW && newH >= oldH && (newW > oldW || newH > oldH)
	wOnly := newW > oldW && newH < oldH
	hOnly := newH > oldH && newW < oldW
	noImprovement := newW <= oldW && newH <= oldH

	if noImprovement {
		zr.Close()
		if verbose {
			fmt.Printf("  cover.jpg (%dx%d) not larger than %s (%dx%d); skipping\n",
				newW, newH, filepath.Base(coverEntry.Name), oldW, oldH)
		}
		return true, nil
	}

	replace := false
	switch {
	case bothBetter:
		replace = true
		if verbose {
			fmt.Printf("  cover.jpg (%dx%d) > %s (%dx%d); replacing\n",
				newW, newH, filepath.Base(coverEntry.Name), oldW, oldH)
		}
	case wOnly || hOnly:
		if dryRun {
			fmt.Printf("  cover.jpg (%dx%d) vs %s (%dx%d) — mixed dimensions, would prompt\n",
				newW, newH, filepath.Base(coverEntry.Name), oldW, oldH)
			zr.Close()
			return false, nil
		}
		q := fmt.Sprintf("  cover.jpg (%dx%d) vs %s (%dx%d) — replace? [y/n]: ",
			newW, newH, filepath.Base(coverEntry.Name), oldW, oldH)
		yes, pErr := prompt(q)
		if pErr != nil {
			zr.Close()
			return false, pErr
		}
		replace = yes
	}

	if !replace {
		zr.Close()
		return true, nil
	}

	if dryRun {
		zr.Close()
		return false, nil
	}

	newCoverData, err := os.ReadFile(newCoverPath)
	if err != nil {
		zr.Close()
		return false, fmt.Errorf("read cover.jpg: %w", err)
	}

	tmpPath, err := writeCBZWithNewCover(zr, coverEntry.Name, newCoverData, dir)
	zr.Close()
	if err != nil {
		return false, fmt.Errorf("write new CBZ: %w", err)
	}

	if !noBackup {
		if err := copyFile(cbzPath, cbzPath+".bak"); err != nil {
			os.Remove(tmpPath)
			return false, fmt.Errorf("backup: %w", err)
		}
	}

	if err := os.Rename(tmpPath, cbzPath); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("replace original: %w", err)
	}

	return false, nil
}

// imageDimsFile returns the pixel dimensions of an image file on disk.
func imageDimsFile(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return cfg.Width, cfg.Height, nil
}

// imageDimsZip returns the pixel dimensions of a zip entry.
func imageDimsZip(f *zip.File) (w, h int, err error) {
	rc, err := f.Open()
	if err != nil {
		return 0, 0, err
	}
	defer rc.Close()
	cfg, _, err := image.DecodeConfig(rc)
	if err != nil {
		return 0, 0, fmt.Errorf("decode %s: %w", f.Name, err)
	}
	return cfg.Width, cfg.Height, nil
}

// writeCBZWithNewCover writes a new CBZ to a temp file in dir, copying all
// entries from zr except the old cover (oldCoverName), which is replaced by
// "cover.jpg" containing newCoverData.  Returns the temp file path.
func writeCBZWithNewCover(zr *zip.ReadCloser, oldCoverName string, newCoverData []byte, dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, "cbzfix-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	zw := zip.NewWriter(tmp)
	cleanup := func() {
		zw.Close()
		tmp.Close()
		os.Remove(tmpPath)
	}

	// New cover goes first.
	w, err := zw.Create("cover.jpg")
	if err != nil {
		cleanup()
		return "", fmt.Errorf("create cover.jpg: %w", err)
	}
	if _, err := w.Write(newCoverData); err != nil {
		cleanup()
		return "", fmt.Errorf("write cover.jpg: %w", err)
	}

	// Copy remaining entries, skipping the replaced cover.
	for _, f := range zr.File {
		if f.Name == oldCoverName {
			continue
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("create entry %s: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			cleanup()
			return "", fmt.Errorf("open entry %s: %w", f.Name, err)
		}
		_, cpErr := io.Copy(w, rc)
		rc.Close()
		if cpErr != nil {
			cleanup()
			return "", fmt.Errorf("copy entry %s: %w", f.Name, cpErr)
		}
	}

	if err := zw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("close zip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}
