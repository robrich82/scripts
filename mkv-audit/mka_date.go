package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// mkaTagsDoc decodes the XML produced by: mkvextract <file> tags <output>
type mkaTagsDoc struct {
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

// readTagDateFromMKA extracts the DATE_RECORDED tag from an MKA (or MKV) file's
// Matroska tags section using mkvextract. Returns the date and true on success.
func readTagDateFromMKA(mkvextractPath, path string) (time.Time, bool) {
	tmp, err := os.CreateTemp("", "mkv-audit-tags-*.xml")
	if err != nil {
		return time.Time{}, false
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.Command(mkvextractPath, path, "tags", tmp.Name())
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return time.Time{}, false
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil || len(data) == 0 {
		return time.Time{}, false
	}

	var doc mkaTagsDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return time.Time{}, false
	}

	for _, tag := range doc.Tags {
		tv := tag.Targets.TargetTypeValue
		if tv != "" && tv != "50" {
			continue
		}
		for _, s := range tag.Simple {
			if strings.ToUpper(s.Name) == "DATE_RECORDED" && s.String != "" {
				if dt, err := parseMKATagDate(s.String); err == nil {
					return dt, true
				}
			}
		}
	}
	return time.Time{}, false
}

func parseMKATagDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && s[4] == ':' && s[7] == ':' {
		s = s[:4] + "-" + s[5:7] + "-" + s[8:]
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date: %q", s)
}
