package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Asset represents a downloadable file from a release.
type Asset struct {
	Name string
	URL  string
	// SHA256 is the expected hex digest, when the source publishes one.
	SHA256 string
}

// CheckResult holds the outcome of a version check.
type CheckResult struct {
	Version string
	Assets  []Asset
}

// Checker checks for the latest upstream version of an app.
type Checker interface {
	Check(app App) (*CheckResult, error)
}

// NewChecker returns a Checker that dispatches based on app.Checker.Type.
func NewChecker(client *http.Client) Checker {
	return &checkerDispatch{client: client}
}

type checkerDispatch struct {
	client *http.Client
}

func (d *checkerDispatch) Check(app App) (*CheckResult, error) {
	switch app.Checker.Type {
	case "github":
		return (&githubChecker{client: d.client}).check(app)
	case "html":
		return (&htmlChecker{client: d.client}).check(app)
	case "http-header":
		return (&headerChecker{client: d.client}).check(app)
	case "sourceforge":
		return (&sfChecker{client: d.client}).check(app)
	case "":
		return nil, fmt.Errorf("no checker type configured for %q", app.Name)
	default:
		return nil, fmt.Errorf("unknown checker type %q for %q", app.Checker.Type, app.Name)
	}
}

// ---- GitHub checker ----

type githubChecker struct {
	client *http.Client
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"` // "sha256:<hex>"; absent on older assets
}

func (a ghAsset) asset() Asset {
	out := Asset{Name: filepath.Base(a.Name), URL: a.BrowserDownloadURL}
	if sum, ok := strings.CutPrefix(a.Digest, "sha256:"); ok {
		out.SHA256 = sum
	}
	return out
}

func (g *githubChecker) check(app App) (*CheckResult, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases", app.Checker.Repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 120))
		msg := strings.TrimSpace(string(body))
		if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
			msg = msg[:i]
		}
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, msg)
	}

	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		if rel.Prerelease && !app.Checker.Prerelease {
			continue
		}
		assets := selectAssets(rel.Assets, app.Checker.AssetPatterns)
		if app.Checker.RequireAsset && len(assets) == 0 {
			continue
		}
		assets = sortAndLimitAssets(assets, app.Checker.AssetSort)

		// Version normally comes from the tag name, but some projects tag
		// releases by date (e.g. "251105T2353") while encoding the real
		// version in the asset filename. When version_regex is set, extract
		// the version from the first matched asset instead of the tag.
		rawVersion := strings.TrimPrefix(rel.TagName, "v")
		if app.Checker.VersionRegex != "" {
			if len(assets) == 0 {
				continue
			}
			rawVersion, err = applyVersionRegex(assets[0].Name, app.Checker.VersionRegex)
			if err != nil {
				return nil, err
			}
		}
		version, err := applyDateLayout(rawVersion, app.Checker.VersionDateLayout)
		if err != nil {
			return nil, err
		}
		version, err = applyVersionRewrite(version, app.Checker.VersionRewrite)
		if err != nil {
			return nil, err
		}
		return &CheckResult{
			Version: version,
			Assets:  assets,
		}, nil
	}

	return nil, fmt.Errorf("no suitable release found for %s", app.Checker.Repo)
}

// sortAndLimitAssets sorts assets by name and returns only the first when asset_sort is set.
func sortAndLimitAssets(assets []Asset, sort string) []Asset {
	if sort == "" || len(assets) <= 1 {
		return assets
	}
	sorted := slices.Clone(assets)
	switch sort {
	case "name":
		slices.SortFunc(sorted, func(a, b Asset) int { return strings.Compare(a.Name, b.Name) })
	case "name_desc":
		slices.SortFunc(sorted, func(a, b Asset) int { return strings.Compare(b.Name, a.Name) })
	default:
		// Unrecognised asset_sort value: leave the list untouched rather than
		// silently dropping every asset but the first.
		return assets
	}
	return sorted[:1]
}

// matchAssetPattern matches an asset name against a pattern.
// It matches if either a full-string regex match or a filepath.Match glob
// succeeds: a glob like "app-*_64.zip" is also a valid regex ("app" then zero
// or more "-"), so trying regex alone would silently reject it.
func matchAssetPattern(pat, name string) bool {
	if re, err := regexp.Compile("^(?:" + pat + ")$"); err == nil && re.MatchString(name) {
		return true
	}
	matched, _ := filepath.Match(pat, name)
	return matched
}

// selectAssets matches release assets against patterns (or auto-selects for Windows).
func selectAssets(ghAssets []ghAsset, patterns []string) []Asset {
	if len(patterns) > 0 {
		var out []Asset
		for _, pat := range patterns {
			for _, a := range ghAssets {
				if matchAssetPattern(pat, a.Name) {
					out = append(out, a.asset())
				}
			}
		}
		return out
	}
	return autoSelectWindows(ghAssets)
}

var (
	nonWindowsExts        = []string{".appimage", ".deb", ".rpm", ".dmg"}
	nonWindowsSubstrings  = []string{"-linux-", "-macos-", "-darwin-", ".tar.xz", "-steamrt-", "-musl-"}
	windowsPreferKeywords = []string{"win", "windows", "x64", "amd64", "x86_64"}
	windowsPreferExts     = []string{".exe", ".msi", ".zip", ".7z"}
)

