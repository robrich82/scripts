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

// leafChildren matches the leaf elements mkvextract may write inside a
// <Simple> besides <Name> (<String>, <TagLanguageIETF>, <TagDefault>, …).
const leafChildren = `(?:\s*<\w+>[^<]*</\w+>)*`

// simpleElementRe returns a pattern for a leaf <Simple> element called name,
// in the compact form buildMovieTagXML writes or the indented form mkvextract
// writes (which adds <TagLanguageIETF> after the value).
func simpleElementRe(name string) string {
	return `<Simple>\s*<Name>\s*` + name + `\s*</Name>` + leafChildren + `\s*</Simple>`
}

// debandElementRe matches a DEBAND Simple element in Matroska tag XML.
var debandElementRe = regexp.MustCompile(simpleElementRe("DEBAND"))

// countryRatingElementRe matches the COUNTRY Simple element with its nested
// LAW_RATING.
var countryRatingElementRe = regexp.MustCompile(`<Simple>\s*<Name>\s*COUNTRY\s*</Name>` + leafChildren +
	`\s*` + simpleElementRe("LAW_RATING") + leafChildren + `\s*</Simple>`)

// managedElementRes match every element buildMovieTagXML writes, so a merge
// can replace them without touching anything else in the file-level tags.
var managedElementRes = []*regexp.Regexp{
	regexp.MustCompile(simpleElementRe(`(?:DATE|YEAR|SUMMARY|TMDB_ID)`)),
	countryRatingElementRe,
	debandElementRe,
}

// updateDebandInXML adds or removes the DEBAND tag element from a Matroska
// tags XML string. If deband is false the element is removed; if true it is
// inserted into the file-level tag block. When the XML has no <Tags> document
// and deband is true, a minimal tag block is returned.
func updateDebandInXML(tagsXML string, deband bool) string {
	updated := debandElementRe.ReplaceAllString(tagsXML, "")

	if !deband {
		return updated
	}

	const debandEl = "<Simple><Name>DEBAND</Name><String>true</String></Simple>"

	if xml, ok := insertFileLevelElements(updated, debandEl); ok {
		return xml
	}

	// No existing tag block — build minimal XML from scratch.
	return buildMovieTagXML("", "", "", RatingNone, true)
}

// tagBlockRe matches one <Tag> element; Matroska tags don't nest <Tag>.
var tagBlockRe = regexp.MustCompile(`(?s)<Tag>.*?</Tag>`)

// targetsRe matches a <Tag>'s <Targets>, capturing its contents.
var targetsRe = regexp.MustCompile(`(?s)<Targets\s*/>|<Targets>(.*?)</Targets>`)

var targetTypeValueRe = regexp.MustCompile(`<TargetTypeValue>\s*(\d+)\s*</TargetTypeValue>`)

// isFileLevelTag reports whether a <Tag> block applies to the whole file: no
// track, edition, chapter or attachment UID, and TargetTypeValue 50 or none.
// mkvextract omits the default 50 and writes <Targets />.
func isFileLevelTag(block string) bool {
	m := targetsRe.FindStringSubmatch(block)
	if m == nil {
		return true
	}
	if strings.Contains(m[1], "UID>") {
		return false
	}
	v := targetTypeValueRe.FindStringSubmatch(m[1])
	return v == nil || v[1] == "50"
}

// fileLevelTagCloseIndex returns the index of the </Tag> that closes the
// first file-level tag block, or -1 if there is none.
func fileLevelTagCloseIndex(xml string) int {
	for _, loc := range tagBlockRe.FindAllStringIndex(xml, -1) {
		if isFileLevelTag(xml[loc[0]:loc[1]]) {
			return loc[1] - len("</Tag>")
		}
	}
	return -1
}

// insertFileLevelElements adds els to the first file-level tag block of
// tagsXML. Inserting before the last </Tag> instead could nest them in a
// per-track statistics tag, where Plex doesn't read them. With no file-level
// block but other tags present, a new block is added before </Tags>. Returns
// false when tagsXML has no <Tags> document at all.
func insertFileLevelElements(tagsXML, els string) (string, bool) {
	if pos := fileLevelTagCloseIndex(tagsXML); pos >= 0 {
		return tagsXML[:pos] + els + tagsXML[pos:], true
	}
	if idx := strings.LastIndex(tagsXML, "</Tags>"); idx >= 0 {
		newTag := "<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>" + els + "</Tag>"
		return tagsXML[:idx] + newTag + tagsXML[idx:], true
	}
	return "", false
}

// mergeMovieTags combines the file's existing tag XML (from mkvextract,
// possibly empty) with a freshly built document (buildMovieTagXML output). The
// elements this tool manages are removed from the file-level blocks and the
// fresh ones inserted, so per-track tags such as mkvmerge's _STATISTICS_* and
// file-level tags from other tools survive. Applying fresh alone with
// "--tags all:" would delete every tag it doesn't contain.
func mergeMovieTags(existing, fresh string) string {
	fresh = strings.TrimSpace(fresh)
	if fresh == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" || !strings.Contains(existing, "<Tags") {
		return fresh
	}
	open := strings.Index(fresh, "</Targets>")
	closeIdx := strings.LastIndex(fresh, "</Tag>")
	if open < 0 || closeIdx < 0 {
		return fresh
	}
	freshEls := fresh[open+len("</Targets>") : closeIdx]

	stripped := tagBlockRe.ReplaceAllStringFunc(existing, func(block string) string {
		if !isFileLevelTag(block) {
			return block
		}
		for _, re := range managedElementRes {
			block = re.ReplaceAllString(block, "")
		}
		return block
	})
	if merged, ok := insertFileLevelElements(stripped, freshEls); ok {
		return merged
	}
	return fresh
}
