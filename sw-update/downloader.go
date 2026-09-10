package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// browserUserAgent is sent on asset downloads. Bot-protection front ends
// (Cloudflare among them) answer Go's default "Go-http-client/1.1" with a 403
// on a cache miss, so a download has to look like an ordinary browser.
const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// downloadAttempts is how many times a download is tried before giving up.
const downloadAttempts = 3

// downloadAsset downloads a single asset to destDir and returns the local path.
// referer is the page the asset was found on; sites that only serve downloads
// to visitors of their own pages need it.
func downloadAsset(asset Asset, destDir, referer string) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, asset.Name)

	fmt.Printf("  Downloading %s...\n", asset.Name)

	client := &http.Client{Timeout: 10 * time.Minute}

	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt-1) * 3 * time.Second
			fmt.Printf("  Attempt %d failed (%v); retrying in %s...\n", attempt-1, lastErr, delay)
			time.Sleep(delay)
		}

		err := fetchToFile(client, asset.URL, referer, destPath)
		if err == nil {
			fmt.Printf("  Saved to %s\n", destPath)
			return destPath, nil
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}
	return "", lastErr
}

// fetchToFile GETs rawURL and writes the response body to destPath.
func fetchToFile(client *http.Client, rawURL, referer, destPath string) error {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "*/*")
	if ref := downloadReferer(rawURL, referer); ref != "" {
		req.Header.Set("Referer", ref)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return &httpStatusError{StatusCode: resp.StatusCode, URL: rawURL}
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Close()
}

// downloadReferer returns referer when it shares a host with the asset URL, and
// the asset's own origin otherwise — a cross-site Referer is worse than none.
func downloadReferer(rawURL, referer string) string {
	asset, err := url.Parse(rawURL)
	if err != nil || asset.Host == "" {
		return ""
	}
	if referer != "" {
		if ref, err := url.Parse(referer); err == nil && strings.EqualFold(ref.Host, asset.Host) {
			return referer
		}
	}
	return asset.Scheme + "://" + asset.Host + "/"
}

// httpStatusError is a non-200 response to a download request.
type httpStatusError struct {
	StatusCode int
	URL        string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d downloading %s", e.StatusCode, e.URL)
}

// isRetryable reports whether err is worth another attempt: transport trouble,
// or a status that tends to be transient (rate limiting, bot-protection blips,
// server errors). A 404 or a 401 will not fix itself.
func isRetryable(err error) bool {
	var status *httpStatusError
	if !errors.As(err, &status) {
		// Transport-level failure — connection reset, timeout, DNS.
		return true
	}
	switch status.StatusCode {
	case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status.StatusCode >= 500
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
