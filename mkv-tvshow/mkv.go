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

// emptyTagsDoc is a tags document with no tags. mkvpropedit rejects an empty
// file but accepts this.
const emptyTagsDoc = "<Tags></Tags>"

// simpleTagRe matches the opening and closing tags of <Simple> elements.
var simpleTagRe = regexp.MustCompile(`</?Simple>`)

// topLevelSimples returns the [start, end) spans of the <Simple> elements that
// are direct children of a <Tag> block, skipping nested ones.
func topLevelSimples(block string) [][2]int {
	var spans [][2]int
	depth, start := 0, 0
	for _, loc := range simpleTagRe.FindAllStringIndex(block, -1) {
		if block[loc[0]+1] != '/' {
			if depth == 0 {
				start = loc[0]
			}
			depth++
			continue
		}
		if depth == 0 {
			continue // stray </Simple>
		}
		depth--
		if depth == 0 {
			spans = append(spans, [2]int{start, loc[1]})
		}
	}
	return spans
}

var (
	simpleNameRe   = regexp.MustCompile(`<Name>\s*([^<]*?)\s*</Name>`)
	simpleStringRe = regexp.MustCompile(`<String>\s*([^<]*?)\s*</String>`)
)

// simpleNameValue returns the <Name> and <String> of a <Simple> element,
// ignoring those of any <Simple> nested in it.
func simpleNameValue(el string) (name, value string) {
	head := el[len("<Simple>"):]
	if i := strings.Index(head, "<Simple>"); i >= 0 {
		head = head[:i]
	}
	if m := simpleNameRe.FindStringSubmatch(head); m != nil {
		name = m[1]
	}
	if m := simpleStringRe.FindStringSubmatch(head); m != nil {
		value = m[1]
	}
	return name, value
}

// isDebandElement reports whether a <Simple> is the DEBAND element.
func isDebandElement(name, _ string) bool { return name == "DEBAND" }

// isRatingElement reports whether a <Simple> is the NZ COUNTRY element that
// holds LAW_RATING.
func isRatingElement(name, value string) bool { return name == "COUNTRY" && value == "NZ" }

// isManagedElement reports whether a <Simple> is one this tool writes.
func isManagedElement(name, value string) bool {
	switch name {
	case "DATE", "YEAR", "SUMMARY", "TMDB_ID", "DEBAND":
		return true
	}
	return isRatingElement(name, value)
}

// removeElements deletes from every <Tag> block the top-level <Simple>
// elements that drop selects, then deletes each <Tag> left with no <Simple>,
// which mkvpropedit would reject.
func removeElements(tagsXML string, drop func(fileLevel bool, name, value string) bool) string {
	return tagBlockRe.ReplaceAllStringFunc(tagsXML, func(block string) string {
		fileLevel := isFileLevelTag(block)
		spans := topLevelSimples(block)
		kept := len(spans)
		for i := len(spans) - 1; i >= 0; i-- {
			s := spans[i]
			if name, value := simpleNameValue(block[s[0]:s[1]]); drop(fileLevel, name, value) {
				block = strings.TrimRight(block[:s[0]], " \t\r\n") + block[s[1]:]
				kept--
			}
		}
		if kept == 0 {
			return ""
		}
		return block
	})
}

// replaceElement removes every top-level element that match selects and, if
// el is not empty, adds el to the file-level tag block.
func replaceElement(tagsXML string, match func(name, value string) bool, el string) string {
	if strings.TrimSpace(tagsXML) == "" {
		tagsXML = emptyTagsDoc
	}
	updated := removeElements(tagsXML, func(_ bool, name, value string) bool { return match(name, value) })
	if el == "" {
		return updated
	}
	if xml, ok := insertFileLevelElements(updated, el); ok {
		return xml
	}
	// Not a tags document: leave it for mkvpropedit to reject.
	return updated
}

// updateDebandInXML removes the DEBAND element from a Matroska tags XML string
// and, if deband is true, adds it to the file-level tag block.
func updateDebandInXML(tagsXML string, deband bool) string {
	var el string
	if deband {
		el = "<Simple><Name>DEBAND</Name><String>true</String></Simple>"
	}
	return replaceElement(tagsXML, isDebandElement, el)
}

// updateRatingInXML removes the NZ COUNTRY/LAW_RATING element from a Matroska
// tags XML string and, unless rating is RatingNone, adds the new one to the
// file-level tag block.
func updateRatingInXML(tagsXML string, rating NZRating) string {
	var el string
	if rating != RatingNone {
		el = fmt.Sprintf(
			"<Simple><Name>COUNTRY</Name><String>NZ</String>"+
				"<Simple><Name>LAW_RATING</Name><String>%s</String></Simple></Simple>",
			rating)
	}
	return replaceElement(tagsXML, isRatingElement, el)
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
// block, a new one is added before </Tags>. Returns false when tagsXML has no
// </Tags>.
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

// mergeEpisodeTags combines the file's existing tag XML (from mkvextract, empty when
// the file has none) with a freshly built document (buildTagXML output), because
// "--tags all:" replaces every tag in the file. The tool's elements are
// removed from the file-level blocks, plus DEBAND and the NZ rating from any
// block (older versions could put them in a per-track one), and the fresh
// elements go into the first file-level block. Everything else survives, such
// as mkvmerge's per-track _STATISTICS_* tags.
func mergeEpisodeTags(existing, fresh string) (string, error) {
	fresh = strings.TrimSpace(fresh)
	if fresh == "" {
		return existing, nil
	}
	if strings.TrimSpace(existing) == "" {
		return fresh, nil
	}
	targetsEnd := strings.Index(fresh, "</Targets>")
	tagEnd := strings.LastIndex(fresh, "</Tag>")
	if targetsEnd < 0 || tagEnd < targetsEnd {
		return "", fmt.Errorf("fresh tags have no <Tag> block: %q", fresh)
	}
	freshEls := fresh[targetsEnd+len("</Targets>") : tagEnd]

	stripped := removeElements(existing, func(fileLevel bool, name, value string) bool {
		if fileLevel {
			return isManagedElement(name, value)
		}
		return isDebandElement(name, value) || isRatingElement(name, value)
	})
	merged, ok := insertFileLevelElements(stripped, freshEls)
	if !ok {
		return "", fmt.Errorf("existing tags are not a <Tags> document")
	}
	return merged, nil
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
