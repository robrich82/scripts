package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// AttachmentInfo describes which image types are already attached to an MKV file.
type AttachmentInfo struct {
	HasJPEG bool
	HasPNG  bool
}

// MKVRunner abstracts mkvpropedit and mkvmerge subprocess calls so that the
// processor logic can be tested without invoking real binaries.
type MKVRunner interface {
	// Available returns true if mkvpropedit was found in PATH.
	Available() bool
	// ProbeAttachments inspects an MKV file for existing image attachments.
	ProbeAttachments(filePath string) (AttachmentInfo, error)
	// Apply runs mkvpropedit on filePath with the given extra arguments.
	// Returns combined stdout+stderr, the process exit code, and any exec error.
	Apply(filePath string, extraArgs []string) (combined string, rc int, err error)
	// WriteTempTags writes XML to a temp file and returns its path.
	WriteTempTags(xml string) (string, error)
	// DownloadPoster fetches a poster image to a temp file. Returns the temp path
	// and true on success, or ("", false) on any failure.
	DownloadPoster(url string) (string, bool)
	// Cleanup removes the given temp file paths, ignoring errors.
	Cleanup(paths ...string)
	// ExtractTags returns the global Matroska tags XML for filePath.
	// Returns ("", nil) when the file has no tags.
	ExtractTags(filePath string) (string, error)
}

// realMKVRunner is the production MKVRunner that calls mkvpropedit and mkvmerge.
type realMKVRunner struct {
	mkvpropedit string // path found via exec.LookPath
	mkvmerge    string // path found via exec.LookPath (empty if not found)
	mkvextract  string // path found via exec.LookPath (empty if not found)
}

func newRealMKVRunner() *realMKVRunner {
	r := &realMKVRunner{}
	r.mkvpropedit, _ = exec.LookPath("mkvpropedit")
	r.mkvmerge, _ = exec.LookPath("mkvmerge")
	r.mkvextract, _ = exec.LookPath("mkvextract")
	return r
}

func (r *realMKVRunner) Available() bool {
	return r.mkvpropedit != ""
}

func (r *realMKVRunner) ProbeAttachments(filePath string) (AttachmentInfo, error) {
	if r.mkvmerge == "" {
		return AttachmentInfo{}, nil
	}
	out, err := exec.Command(r.mkvmerge, "-i", filePath).CombinedOutput()
	if err != nil {
		// mkvmerge may exit non-zero for warnings; treat output as valid
		if len(out) == 0 {
			return AttachmentInfo{}, err
		}
	}
	combined := string(out)
	return AttachmentInfo{
		HasJPEG: strings.Contains(combined, "image/jpeg") || strings.Contains(combined, "type 'image/jpeg'"),
		HasPNG:  strings.Contains(combined, "image/png") || strings.Contains(combined, "type 'image/png'"),
	}, nil
}

func (r *realMKVRunner) Apply(filePath string, extraArgs []string) (string, int, error) {
	args := append([]string{filePath}, extraArgs...)
	cmd := exec.Command(r.mkvpropedit, args...)
	out, err := cmd.CombinedOutput()
	combined := string(out)
	rc := 0
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	// exec.Command returns an *exec.ExitError for non-zero exits; that is not an
	// "exec error" for our purposes — the caller inspects rc and combined.
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			return combined, rc, err
		}
	}
	return combined, rc, nil
}

func (r *realMKVRunner) WriteTempTags(xml string) (string, error) {
	f, err := os.CreateTemp("", "mkv-tags-*.xml")
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

func (r *realMKVRunner) DownloadPoster(posterURL string) (string, bool) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(posterURL)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return "", false
	}
	defer resp.Body.Close()

	f, err := os.CreateTemp("", "mkv-poster-*.jpg")
	if err != nil {
		return "", false
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", false
	}
	return f.Name(), true
}

func (r *realMKVRunner) Cleanup(paths ...string) {
	for _, p := range paths {
		if p != "" {
			os.Remove(p)
		}
	}
}