func autoSelectWindows(ghAssets []ghAsset) []Asset {
	// Pass 1: exclude obvious non-Windows assets.
	var candidates []ghAsset
	for _, a := range ghAssets {
		if !isNonWindowsAsset(a.Name) {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		candidates = ghAssets
	}

	// Pass 2: prefer assets with explicit Windows keywords.
	var windowsKeyed []ghAsset
	for _, a := range candidates {
		lower := strings.ToLower(a.Name)
		for _, kw := range windowsPreferKeywords {
			if strings.Contains(lower, kw) {
				windowsKeyed = append(windowsKeyed, a)
				break
			}
		}
	}
	if len(windowsKeyed) > 0 {
		candidates = windowsKeyed
	}

	// Pass 3: prefer Windows-friendly extensions.
	var extMatched []ghAsset
	for _, a := range candidates {
		lower := strings.ToLower(a.Name)
		for _, ext := range windowsPreferExts {
			if strings.HasSuffix(lower, ext) {
				extMatched = append(extMatched, a)
				break
			}
		}
	}
	if len(extMatched) > 0 {
		candidates = extMatched
	}

	var out []Asset
	for _, a := range candidates {
		out = append(out, a.asset())
	}
	return out
}

func isNonWindowsAsset(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range nonWindowsExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	for _, sub := range nonWindowsSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// ---- HTML checker ----

type htmlChecker struct {
	client *http.Client
}

// ---- HTTP header checker ----

type headerChecker struct {
	client *http.Client
}

func (h *headerChecker) check(app App) (*CheckResult, error) {
	if app.Checker.URL == "" {
		return nil, fmt.Errorf("http-header checker requires url")
	}

	req, err := http.NewRequest("HEAD", app.Checker.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http-header HEAD request returned %d", resp.StatusCode)
	}

	headerName := app.Checker.Header
	if headerName == "" {
		headerName = "Last-Modified"
	}

	val := resp.Header.Get(headerName)
	if val == "" {
		return nil, fmt.Errorf("http-header: server did not return %s header", headerName)
	}

	if strings.EqualFold(headerName, "Last-Modified") {
		t, err := http.ParseTime(val)
		if err != nil {
			return nil, fmt.Errorf("http-header: could not parse Last-Modified %q: %w", val, err)
		}
		val = t.UTC().Format(time.DateOnly)
	}

	return &CheckResult{Version: val}, nil
}

func (h *htmlChecker) check(app App) (*CheckResult, error) {
	if app.Checker.URL == "" {
		return nil, fmt.Errorf("html checker requires url")
	}
	if app.Checker.VersionRegex == "" {
		return nil, fmt.Errorf("html checker requires version_regex (not configured yet)")
	}

	body, err := h.fetch(app.Checker.URL)
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile(app.Checker.VersionRegex)
	if err != nil {
		return nil, fmt.Errorf("invalid version_regex: %w", err)
	}

	m := re.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("version_regex did not match page content")
	}

	version := string(m[0])
	if len(m) > 1 {
		version = string(m[1])
	}

	version, err = applyDateLayout(version, app.Checker.VersionDateLayout)
	if err != nil {
		return nil, err
	}
	version, err = applyVersionRewrite(version, app.Checker.VersionRewrite)
	if err != nil {
		return nil, err
	}

	// Some projects publish the version on a bare text endpoint that carries no
	// download links (e.g. gyan.dev's release-version file); asset_page points
	// at the real download page so assets can still be discovered.
	assetPage := app.Checker.AssetPage
	assetBody := body
	if assetPage == "" {
		assetPage = app.Checker.URL
	} else if len(app.Checker.AssetPatterns) > 0 {
		assetBody, err = h.fetch(assetPage)
		if err != nil {
			return nil, err
		}
	}

	assets := extractHTMLAssets(assetBody, assetPage, app.Checker.AssetPatterns)
	assets = sortAndLimitAssets(assets, app.Checker.AssetSort)
	return &CheckResult{Version: version, Assets: assets}, nil
}

// fetch GETs pageURL and returns its body.
func (h *htmlChecker) fetch(pageURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Without this, an error page (e.g. a Cloudflare 5xx while the site is
	// down) is read as if it were the real page and surfaces as a misleading
	// "version_regex did not match page content".
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("html checker: %s returned HTTP %d", pageURL, resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

var reHref = regexp.MustCompile(`(?i)href=(?:"([^"]+)"|'([^']+)')`)

// extractHTMLAssets finds all href links in body whose filename matches any of
// the given patterns, resolving relative URLs against pageURL.
func extractHTMLAssets(body []byte, pageURL string, patterns []string) []Asset {
	if len(patterns) == 0 {
		return nil
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		base = nil
	}
	var out []Asset
	seen := map[string]bool{}
	for _, m := range reHref.FindAllSubmatch(body, -1) {
		href := string(m[1])
		if href == "" {
			href = string(m[2])
		}
		name := hrefFileName(href)
		matched := false
		for _, pat := range patterns {
			if matchAssetPattern(pat, name) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		assetURL := href
		if base != nil {
			if ref, err := url.Parse(href); err == nil {
				assetURL = base.ResolveReference(ref).String()
			}
		}
		// Keyed by name, not URL: pages often link the same file from several
		// paths, and assets are saved by name so a second copy would only
		// overwrite the first.
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Asset{Name: name, URL: assetURL})
	}
	return out
}

// hrefFileName returns the filename an href points at. SourceForge mirror links
// end in "/download" (e.g. .../files/app-1.2.zip/download), so the real name is
// the segment before it.
func hrefFileName(href string) string {
	p := href
	if u, err := url.Parse(href); err == nil {
		p = u.Path
	}
	p = strings.TrimSuffix(p, "/")
	if path.Base(p) == "download" {
		p = path.Dir(p)
	}
	return path.Base(p)
}
