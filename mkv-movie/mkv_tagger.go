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

	if idx := strings.LastIndex(updated, "</Tag>"); idx >= 0 {
		return updated[:idx] + debandEl + updated[idx:]
	}

	// No existing tag block — build minimal XML from scratch.
	return buildMovieTagXML("", "", "", RatingNone, true)
}