func (r *realMKVRunner) ExtractTags(filePath string) (string, error) {
	if r.mkvextract == "" {
		return "", fmt.Errorf("mkvextract not found in PATH")
	}
	f, err := os.CreateTemp("", "mkv-tags-*.xml")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)

	out, err := exec.Command(r.mkvextract, filePath, "tags", name).CombinedOutput()
	if err != nil {
		// Exit code 1 means warnings only — check output for actual errors.
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

// leafChildren matches the leaf elements mkvextract may write inside a
// <Simple> besides <Name> (<String>, <TagLanguageIETF>, <TagDefault>, …).
const leafChildren = `(?:\s*<\w+>[^<]*</\w+>)*`

// simpleElementRe returns a pattern for a leaf <Simple> element called name,
// in the compact form buildTagXML writes or the indented form mkvextract
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

// managedElementRes match every element buildTagXML writes, so a merge can
// replace them without touching anything else in the file-level tags.
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

	// No tag block present — build minimal XML from scratch.
	return buildTagXML("", "", "", RatingNone, true)
}

// updateRatingInXML adds or replaces the COUNTRY/LAW_RATING nested tag element
// in a Matroska tags XML string. If rating is RatingNone the element is removed;
// otherwise it is inserted into the file-level tag block. All other tag
// elements (DEBAND, SUMMARY, TMDB_ID, etc.) are preserved. When the XML has no
// <Tags> document and rating is non-empty, a minimal tag block is returned.
func updateRatingInXML(tagsXML string, rating NZRating) string {
	updated := countryRatingElementRe.ReplaceAllString(tagsXML, "")

	if rating == RatingNone {
		return updated
	}

	ratingEl := fmt.Sprintf(
		"<Simple><Name>COUNTRY</Name><String>NZ</String>"+
			"<Simple><Name>LAW_RATING</Name><String>%s</String></Simple></Simple>",
		rating)

	if xml, ok := insertFileLevelElements(updated, ratingEl); ok {
		return xml
	}

	// No tag block present — build minimal XML from scratch.
	return buildTagXML("", "", "", rating, false)
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

// mergeEpisodeTags combines the file's existing tag XML (from mkvextract,
// possibly empty) with a freshly built document (buildTagXML output). The
// elements this tool manages are removed from the file-level blocks and the
// fresh ones inserted, so per-track tags such as mkvmerge's _STATISTICS_* and
// file-level tags from other tools survive. Applying fresh alone with
// "--tags all:" would delete every tag it doesn't contain.
func mergeEpisodeTags(existing, fresh string) string {
	fresh = strings.TrimSpace(fresh)
	if fresh == "" || strings.TrimSpace(existing) == "" || !strings.Contains(existing, "<Tags") {
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

// xmlEscape replaces XML special characters so they are safe inside tag values.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// buildTagXML constructs the Matroska tags XML string for the given metadata.
// Returns an empty string if there is nothing to tag.
func buildTagXML(summary, tmdbID, dateStr string, rating NZRating, deband bool) string {
	var elements []string

	if dateStr != "" {
		datePart := strings.SplitN(dateStr, "T", 2)[0]
		yearPart := strings.SplitN(dateStr, "-", 2)[0]
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>DATE</Name><String>%s</String></Simple>", datePart),
			fmt.Sprintf("<Simple><Name>YEAR</Name><String>%s</String></Simple>", yearPart),
		)
	}
	if summary != "" {
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>SUMMARY</Name><String>%s</String></Simple>", xmlEscape(summary)),
		)
	}
	if tmdbID != "" {
		elements = append(elements,
			fmt.Sprintf("<Simple><Name>TMDB_ID</Name><String>%s</String></Simple>", tmdbID),
		)
	}
	if rating != RatingNone {
		// COUNTRY is a nesting tag; LAW_RATING must be nested inside it.
		elements = append(elements, fmt.Sprintf(
			"<Simple><Name>COUNTRY</Name><String>NZ</String>"+
				"<Simple><Name>LAW_RATING</Name><String>%s</String></Simple></Simple>",
			rating))
	}
	if deband {
		elements = append(elements,
			"<Simple><Name>DEBAND</Name><String>true</String></Simple>",
		)
	}

	if len(elements) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>%s</Tag></Tags>",
		strings.Join(elements, ""),
	)
}
