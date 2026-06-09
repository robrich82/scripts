package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// downloadAsset downloads a single asset to destDir and returns the local path.
func downloadAsset(asset Asset, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, asset.Name)

	fmt.Printf("  Downloading %s...\n", asset.Name)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(asset.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, asset.URL)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	fmt.Printf("  Saved to %s\n", destPath)
	return destPath, nil
}

// runOrOpen executes an installer, extracts an archive, or opens the folder in Explorer.
func runOrOpen(path string) error {
	lower := strings.ToLower(filepath.Base(path))

	switch {
	case strings.HasSuffix(lower, ".exe"):
		fmt.Printf("  Launching %s...\n", filepath.Base(path))
		return runElevated(path, "")

	case strings.HasSuffix(lower, ".msi"):
		fmt.Printf("  Running msiexec for %s...\n", filepath.Base(path))
		return runElevated("msiexec", fmt.Sprintf(`/i "%s"`, path))

	case strings.HasSuffix(lower, ".zip"):
		destDir := strings.TrimSuffix(path, ".zip")
		if err := extractZip(path, destDir); err != nil {
			return err
		}
		fmt.Printf("  Extracted to %s\n", destDir)
		return openExplorer(destDir)

	default:
		// .7z, .tar.gz, etc. — just open the containing folder.
		fmt.Printf("  Opening folder for %s\n", filepath.Base(path))
		return openExplorer(filepath.Dir(path))
	}
}

// extractZip extracts a zip archive to destDir.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	fmt.Printf("  Extracting %s...\n", filepath.Base(zipPath))

	for _, f := range r.File {
		destPath := filepath.Join(destDir, f.Name)

		// Prevent zip slip.
		cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(destPath)+string(os.PathSeparator), cleanDest) {
			return fmt.Errorf("zip slip detected: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// openExplorer opens a folder in Windows Explorer.
func openExplorer(path string) error {
	return exec.Command("explorer", path).Start()
}
