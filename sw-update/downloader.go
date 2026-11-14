package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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
// browser UA on a non-browser connection (SourceForge), but accept this. It
// carries no "(...)" comment: amd.com's Akamai front end resets any request
// whose UA has one.
const userAgent = "sw-update/1.0"

// downloadTransport carries download requests; nil means http.DefaultTransport.
// Tests set it to a TLS test server's transport.
var downloadTransport http.RoundTripper

// downloadAttempts is how many times a download is tried before giving up.
const downloadAttempts = 3

// retryBackoff is the wait before the second attempt; each later one waits
// another retryBackoff longer. A variable so tests can shorten it.
var retryBackoff = 3 * time.Second

// downloadAsset downloads a single asset to destDir and returns the local path.
// referer is the page the asset was found on; sites that only serve downloads
// to visitors of their own pages need it.
func downloadAsset(asset Asset, destDir, referer string) (string, error) {
	if err := checkAssetName(asset.Name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, asset.Name)

	fmt.Printf("  Downloading %s...\n", asset.Name)

	client := &http.Client{
		Timeout:       10 * time.Minute,
		Transport:     downloadTransport,
		CheckRedirect: httpsOnlyRedirect,
	}

	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt-1) * retryBackoff
			fmt.Printf("  Attempt %d failed (%v); retrying in %s...\n", attempt-1, lastErr, delay)
			time.Sleep(delay)
		}

		err := fetchToFile(client, asset.URL, referer, destPath, asset.SHA256)
		if err == nil {
			if asset.SHA256 != "" {
				fmt.Println("  SHA-256 verified")
			}
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

// httpsOnlyRedirect is an http.Client CheckRedirect that refuses any redirect
// to a non-https URL; Go follows an https -> http redirect by default.
func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return &insecureURLError{URL: req.URL.String()}
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// checkAssetName rejects anything but a plain file name. Names scraped from
// links are URL-decoded, so "%5C..%5C" arrives as a backslash path that
// filepath.Join would resolve outside destDir.
func checkAssetName(name string) error {
	if name == "." || name == ".." || strings.ContainsAny(name, `/\:`) || !filepath.IsLocal(name) {
		return fmt.Errorf("unsafe asset name %q", name)
	}
	return nil
}

// maxInterstitialHops bounds how many "your download will start shortly"
// pages fetchToFile follows before giving up.
const maxInterstitialHops = 3

// fetchToFile GETs rawURL and writes the response body to destPath, checking it
// against wantSHA256 when that is set. An HTML page served in place of a
// non-HTML asset (SourceForge's /download links) is followed through its meta
// refresh; one without a refresh is an htmlPageError.
func fetchToFile(client *http.Client, rawURL, referer, destPath, wantSHA256 string) error {
	pageURL := rawURL
	for hop := 0; ; hop++ {
		resp, err := getDownload(client, pageURL, referer)
		if err != nil {
			return err
		}
		body := bufio.NewReader(resp.Body)
		if isHTMLName(destPath) || !isHTMLPage(resp, body) {
			err := writeBody(body, destPath, wantSHA256)
			resp.Body.Close()
			return err
		}

		page, err := io.ReadAll(io.LimitReader(body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		next := metaRefreshURL(page, resp.Request.URL)
		if next == "" {
			return &htmlPageError{URL: rawURL}
		}
		if hop == maxInterstitialHops {
			return &htmlPageError{URL: rawURL, Hops: hop}
		}
		referer, pageURL = resp.Request.URL.String(), next
	}
}

// getDownload issues a GET with the download headers; on a 200 the caller
// closes the body, any other status is an httpStatusError.
func getDownload(client *http.Client, rawURL, referer string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" {
		return nil, &insecureURLError{URL: rawURL}
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

// writeBody saves body to destPath via destPath+".part", renamed into place only
// once complete and matching wantSHA256 (unchecked when empty), so unverified
// bytes never sit under the installer's name or replace an earlier download.
func writeBody(body io.Reader, destPath, wantSHA256 string) (err error) {
	partPath := destPath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		if rmErr := os.Remove(partPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = errors.Join(err, fmt.Errorf("could not delete %s: %w", partPath, rmErr))
		}
	}()

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), body)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return err
	}
	if wantSHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
			return &sha256MismatchError{Want: wantSHA256, Got: got}
		}
	}
	return os.Rename(partPath, destPath)
}

