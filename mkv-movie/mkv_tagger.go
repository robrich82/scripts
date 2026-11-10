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

// mergeMovieTags combines the file's existing tag XML (from mkvextract, empty when
// the file has none) with a freshly built document (buildMovieTagXML output), because
// "--tags all:" replaces every tag in the file. The tool's elements are
// removed from the file-level blocks, plus DEBAND and the NZ rating from any
// block (older versions could put them in a per-track one), and the fresh
// elements go into the first file-level block. Everything else survives, such
// as mkvmerge's per-track _STATISTICS_* tags.
func mergeMovieTags(existing, fresh string) (string, error) {
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
