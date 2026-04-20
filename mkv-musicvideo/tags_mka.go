package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
	"strconv"
	"strings"
)

// mkvTagsDocument decodes the XML produced by: mkvextract <file> tags <output>
// mkvextract always includes a DOCTYPE declaration; Go's encoding/xml skips it.
type mkvTagsDocument struct {
	XMLName xml.Name `xml:"Tags"`
	Tags    []struct {
		Targets struct {
			TargetTypeValue string `xml:"TargetTypeValue"`
		} `xml:"Targets"`
		Simple []struct {
			Name   string `xml:"Name"`
			String string `xml:"String"`
		} `xml:"Simple"`
	} `xml:"Tag"`
}

// parseMKVTagsXML reads a Matroska tags XML file and returns two maps:
//   - level50: album-level tags (TargetTypeValue=50, or absent which defaults to 50)
//   - level30: track-level tags (TargetTypeValue=30)
//
// Keys are upper-case tag names (TITLE, ARTIST, …).
// Missing or empty files return two empty maps without error.
func parseMKVTagsXML(xmlPath string) (level50, level30 map[string]string) {
	level50 = make(map[string]string)
	level30 = make(map[string]string)

	data, err := os.ReadFile(xmlPath)
	if err != nil || len(data) == 0 {
		return
	}

	var doc mkvTagsDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return
	}

	for _, tag := range doc.Tags {
		target := level50 // Matroska default TargetTypeValue is 50
		if tag.Targets.TargetTypeValue == "30" {
			target = level30
		}
		for _, s := range tag.Simple {
			name := strings.ToUpper(s.Name)
			if target[name] == "" { // first value wins
				target[name] = s.String
			}
		}
	}
	return
}

// mkvmergeIdentify is the subset of `mkvmerge --identification-format json`
// output needed to discover image attachments and the file header title.
type mkvmergeIdentify struct {
	Container struct {
		Properties struct {
			Title   string `json:"title"`
			DateUTC *int64 `json:"date_utc"`
		} `json:"properties"`
	} `json:"container"`
	Attachments []struct {
		ID          int    `json:"id"`
		ContentType string `json:"content_type"`
	} `json:"attachments"`
}

// readMKVHeaderTitle returns the file-header title stored in an MKV, or ""
// when the title is absent or the identify call fails.
func readMKVHeaderTitle(mkvmergePath, mkvPath string) string {
	var buf bytes.Buffer
	cmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkvPath)
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return ""
	}
	var info mkvmergeIdentify
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		return ""
	}
	return info.Container.Properties.Title
}

// readMKVHeaderDate returns the date stored in the MKV container header.
// date_utc from mkvmerge JSON is a Unix timestamp in seconds (UTC).
// Returns zero time and false when absent or on error.
func readMKVHeaderDate(mkvmergePath, mkvPath string) (time.Time, bool) {
	var buf bytes.Buffer
	cmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkvPath)
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return time.Time{}, false
	}
	var info mkvmergeIdentify
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		return time.Time{}, false
	}
	if info.Container.Properties.DateUTC == nil {
		return time.Time{}, false
	}
	return time.Unix(*info.Container.Properties.DateUTC, 0).UTC(), true
}

// extractMKACover finds the first image attachment in mkaPath and returns its
// raw bytes and MIME type.  Returns nil, "" when no image attachment exists.
func extractMKACover(mkvextractPath, mkvmergePath, mkaPath string) ([]byte, string) {
	var identBuf bytes.Buffer
	idCmd := exec.Command(mkvmergePath, "--identify", "--identification-format", "json", mkaPath)
	idCmd.Stdout = &identBuf
	idCmd.Stderr = io.Discard
	if idCmd.Run() != nil {
		return nil, ""
	}

	var info mkvmergeIdentify
	if err := json.Unmarshal(identBuf.Bytes(), &info); err != nil {
		return nil, ""
	}

	for _, att := range info.Attachments {
		if !strings.HasPrefix(att.ContentType, "image/") {
			continue
		}
		tmp, err := os.CreateTemp("", "mka-cover-*")
		if err != nil {
			continue
		}
		tmp.Close()
		defer os.Remove(tmp.Name())

		extract := exec.Command(mkvextractPath, mkaPath, "attachments",
			fmt.Sprintf("%d:%s", att.ID, tmp.Name()))
		extract.Stdout = io.Discard
		extract.Stderr = io.Discard
		if extract.Run() != nil {
			continue
		}
		data, err := os.ReadFile(tmp.Name())
		if err == nil && len(data) > 0 {
			return data, att.ContentType
		}
	}
	return nil, ""
}

// readAudioTagsFromMKA reads metadata from a Matroska Audio (.mka) file using
// mkvextract (for global tags) and mkvmerge (for attachment discovery).
// dhowden/tag does not support the Matroska container format.
func readAudioTagsFromMKA(path, mkvextractPath, mkvmergePath string) (AudioTags, error) {
	xmlTmp, err := os.CreateTemp("", "mka-tags-*.xml")
	if err != nil {
		return AudioTags{}, fmt.Errorf("create temp tags file: %w", err)
	}
	xmlTmp.Close()
	defer os.Remove(xmlTmp.Name())

	// mkvextract exits non-zero when the file has no tags — tolerated.
	cmd := exec.Command(mkvextractPath, path, "tags", xmlTmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Run() //nolint:errcheck

	lvl50, lvl30 := parseMKVTagsXML(xmlTmp.Name())

	var trackNum, year int
	if n, err := strconv.Atoi(lvl30["PART_NUMBER"]); err == nil {
		trackNum = n
	}
	if y, err := strconv.Atoi(lvl50["DATE_RECORDED"]); err == nil {
		year = y
	}

	coverData, coverMIME := extractMKACover(mkvextractPath, mkvmergePath, path)

	return AudioTags{
		Title:       lvl30["TITLE"],
		Artist:      lvl30["ARTIST"],
		AlbumArtist: lvl50["ARTIST"],
		Album:       lvl50["TITLE"],
		TrackNum:    trackNum,
		Year:        year,
		Genre:       lvl50["GENRE"],
		Comment:     lvl50["COMMENT"],
		CoverData:   coverData,
		CoverMIME:   coverMIME,
	}, nil
}