// insecureURLError is a download URL, asset URL, redirect or refresh target
// that isn't https: sw-update runs what it downloads, so neither the file nor
// the page naming it is fetched in the clear.
type insecureURLError struct {
	URL string
}

func (e *insecureURLError) Error() string {
	return fmt.Sprintf("refusing non-https URL %s", e.URL)
}

// sha256MismatchError is a download whose content doesn't match the digest its
// source published.
type sha256MismatchError struct {
	Want, Got string
}

func (e *sha256MismatchError) Error() string {
	return fmt.Sprintf("SHA-256 mismatch, download discarded: got %s, want %s", e.Got, e.Want)
}

// isHTMLPage reports whether resp is a web page: labelled HTML and starting
// like one, so a binary a server mislabels as text/html is still saved.
func isHTMLPage(resp *http.Response, body *bufio.Reader) bool {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return false
	}
	head, _ := body.Peek(512)
	return strings.HasPrefix(http.DetectContentType(head), "text/html")
}

func isHTMLName(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

var (
	reMetaTag          = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reHTTPEquivRefresh = regexp.MustCompile(`(?i)\shttp-equiv\s*=\s*["']?refresh\b`)
	reMetaContent      = regexp.MustCompile(`(?is)\scontent\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	reRefreshURL       = regexp.MustCompile(`(?i)url\s*=\s*['"]?([^'"]+)`)
	// Only ;-terminated entities: html.UnescapeString alone turns "&region=" into "®ion=".
	reEntity = regexp.MustCompile(`&#?[0-9A-Za-z]+;`)
)

// metaRefreshURL returns the absolute http(s) target of page's
// <meta http-equiv="refresh">, or "" when it has none.
func metaRefreshURL(page []byte, base *url.URL) string {
	for _, tag := range reMetaTag.FindAll(page, -1) {
		if !reHTTPEquivRefresh.Match(tag) {
			continue
		}
		c := reMetaContent.FindSubmatch(tag)
		if c == nil {
			continue
		}
		content := reEntity.ReplaceAllStringFunc(string(c[1])+string(c[2])+string(c[3]), html.UnescapeString)
		m := reRefreshURL.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		target, err := base.Parse(strings.TrimSpace(m[1]))
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			continue
		}
		return target.String()
	}
	return ""
}

// htmlPageError is a web page served where a file was expected. Hops is set
// when it was still a page after following maxInterstitialHops refreshes.
type htmlPageError struct {
	URL  string
	Hops int
}

func (e *htmlPageError) Error() string {
	if e.Hops > 0 {
		return fmt.Sprintf("%s still returned a web page after following %d refreshes", e.URL, e.Hops)
	}
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
	var insecure *insecureURLError
	var mismatch *sha256MismatchError
	if errors.As(err, &page) || errors.As(err, &insecure) || errors.As(err, &mismatch) {
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

// runOrOpen executes an installer, extracts an archive, or opens the folder in
// Explorer. password decrypts encrypted .zip/.7z archives.
func runOrOpen(path, password string) error {
	lower := strings.ToLower(filepath.Base(path))

	switch {
	case strings.HasSuffix(lower, ".exe"):
		fmt.Printf("  Launching %s...\n", filepath.Base(path))
		return runElevated(path, "")

	case strings.HasSuffix(lower, ".msi"):
		fmt.Printf("  Running msiexec for %s...\n", filepath.Base(path))
		return runElevated("msiexec", fmt.Sprintf(`/i "%s"`, path))

	case strings.HasSuffix(lower, ".zip"), strings.HasSuffix(lower, ".7z"):
		destDir := strings.TrimSuffix(path, filepath.Ext(path))
		if err := extractArchive(path, destDir, password); err != nil {
			return err
		}
		fmt.Printf("  Extracted to %s\n", destDir)
		return openExplorer(destDir)

	default:
		// .tar.gz, etc. — just open the containing folder.
		fmt.Printf("  Opening folder for %s\n", filepath.Base(path))
		return openExplorer(filepath.Dir(path))
	}
}

// openExplorer opens a folder in Windows Explorer.
func openExplorer(path string) error {
	return exec.Command("explorer", path).Start()
}
