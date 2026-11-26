package main

import (
	"strings"
	"testing"
)

// fakeMKVRunner is a test double for MKVRunner that records calls and returns
// pre-configured responses.
type fakeMKVRunner struct {
	avail          bool
	attachments    AttachmentInfo
	probeErr       error
	applyResult    string
	applyRC        int
	applyErr       error
	calls          []applyCall
	tagsPath       string
	tagsErr        error
	posterPath     string
	posterErr      error
	cleaned        []string
	extractTagsXML string
	extractTagsErr error
	writtenXML     []string
	titles         map[string]string // ReadTitle's answer by path; nil → errNoMKVMerge
	readTitleErr   error
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

func (f *fakeMKVRunner) WriteTempTags(xml string) (string, error) {
	f.writtenXML = append(f.writtenXML, xml)
	return f.tagsPath, f.tagsErr
}

func (f *fakeMKVRunner) DownloadPoster(_ string) (string, error) {
	return f.posterPath, f.posterErr
}

func (f *fakeMKVRunner) Cleanup(paths ...string) {
	f.cleaned = append(f.cleaned, paths...)
}

func (f *fakeMKVRunner) ExtractTags(_ string) (string, error) {
	return f.extractTagsXML, f.extractTagsErr
}

func (f *fakeMKVRunner) ReadTitle(filePath string) (string, error) {
	switch {
	case f.readTitleErr != nil:
		return "", f.readTitleErr
	case f.titles == nil:
		return "", errNoMKVMerge
	}
	return f.titles[filePath], nil
}

// --- buildTagXML tests ---

func TestBuildTagXML_WithDate(t *testing.T) {
	xml := buildTagXML("", "", "2020-04-01T00:00:00Z", RatingNone, false)
	if !strings.Contains(xml, "<Simple><Name>DATE</Name><String>2020-04-01</String></Simple>") {
		t.Errorf("missing DATE node in: %s", xml)
	}
	if !strings.Contains(xml, "<Simple><Name>YEAR</Name><String>2020</String></Simple>") {
		t.Errorf("missing YEAR node in: %s", xml)
	}
}

func TestBuildTagXML_WithSummary(t *testing.T) {
	xml := buildTagXML("Episode summary", "", "", RatingNone, false)
	if !strings.Contains(xml, "<Simple><Name>SUMMARY</Name><String>Episode summary</String></Simple>") {
		t.Errorf("missing SUMMARY node in: %s", xml)
	}
}

func TestBuildTagXML_WithTMDBID(t *testing.T) {
	xml := buildTagXML("", "12345", "", RatingNone, false)
	if !strings.Contains(xml, "<Simple><Name>TMDB_ID</Name><String>12345</String></Simple>") {
		t.Errorf("missing TMDB_ID node in: %s", xml)
	}
}

func TestBuildTagXML_NoDate_ExcludesDateNodes(t *testing.T) {
	xml := buildTagXML("summary", "id", "", RatingNone, false)
	if strings.Contains(xml, "DATE") || strings.Contains(xml, "YEAR") {
		t.Errorf("unexpected DATE/YEAR in: %s", xml)
	}
}

func TestBuildTagXML_AllEmpty_ReturnsEmpty(t *testing.T) {
	xml := buildTagXML("", "", "", RatingNone, false)
	if xml != "" {
		t.Errorf("expected empty string, got: %s", xml)
	}
}

func TestBuildTagXML_WrappedInTagsElement(t *testing.T) {
	xml := buildTagXML("s", "", "", RatingNone, false)
	if !strings.HasPrefix(xml, "<Tags>") || !strings.HasSuffix(xml, "</Tags>") {
		t.Errorf("expected <Tags>...</Tags> wrapper, got: %s", xml)
	}
}

func TestBuildTagXML_WithRating_IncludesLAWRatingNestedInCountry(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingR16, false)
	// LAW_RATING must be nested *inside* COUNTRY (COUNTRY is a nesting tag per Matroska spec).
	want := "<Simple><Name>COUNTRY</Name><String>NZ</String>" +
		"<Simple><Name>LAW_RATING</Name><String>R16</String></Simple></Simple>"
	if !strings.Contains(xml, want) {
		t.Errorf("expected nested COUNTRY>LAW_RATING in: %s", xml)
	}
}

