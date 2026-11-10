package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// MKVTagger abstracts tag extraction and application.
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

// ExtractTags returns all Matroska tags XML for filePath, per-track included,
// or "" when the file has no tags.
func (r *realMKVTagger) ExtractTags(filePath string) (string, error) {
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

// checkTagsDoc rejects XML without a closing </Tags>. mkvpropedit would apply
// a self-closed <Tags/> as "no tags" and reject a truncated document.
func checkTagsDoc(tagsXML string) error {
	if !strings.Contains(tagsXML, "</Tags>") {
		return fmt.Errorf("not a Matroska tags document: %.80q", tagsXML)
	}
	return nil
}

// simpleTagRe matches the opening and closing tags of <Simple> elements.
var simpleTagRe = regexp.MustCompile(`</?Simple>`)

// topLevelSimples returns the [start, end) spans of the outermost <Simple>
// elements in s, skipping nested ones.
func topLevelSimples(s string) [][2]int {
	var spans [][2]int
	depth, start := 0, 0
	for _, loc := range simpleTagRe.FindAllStringIndex(s, -1) {
		if s[loc[0]+1] != '/' {
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
	lawRatingRe    = regexp.MustCompile(`<Name>\s*LAW_RATING\s*</Name>`)
)

// simpleEl is a <Simple>'s own <Name> and <String>, and its nested <Simple>s.
type simpleEl struct{ name, value, nested string }

func parseSimple(el string) simpleEl {
	inner := strings.TrimSuffix(strings.TrimPrefix(el, "<Simple>"), "</Simple>")
	var own, nested strings.Builder
	last := 0
	for _, s := range topLevelSimples(inner) {
		own.WriteString(inner[last:s[0]])
		nested.WriteString(inner[s[0]:s[1]])
		last = s[1]
	}
	own.WriteString(inner[last:])
	e := simpleEl{nested: nested.String()}
	if m := simpleNameRe.FindStringSubmatch(own.String()); m != nil {
		e.name = m[1]
	}
	if m := simpleStringRe.FindStringSubmatch(own.String()); m != nil {
		e.value = m[1]
	}
	return e
}

func isDebandElement(e simpleEl) bool { return e.name == "DEBAND" }

// isRatingElement reports whether e is the NZ COUNTRY element holding LAW_RATING.
func isRatingElement(e simpleEl) bool {
	return e.name == "COUNTRY" && e.value == "NZ" && lawRatingRe.MatchString(e.nested)
}

// isManagedElement reports whether e is one this tool writes.
func isManagedElement(e simpleEl) bool {
	switch e.name {
	case "DATE", "YEAR", "SUMMARY", "TMDB_ID", "DEBAND":
		return true
	}
	return isRatingElement(e)
}

// removeElements deletes from every <Tag> block the top-level <Simple>
// elements that drop selects, and deletes a block it leaves with no <Simple>,
// which mkvpropedit would reject.
func removeElements(tagsXML string, drop func(fileLevel bool, e simpleEl) bool) string {
	return tagBlockRe.ReplaceAllStringFunc(tagsXML, func(block string) string {
		fileLevel := isFileLevelTag(block)
		spans := topLevelSimples(block)
		kept := len(spans)
		for i := len(spans) - 1; i >= 0; i-- {
			s := spans[i]
			if drop(fileLevel, parseSimple(block[s[0]:s[1]])) {
				block = strings.TrimRight(block[:s[0]], " \t\r\n") + block[s[1]:]
				kept--
			}
		}
		if kept == 0 && len(spans) > 0 {
			return ""
		}
		return block
	})
}

// replaceElement removes every top-level element that match selects and, if
// el is not empty, adds el to the file-level tag block. Blank input is treated
// as a file with no tags.
func replaceElement(tagsXML string, match func(simpleEl) bool, el string) (string, error) {
	if strings.TrimSpace(tagsXML) == "" {
		tagsXML = emptyTagsDoc
	}
	if err := checkTagsDoc(tagsXML); err != nil {
		return "", err
	}
	updated := removeElements(tagsXML, func(_ bool, e simpleEl) bool { return match(e) })
	if el == "" {
		return updated, nil
	}
	return insertFileLevelElements(updated, el), nil
}

// updateDebandInXML removes the DEBAND element from a Matroska tags XML string
// and, if deband is true, adds it to the file-level tag block.
func updateDebandInXML(tagsXML string, deband bool) (string, error) {
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

// insertFileLevelElements adds els to the first file-level tag block, or to a
// new one before </Tags>: Plex doesn't read them from a per-track block.
// tagsXML must pass checkTagsDoc.
func insertFileLevelElements(tagsXML, els string) string {
	if pos := fileLevelTagCloseIndex(tagsXML); pos >= 0 {
		return tagsXML[:pos] + els + tagsXML[pos:]
	}
	idx := strings.LastIndex(tagsXML, "</Tags>")
	return tagsXML[:idx] + "<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>" + els + "</Tag>" + tagsXML[idx:]
}

// mergeMovieTags merges fresh (buildMovieTagXML output) into existing (mkvextract output,
// "" when untagged), because "--tags all:" replaces every tag. It replaces
// this tool's file-level elements, clears DEBAND and the NZ rating from other
// blocks too, and keeps everything else.
func mergeMovieTags(existing, fresh string) (string, error) {
	fresh = strings.TrimSpace(fresh)
	if fresh == "" {
		return existing, nil
	}
	if strings.TrimSpace(existing) == "" {
		return fresh, nil
	}
	if err := checkTagsDoc(existing); err != nil {
		return "", err
	}
	targetsEnd := strings.Index(fresh, "</Targets>")
	tagEnd := strings.LastIndex(fresh, "</Tag>")
	if targetsEnd < 0 || tagEnd < targetsEnd {
		return "", fmt.Errorf("fresh tags have no <Tag> block: %q", fresh)
	}
	freshEls := fresh[targetsEnd+len("</Targets>") : tagEnd]

	stripped := removeElements(existing, func(fileLevel bool, e simpleEl) bool {
		if fileLevel {
			return isManagedElement(e)
		}
		return isDebandElement(e) || isRatingElement(e)
	})
	return insertFileLevelElements(stripped, freshEls), nil
}
