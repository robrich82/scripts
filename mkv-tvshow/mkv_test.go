package main

import (
	"strings"
	"testing"
)

// fakeMKVRunner is a test double for MKVRunner that records calls and returns
// pre-configured responses.
type fakeMKVRunner struct {
	avail       bool
	attachments AttachmentInfo
	probeErr    error
	applyResult string
	applyRC     int
	applyErr    error
	calls       []applyCall
	tagsPath    string
	tagsErr     error
	posterPath  string
	posterOK    bool
	cleaned     []string
}

type applyCall struct {
	filePath string
	args     []string
}

func (f *fakeMKVRunner) Available() bool { return f.avail }

func (f *fakeMKVRunner) ProbeAttachments(_ string) (AttachmentInfo, error) {
	return f.attachments, f.probeErr
}

func (f *fakeMKVRunner) Apply(filePath string, extraArgs []string) (string, int, error) {
	// Make a copy of the slice so the caller can't mutate our recorded data.
	argsCopy := make([]string, len(extraArgs))
	copy(argsCopy, extraArgs)
	f.calls = append(f.calls, applyCall{filePath, argsCopy})
	return f.applyResult, f.applyRC, f.applyErr
}

func (f *fakeMKVRunner) WriteTempTags(_ string) (string, error) {
	return f.tagsPath, f.tagsErr
}

func (f *fakeMKVRunner) DownloadPoster(_ string) (string, bool) {
	return f.posterPath, f.posterOK
}

func (f *fakeMKVRunner) Cleanup(paths ...string) {
	f.cleaned = append(f.cleaned, paths...)
}

// --- buildTagXML tests ---

func TestBuildTagXML_WithDate(t *testing.T) {
	xml := buildTagXML("", "", "2020-04-01T00:00:00Z", RatingNone)
	if !strings.Contains(xml, "<Simple><Name>DATE</Name><String>2020-04-01</String></Simple>") {
		t.Errorf("missing DATE node in: %s", xml)
	}
	if !strings.Contains(xml, "<Simple><Name>YEAR</Name><String>2020</String></Simple>") {
		t.Errorf("missing YEAR node in: %s", xml)
	}
}

func TestBuildTagXML_WithSummary(t *testing.T) {
	xml := buildTagXML("Episode summary", "", "", RatingNone)
	if !strings.Contains(xml, "<Simple><Name>SUMMARY</Name><String>Episode summary</String></Simple>") {
		t.Errorf("missing SUMMARY node in: %s", xml)
	}
}

func TestBuildTagXML_WithTMDBID(t *testing.T) {
	xml := buildTagXML("", "12345", "", RatingNone)
	if !strings.Contains(xml, "<Simple><Name>TMDB_ID</Name><String>12345</String></Simple>") {
		t.Errorf("missing TMDB_ID node in: %s", xml)
	}
}

func TestBuildTagXML_NoDate_ExcludesDateNodes(t *testing.T) {
	xml := buildTagXML("summary", "id", "", RatingNone)
	if strings.Contains(xml, "DATE") || strings.Contains(xml, "YEAR") {
		t.Errorf("unexpected DATE/YEAR in: %s", xml)
	}
}

func TestBuildTagXML_AllEmpty_ReturnsEmpty(t *testing.T) {
	xml := buildTagXML("", "", "", RatingNone)
	if xml != "" {
		t.Errorf("expected empty string, got: %s", xml)
	}
}

func TestBuildTagXML_WrappedInTagsElement(t *testing.T) {
	xml := buildTagXML("s", "", "", RatingNone)
	if !strings.HasPrefix(xml, "<Tags>") || !strings.HasSuffix(xml, "</Tags>") {
		t.Errorf("expected <Tags>...</Tags> wrapper, got: %s", xml)
	}
}

func TestBuildTagXML_WithRating_IncludesLAWRatingNestedInCountry(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingR16)
	// LAW_RATING must be nested *inside* COUNTRY (COUNTRY is a nesting tag per Matroska spec).
	want := "<Simple><Name>COUNTRY</Name><String>NZ</String>" +
		"<Simple><Name>LAW_RATING</Name><String>R16</String></Simple></Simple>"
	if !strings.Contains(xml, want) {
		t.Errorf("expected nested COUNTRY>LAW_RATING in: %s", xml)
	}
}

func TestBuildTagXML_NoRating_OmitsLAWRating(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingNone)
	if strings.Contains(xml, "LAW_RATING") || strings.Contains(xml, "COUNTRY") {
		t.Errorf("unexpected LAW_RATING/COUNTRY in: %s", xml)
	}
}
