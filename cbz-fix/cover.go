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
	var ciEntry *zip.File
	for _, f := range zr.File {
		base := strings.ToLower(filepath.Base(f.Name))
		if coverEntry == nil && strings.HasPrefix(base, "cover.") && isImageFile(f.Name) {
			coverEntry = f
		} else if base == "comicinfo.xml" {
			ciEntry = f
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
			verb := "replacing"
			if dryRun {
				verb = "would replace"
			}
			fmt.Printf("  cover.jpg (%dx%d) > %s (%dx%d); %s\n",
				newW, newH, filepath.Base(coverEntry.Name), oldW, oldH, verb)
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

	ciData := updatedComicInfo(ciEntry, newW, newH, len(newCoverData))

	tmpPath, err := writeCBZWithNewCover(zr, coverEntry.Name, newCoverData, ciData, dir)
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

// updatedComicInfo parses ciEntry, updates the Image=0 page dimensions, and
// returns re-marshalled XML.  Fields that were zero (absent) in the original
// are left at zero so omitempty keeps them out of the output.
// Returns nil if ciEntry is nil or on any error (caller skips the update).
func updatedComicInfo(ciEntry *zip.File, newW, newH, newSize int) []byte {
	if ciEntry == nil {
		return nil
	}
	raw, err := readZipEntry(ciEntry)
	if err != nil {
		return nil
	}
	ci, err := parseComicInfo(raw)
	if err != nil {
		return nil
	}
	if ci.Pages != nil {
		for i := range ci.Pages.Pages {
			if ci.Pages.Pages[i].Image == 0 {
				p := &ci.Pages.Pages[i]
				if p.ImageWidth != 0 {
					p.ImageWidth = newW
				}
				if p.ImageHeight != 0 {
					p.ImageHeight = newH
				}
				if p.ImageSize != 0 {
					p.ImageSize = int64(newSize)
				}
				break
			}
		}
	}
	out, err := marshalComicInfo(ci)
	if err != nil {
		return nil
	}
	return out
}

// writeCBZWithNewCover writes a new CBZ to a temp file in dir, copying all
// entries from zr except the old cover (oldCoverName), which is replaced by
// "cover.jpg" containing newCoverData.  If ciData is non-nil the ComicInfo.xml
// entry is replaced with it.  Returns the temp file path.
func writeCBZWithNewCover(zr *zip.ReadCloser, oldCoverName string, newCoverData, ciData []byte, dir string) (string, error) {
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

	// New cover goes first. writeStored uses CreateRaw so CRC32/sizes live in
	// the local header rather than a trailing data descriptor — 7-Zip's update
	// handler cannot modify archives that contain data descriptors, so every
	// entry here must avoid them (matching writeNewCBZ).
	if err := writeStored(zw, "cover.jpg", newCoverData); err != nil {
		cleanup()
		return "", fmt.Errorf("write cover.jpg: %w", err)
	}

	// Copy remaining entries, skipping the replaced cover.
	// Replace ComicInfo.xml content if ciData is provided.
	for _, f := range zr.File {
		if f.Name == oldCoverName {
			continue
		}
		if ciData != nil && strings.EqualFold(filepath.Base(f.Name), "comicinfo.xml") {
			if err := writeStored(zw, f.Name, ciData); err != nil {
				cleanup()
				return "", fmt.Errorf("write ComicInfo.xml: %w", err)
			}
			continue
		}
		// Copy the source entry's raw (already-compressed) bytes, clearing bit 3
		// so the CRC32/sizes from the central directory are written into the
		// local header.
		fh := f.FileHeader
		fh.Flags &^= 0x8
		w, err := zw.CreateRaw(&fh)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("create entry %s: %w", f.Name, err)
		}
		rc, err := f.OpenRaw()
		if err != nil {
			cleanup()
			return "", fmt.Errorf("open raw entry %s: %w", f.Name, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			cleanup()
			return "", fmt.Errorf("copy entry %s: %w", f.Name, err)
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
