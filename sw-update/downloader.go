package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// userAgent identifies sw-update on every request. Bot-protection front ends
// 403 both Go's default "Go-http-client/1.1" (falcosoft.hu) and a spoofed
// browser UA on a non-browser connection (SourceForge), but accept this.
const userAgent = "sw-update/1.0 (+https://github.com/)"

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

// maxInterstitialHops bounds how many "your download will start shortly"
// pages fetchToFile follows before giving up.
const maxInterstitialHops = 3

// fetchToFile GETs rawURL and writes the response body to destPath. An HTML
// page served in place of a non-HTML asset (SourceForge's /download links) is
// followed through its meta refresh rather than saved as the asset.
func fetchToFile(client *http.Client, rawURL, referer, destPath string) error {
	for hop := 0; ; hop++ {
		resp, err := get(client, rawURL, referer)
		if err != nil {
			return err
		}
		if !isHTMLResponse(resp) || isHTMLName(destPath) {
			defer resp.Body.Close()
			return writeBody(resp.Body, destPath)
		}

		page, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		next := metaRefreshURL(page, resp.Request.URL)
		if next == "" || hop >= maxInterstitialHops {
			return &htmlPageError{URL: rawURL}
		}
		referer, rawURL = resp.Request.URL.String(), next
	}
}

// get issues a download GET and returns the response if it is a 200.
func get(client *http.Client, rawURL, referer string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	if ref := downloadReferer(rawURL, referer); ref != "" {
		req.Header.Set("Referer", ref)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, &httpStatusError{StatusCode: resp.StatusCode, URL: rawURL}
	}
	return resp, nil
}

func writeBody(body io.Reader, destPath string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, body); err != nil {
		return err
	}
	return f.Close()
}

func isHTMLResponse(resp *http.Response) bool {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func isHTMLName(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

var (
	metaTagRe     = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	httpEquivRe   = regexp.MustCompile(`(?i)http-equiv\s*=\s*["']?refresh\b`)
	metaContentRe = regexp.MustCompile(`(?is)content\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	refreshURLRe  = regexp.MustCompile(`(?i)url\s*=\s*['"]?([^'"]+)`)
)

// metaRefreshURL returns the absolute target of page's <meta http-equiv="refresh">,
// or "" when it has none.
func metaRefreshURL(page []byte, base *url.URL) string {
	for _, tag := range metaTagRe.FindAll(page, -1) {
		if !httpEquivRe.Match(tag) {
			continue
		}
		c := metaContentRe.FindSubmatch(tag)
		if c == nil {
			continue
		}
		content := html.UnescapeString(string(c[1]) + string(c[2]))
		m := refreshURLRe.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		target, err := base.Parse(strings.TrimSpace(m[1]))
		if err != nil {
			continue
		}
		return target.String()
	}
	return ""
}

// htmlPageError is a web page served where a download was expected — typically
// an interstitial or login page with no machine-followable link to the file.
type htmlPageError struct {
	URL string
}

func (e *htmlPageError) Error() string {
	return fmt.Sprintf("%s returned a web page instead of the file", e.URL)
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
	var page *htmlPageError
	if errors.As(err, &page) {
		return false
	}
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
