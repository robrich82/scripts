package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
)

type sfChecker struct {
	client *http.Client
}

type sfRSSFeed struct {
	Items []sfRSSItem `xml:"channel>item"`
}

type sfRSSItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

func (s *sfChecker) check(app App) (*CheckResult, error) {
	if app.Checker.Project == "" {
		return nil, fmt.Errorf("sourceforge checker requires project")
	}
	if app.Checker.VersionRegex == "" {
		return nil, fmt.Errorf("sourceforge checker requires version_regex")
	}

	rssURL := fmt.Sprintf("https://sourceforge.net/projects/%s/rss", app.Checker.Project)
	if app.Checker.SFPath != "" {
		rssURL += "?path=" + app.Checker.SFPath
	}

	req, err := http.NewRequest("GET", rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("SourceForge RSS %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var feed sfRSSFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse SourceForge RSS: %w", err)
	}

	re, err := regexp.Compile(app.Checker.VersionRegex)
	if err != nil {
		return nil, fmt.Errorf("invalid version_regex: %w", err)
	}

	// Scan all RSS items for version matches, keeping the highest version.
	bestVer := ""
	var bestItems []sfRSSItem

	for _, item := range feed.Items {
		m := re.FindStringSubmatch(item.Title)
		if m == nil {
			continue
		}
		ver := m[0]
		if len(m) > 1 {
			ver = m[1]
		}
		cmp := CompareVersions(ver, bestVer)
		if cmp > 0 {
			bestVer = ver
			bestItems = []sfRSSItem{item}
		} else if cmp == 0 && bestVer != "" {
			bestItems = append(bestItems, item)
		}
	}

	if bestVer == "" {
		return nil, fmt.Errorf("no release matching version_regex found in SourceForge RSS for %s", app.Checker.Project)
	}

	bestVer, err = applyVersionRewrite(bestVer, app.Checker.VersionRewrite)
	if err != nil {
		return nil, err
	}

	// Build asset list from matched items.
	patterns := app.Checker.AssetPatterns
	var assets []Asset
	depth := map[string]int{} // asset name -> path depth of the item kept for it
	for _, item := range bestItems {
		name := path.Base(item.Title)
		if !matchesAnyAssetPattern(patterns, name) {
			continue
		}
		// One file can sit in several folders (e.g. Windows/ and
		// Windows/old_platforms/); keep the shallowest so only one is downloaded.
		d := strings.Count(item.Title, "/")
		prev, seen := depth[name]
		if seen && d >= prev {
			continue
		}
		depth[name] = d
		asset := Asset{Name: name, URL: item.Link}
		if i := slices.IndexFunc(assets, func(a Asset) bool { return a.Name == name }); i >= 0 {
			assets[i] = asset
		} else {
			assets = append(assets, asset)
		}
	}

	return &CheckResult{Version: bestVer, Assets: assets}, nil
}

// matchesAnyAssetPattern reports whether name matches one of patterns; no patterns matches everything.
func matchesAnyAssetPattern(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	return slices.ContainsFunc(patterns, func(pat string) bool { return matchAssetPattern(pat, name) })
}
