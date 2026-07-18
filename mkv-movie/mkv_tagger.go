package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// MKVTagger abstracts tag extraction and application for the deband-only workflow.
type MKVTagger interface {
	Available() bool
	ExtractTags(filePath string) (string, error)
	WriteTempTags(xml string) (string, error)
	ApplyTags(filePath, tagsPath string) error
	Cleanup(paths ...string)
}

type realMKVTagger struct {
	mkvpropedit string
	mkvextract  string
}

func newRealMKVTagger() *realMKVTagger {
	r := &realMKVTagger{}
	r.mkvpropedit, _ = exec.LookPath("mkvpropedit")
	r.mkvextract, _ = exec.LookPath("mkvextract")
	return r
}

func (r *realMKVTagger) Available() bool {
	return r.mkvpropedit != "" && r.mkvextract != ""
}

func (r *realMKVTagger) ExtractTags(filePath string) (string, error) {
	if r.mkvextract == "" {
		return "", fmt.Errorf("mkvextract not found in PATH")
	}
	return extractMKVTags(r.mkvextract, filePath)
}

// extractMKVTags runs mkvextract to read the current tag XML from filePath.
// Returns "" (with no error) when the file carries no tags.
func extractMKVTags(mkvextract, filePath string) (string, error) {
	f, err := os.CreateTemp("", "mkv-tags-*.xml")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)

	out, err := exec.Command(mkvextract, filePath, "tags", name).CombinedOutput()
	if err != nil {
		// Exit code 1 means warnings only.
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return "", fmt.Errorf("mkvextract: %w: %s", err, out)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *realMKVTagger) WriteTempTags(xml string) (string, error) {
	f, err := os.CreateTemp("", "mkv-movie-tags-*.xml")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(xml); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (r *realMKVTagger) ApplyTags(filePath, tagsPath string) error {
	if r.mkvpropedit == "" {
		return fmt.Errorf("mkvpropedit not found in PATH")
	}
	cmd := exec.Command(r.mkvpropedit, filePath, "--tags", "all:"+tagsPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 {
			return nil // warnings only
		}
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *realMKVTagger) Cleanup(paths ...string) {
	for _, p := range paths {
		if p != "" {
			os.Remove(p)
		}
	}
}

// debandElementRe matches a DEBAND Simple element in Matroska tag XML.
var debandElementRe = regexp.MustCompile(`(?s)<Simple>\s*<Name>\s*DEBAND\s*</Name>\s*<String>[^<]*</String>\s*</Simple>`)

// updateDebandInXML adds or removes the DEBAND tag element from a Matroska
// tags XML string. If deband is false the element is removed; if true it is
// inserted before the closing </Tag>. When the XML has no existing <Tag>
// block and deband is true, a minimal tag block is returned.
func updateDebandInXML(tagsXML string, deband bool) string {
	updated := debandElementRe.ReplaceAllString(tagsXML, "")

	if !deband {
		return updated
	}

	const debandEl = "<Simple><Name>DEBAND</Name><String>true</String></Simple>"

	// Insert into the movie-level (TargetTypeValue 50) tag block. Using the last
	// </Tag> in the document could nest DEBAND inside a per-track statistics tag
	// instead, where downstream tools reading movie-level tags wouldn't find it.
	if pos := movieTagCloseIndex(updated); pos >= 0 {
		return updated[:pos] + debandEl + updated[pos:]
	}

	// No movie-level block, but other (per-track) tags may exist — add a
	// movie-level block before </Tags> rather than discarding them.
	newTag := "<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>" + debandEl + "</Tag>"
	if idx := strings.LastIndex(updated, "</Tags>"); idx >= 0 {
		return updated[:idx] + newTag + updated[idx:]
	}

	// No existing tag block — build minimal XML from scratch.
	return buildMovieTagXML("", "", "", RatingNone, true)
}

// targetTypeValue50Re matches the movie-level target marker in Matroska tag XML.
var targetTypeValue50Re = regexp.MustCompile(`<TargetTypeValue>\s*50\s*</TargetTypeValue>`)

// movieTagCloseIndex returns the index of the </Tag> that closes the movie-level
// (TargetTypeValue 50) tag block, or -1 if there is no such block.
func movieTagCloseIndex(xml string) int {
	m := targetTypeValue50Re.FindStringIndex(xml)
	if m == nil {
		return -1
	}
	rel := strings.Index(xml[m[1]:], "</Tag>")
	if rel < 0 {
		return -1
	}
	return m[1] + rel
}

// removeMovieTagBlock strips the entire movie-level (TargetTypeValue 50) <Tag>
// block from xml, leaving any per-track tag blocks intact. Returns xml unchanged
// when there is no movie-level block.
func removeMovieTagBlock(xml string) string {
	m := targetTypeValue50Re.FindStringIndex(xml)
	if m == nil {
		return xml
	}
	open := strings.LastIndex(xml[:m[0]], "<Tag>")
	if open < 0 {
		return xml
	}
	closeRel := strings.Index(xml[m[1]:], "</Tag>")
	if closeRel < 0 {
		return xml
	}
	end := m[1] + closeRel + len("</Tag>")
	return xml[:open] + xml[end:]
}

// mergeMovieTags combines the file's existing tag XML (from mkvextract, possibly
// empty) with a freshly built tag document (buildMovieTagXML output). The tool's
// movie-level tags replace any previous movie-level block, while per-track tags
// present in existing (e.g. mkvmerge's _STATISTICS_* tags) are preserved. This
// avoids the data loss of applying the fresh document with "--tags all:", which
// would otherwise drop every tag not represented in it.
func mergeMovieTags(existing, fresh string) string {
	fresh = strings.TrimSpace(fresh)
	if fresh == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" || !strings.Contains(existing, "<Tags") {
		return fresh
	}
	open := strings.Index(fresh, "<Tags>")
	closeIdx := strings.LastIndex(fresh, "</Tags>")
	if open < 0 || closeIdx < 0 {
		return fresh
	}
	freshBlock := fresh[open+len("<Tags>") : closeIdx]

	stripped := removeMovieTagBlock(existing)
	insertAt := strings.LastIndex(stripped, "</Tags>")
	if insertAt < 0 {
		return fresh
	}
	return stripped[:insertAt] + freshBlock + stripped[insertAt:]
}