func TestBuildTagXML_NoRating_OmitsLAWRating(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingNone, false)
	if strings.Contains(xml, "LAW_RATING") || strings.Contains(xml, "COUNTRY") {
		t.Errorf("unexpected LAW_RATING/COUNTRY in: %s", xml)
	}
}

func TestBuildTagXML_WithDeband_IncludesDEBAND(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingNone, true)
	if !strings.Contains(xml, "<Simple><Name>DEBAND</Name><String>true</String></Simple>") {
		t.Errorf("expected DEBAND node in: %s", xml)
	}
}

func TestBuildTagXML_DebandFalse_OmitsDEBAND(t *testing.T) {
	xml := buildTagXML("s", "123", "", RatingNone, false)
	if strings.Contains(xml, "DEBAND") {
		t.Errorf("unexpected DEBAND node in: %s", xml)
	}
}

// --- updateRatingInXML ---

func TestUpdateRatingInXML_AddsRatingToExistingTag(t *testing.T) {
	input := `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	out := ratingXML(t, input, RatingM)
	if !strings.Contains(out, "LAW_RATING") {
		t.Errorf("expected LAW_RATING in output: %s", out)
	}
	if !strings.Contains(out, "DEBAND") {
		t.Errorf("DEBAND should be preserved: %s", out)
	}
}

func TestUpdateRatingInXML_ReplacesExistingRating(t *testing.T) {
	input := `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>` +
		`<Simple><Name>COUNTRY</Name><String>NZ</String><Simple><Name>LAW_RATING</Name><String>PG</String></Simple></Simple>` +
		`</Tag></Tags>`
	out := ratingXML(t, input, RatingM)
	if strings.Count(out, "LAW_RATING") != 1 {
		t.Errorf("expected exactly one LAW_RATING: %s", out)
	}
	if !strings.Contains(out, "<String>M</String>") {
		t.Errorf("expected new rating M in output: %s", out)
	}
}

func TestUpdateRatingInXML_RatingNone_RemovesExistingRating(t *testing.T) {
	input := `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>` +
		`<Simple><Name>COUNTRY</Name><String>NZ</String><Simple><Name>LAW_RATING</Name><String>M</String></Simple></Simple>` +
		`</Tag></Tags>`
	out := ratingXML(t, input, RatingNone)
	if strings.Contains(out, "LAW_RATING") || strings.Contains(out, "COUNTRY") {
		t.Errorf("LAW_RATING/COUNTRY should be removed when RatingNone: %s", out)
	}
}

func TestUpdateRatingInXML_EmptyXML_BuildsFromScratch(t *testing.T) {
	out := ratingXML(t, "", RatingR16)
	if !strings.Contains(out, "LAW_RATING") {
		t.Errorf("expected LAW_RATING in scratch-built output: %s", out)
	}
}

// --- ManualEntry.resolveDeband ---

func TestResolveDeband_ShowLevel(t *testing.T) {
	tr := true
	e := ManualEntry{Deband: &tr}
	if !e.resolveDeband("S01", "S01E05") {
		t.Error("expected true from show-level deband")
	}
}

func TestResolveDeband_SeasonOverridesShow(t *testing.T) {
	tr := true
	e := ManualEntry{
		Deband:        &tr,
		DebandSeasons: map[string]bool{"S01": false},
	}
	if e.resolveDeband("S01", "S01E05") {
		t.Error("season-level false should override show-level true")
	}
}

func TestResolveDeband_EpisodeOverridesSeason(t *testing.T) {
	e := ManualEntry{
		DebandSeasons:  map[string]bool{"S01": false},
		DebandEpisodes: map[string]bool{"S01E05": true},
	}
	if !e.resolveDeband("S01", "S01E05") {
		t.Error("episode-level true should override season-level false")
	}
}

func TestResolveDeband_NotConfigured_ReturnsFalse(t *testing.T) {
	e := ManualEntry{}
	if e.resolveDeband("S01", "S01E01") {
		t.Error("expected false when nothing configured")
	}
}

func TestResolveDeband_EmptyEpisodeKey_SkipsEpisodeLookup(t *testing.T) {
	e := ManualEntry{
		DebandEpisodes: map[string]bool{"S01E05": true},
		DebandSeasons:  map[string]bool{"S01": false},
	}
	// Empty episode key — should use season level, not episode level.
	if e.resolveDeband("S01", "") {
		t.Error("expected season-level false when episode key is empty")
	}
}

// --- ManualEntry.hasDebandConfig ---

func TestHasDebandConfig_ShowLevel(t *testing.T) {
	tr := true
	e := ManualEntry{Deband: &tr}
	if !e.hasDebandConfig("S01", "S01E01") {
		t.Error("expected true with show-level deband set")
	}
}

func TestHasDebandConfig_SeasonLevel(t *testing.T) {
	e := ManualEntry{DebandSeasons: map[string]bool{"S01": true}}
	if !e.hasDebandConfig("S01", "S01E01") {
		t.Error("expected true with season-level deband set")
	}
}

func TestHasDebandConfig_EpisodeLevel(t *testing.T) {
	e := ManualEntry{DebandEpisodes: map[string]bool{"S01E05": false}}
	if !e.hasDebandConfig("S01", "S01E05") {
		t.Error("expected true even when episode deband is false (explicitly configured)")
	}
}

func TestHasDebandConfig_NotConfigured(t *testing.T) {
	e := ManualEntry{}
	if e.hasDebandConfig("S01", "S01E01") {
		t.Error("expected false when no deband config exists")
	}
}

func TestHasDebandConfig_OtherSeasonConfigured(t *testing.T) {
	e := ManualEntry{DebandSeasons: map[string]bool{"S02": true}}
	if e.hasDebandConfig("S01", "S01E01") {
		t.Error("expected false when only a different season is configured")
	}
}

// --- updateDebandInXML ---

const existingTagsXML = `<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>` +
	`<Simple><Name>DATE</Name><String>2020-04-01</String></Simple>` +
	`<Simple><Name>YEAR</Name><String>2020</String></Simple>` +
	`</Tag></Tags>`

func TestUpdateDebandInXML_AddToExistingTags(t *testing.T) {
	result := debandXML(t, existingTagsXML, true)
	if !strings.Contains(result, "<Simple><Name>DEBAND</Name><String>true</String></Simple>") {
		t.Errorf("DEBAND element not added: %s", result)
	}
	if !strings.Contains(result, "DATE") {
		t.Errorf("existing DATE tag was lost: %s", result)
	}
}

func TestUpdateDebandInXML_RemoveDebandFromExistingTags(t *testing.T) {
	xml := existingTagsXML[:len(existingTagsXML)-len("</Tag></Tags>")] +
		`<Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	result := debandXML(t, xml, false)
	if strings.Contains(result, "DEBAND") {
		t.Errorf("DEBAND element not removed: %s", result)
	}
	if !strings.Contains(result, "DATE") {
		t.Errorf("existing DATE tag was lost: %s", result)
	}
}

func TestUpdateDebandInXML_PrettyPrintedXML(t *testing.T) {
	// mkvextract outputs indented XML; verify the regex handles whitespace.
	pretty := "<Tags>\n  <Tag>\n    <Targets>\n      <TargetTypeValue>50</TargetTypeValue>\n    </Targets>\n" +
		"    <Simple>\n      <Name>DEBAND</Name>\n      <String>true</String>\n    </Simple>\n  </Tag>\n</Tags>"
	result := debandXML(t, pretty, false)
	if strings.Contains(result, "DEBAND") {
		t.Errorf("DEBAND not removed from pretty-printed XML: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_AddsDebandOnly(t *testing.T) {
	result := debandXML(t, "", true)
	if !strings.Contains(result, "DEBAND") {
		t.Errorf("expected DEBAND in minimal XML: %s", result)
	}
	if !strings.Contains(result, "<Tags>") {
		t.Errorf("expected Tags wrapper: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_DebandFalse_ReturnsEmptyTagsDoc(t *testing.T) {
	if got := debandXML(t, "", false); got != emptyTagsDoc {
		t.Errorf("got %q, want %q", got, emptyTagsDoc)
	}
}
