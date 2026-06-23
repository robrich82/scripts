package main

import (
	"archive/zip"
	"path"
	"regexp"
	"strings"
)

var (
	reCalibreNext  = regexp.MustCompile(`(?i)<a\s[^>]*class=["']calibreANext["'][^>]*>`)
	reHref         = regexp.MustCompile(`(?i)\bhref=["']([^"']*)["']`)
	reImgSrc       = regexp.MustCompile(`(?i)<img\s[^>]*\bsrc=["']([^"']*)["']`)
	reSvgImageHref = regexp.MustCompile(`(?i)<image\s[^>]*\bxlink:href=["']([^"']*)["']`)
)

// kfxPageOrder detects a calibre-exported KFX CBZ (root HTML + matching
// *_files/ directory containing XHTML pages) and returns all comic images in
// reading order by following the calibreANext navigation chain.
// Returns nil if the structure is not present or cannot be parsed.
func kfxPageOrder(zr *zip.Reader) []*zip.File {
	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		byName[f.Name] = f
	}

	// Find a root-level .html whose stem+"_files/" prefix exists in the zip.
	var htmlPath string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if strings.ContainsRune(name, '/') {
			continue // not at root
		}
		if !strings.EqualFold(path.Ext(name), ".html") {
			continue
		}
		stem := strings.TrimSuffix(name, path.Ext(name))
		prefix := stem + "_files/"
		for zipName := range byName {
			if strings.HasPrefix(zipName, prefix) {
				htmlPath = name
				break
			}
		}
		if htmlPath != "" {
			break
		}
	}
	if htmlPath == "" {
		return nil
	}

	htmlData, err := readZipEntry(byName[htmlPath])
	if err != nil {
		return nil
	}

	// First page is the calibreANext link from the main HTML.
	current := calibreNextLink(string(htmlData))
	if current == "" {
		return nil
	}

	var ordered []*zip.File
	seen := make(map[string]bool)

	for current != "" && !seen[current] {
		seen[current] = true
		f := byName[current]
		if f == nil || f.FileInfo().IsDir() {
			break
		}
		data, err := readZipEntry(f)
		if err != nil {
			break
		}
		body := string(data)
		dir := current[:strings.LastIndex(current, "/")+1]

		// Collect the image referenced by this XHTML page.
		if src := calibreImgSrc(body); src != "" && !strings.HasPrefix(src, "http") {
			imgPath := path.Clean(dir + src)
			if img := byName[imgPath]; img != nil && isImageFile(imgPath) {
				ordered = append(ordered, img)
			}
		}

		// Advance to next page; stop when the link leaves the HTML/XHTML chain.
		nextRel := calibreNextLink(body)
		if nextRel == "" {
			break
		}
		nextAbs := path.Clean(dir + nextRel)
		ext := strings.ToLower(path.Ext(nextAbs))
		if ext != ".xhtml" && ext != ".html" {
			break
		}
		current = nextAbs
	}

	if len(ordered) == 0 {
		return nil
	}

	// Calibre stores the book cover at *_files/cover.* — it either appears at
	// the very end of the XHTML chain or is omitted from it entirely. In both
	// cases, promote it to position 0.
	filesRoot := strings.TrimSuffix(htmlPath, path.Ext(htmlPath)) + "_files/"
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if path.Dir(f.Name)+"/" != filesRoot {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(path.Base(f.Name)), "cover.") || !isImageFile(f.Name) {
			continue
		}
		// Remove from ordered if already present (e.g. linked at end of chain).
		for i, o := range ordered {
			if o.Name == f.Name {
				ordered = append(ordered[:i], ordered[i+1:]...)
				break
			}
		}
		ordered = append([]*zip.File{f}, ordered...)
		break
	}

	return ordered
}

// calibreNextLink returns the href of the first <a class="calibreANext"> in s.
func calibreNextLink(s string) string {
	m := reCalibreNext.FindStringIndex(s)
	if m == nil {
		return ""
	}
	hm := reHref.FindStringSubmatch(s[m[0]:m[1]])
	if hm == nil {
		return ""
	}
	return hm[1]
}

// calibreImgSrc returns the image path from the first <img src> or SVG
// <image xlink:href> element in s.
func calibreImgSrc(s string) string {
	if m := reImgSrc.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := reSvgImageHref.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
