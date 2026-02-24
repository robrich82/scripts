package main

import (
	"os/exec"
	"regexp"
	"strings"
)

var dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// MediaInfoReader queries an MKV file for its date tags.
type MediaInfoReader interface {
	// Query returns the best available date string and the internal header date.
	// Both strings are either "YYYY-MM-DD", "YYYY", or empty.
	Query(filePath string) (bestDate, headerDate string, err error)
}

// realMediaInfoReader calls the mediainfo CLI.
type realMediaInfoReader struct {
	path string
}

func newRealMediaInfoReader() (*realMediaInfoReader, error) {
	path, err := exec.LookPath("mediainfo")
	if err != nil {
		return nil, err
	}
	return &realMediaInfoReader{path: path}, nil
}

func (r *realMediaInfoReader) Query(filePath string) (bestDate, headerDate string, err error) {
	out, err := exec.Command(r.path, "--Inform=General;%DATE%|%YEAR%|%Encoded_Date%", filePath).Output()
	if err != nil {
		return "", "", err
	}
	return parseMediaInfoOutput(strings.TrimSpace(string(out)))
}

// parseMediaInfoOutput splits the pipe-delimited mediainfo output and applies
// date priority: DATE tag > YEAR tag > Encoded_Date header.
func parseMediaInfoOutput(raw string) (bestDate, headerDate string, err error) {
	parts := strings.SplitN(raw, "|", 3)

	tagDate := ""
	if len(parts) > 0 {
		tagDate = strings.TrimSpace(parts[0])
	}
	tagYear := ""
	if len(parts) > 1 {
		tagYear = strings.TrimSpace(parts[1])
	}
	encDate := ""
	if len(parts) > 2 {
		encDate = strings.TrimSpace(parts[2])
	}

	// Encoded_Date format: "UTC 2016-09-12 04:35:11" — extract YYYY-MM-DD.
	if encDate != "" {
		if m := dateRe.FindString(encDate); m != "" {
			headerDate = m
		}
	}

	// Priority: DATE tag > YEAR tag > header date.
	bestDate = tagDate
	if bestDate == "" {
		bestDate = tagYear
	}
	if bestDate == "" {
		bestDate = headerDate
	}

	return bestDate, headerDate, nil
}
