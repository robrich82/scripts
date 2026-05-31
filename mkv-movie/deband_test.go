package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- updateDebandInXML tests ---

func TestUpdateDebandInXML_AddWhenNotPresent(t *testing.T) {
	xml := `<Tags><Tag><Targets></Targets><Simple><Name>TMDB_ID</Name><String>42</String></Simple></Tag></Tags>`
	result := updateDebandInXML(xml, true)
	if !strings.Contains(result, "<Name>DEBAND</Name>") {
		t.Errorf("expected DEBAND element in output: %s", result)
	}
	if !strings.Contains(result, "<String>true</String>") {
		t.Errorf("expected DEBAND value true in output: %s", result)
	}
}

func TestUpdateDebandInXML_RemoveWhenPresent(t *testing.T) {
	xml := `<Tags><Tag><Targets></Targets><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	result := updateDebandInXML(xml, false)
	if strings.Contains(result, "DEBAND") {
		t.Errorf("expected DEBAND element removed, got: %s", result)
	}
}

func TestUpdateDebandInXML_PreservesOtherElements(t *testing.T) {
	xml := `<Tags><Tag><Targets></Targets><Simple><Name>TMDB_ID</Name><String>99</String></Simple><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	result := updateDebandInXML(xml, false)
	if !strings.Contains(result, "TMDB_ID") {
		t.Errorf("TMDB_ID should be preserved after deband removal: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_DebandTrue_BuildsMinimalXML(t *testing.T) {
	result := updateDebandInXML("", true)
	if !strings.Contains(result, "<Tags>") {
		t.Errorf("expected Tags wrapper in fallback XML: %s", result)
	}
	if !strings.Contains(result, "DEBAND") {
		t.Errorf("expected DEBAND in fallback XML: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_DebandFalse_ReturnsEmpty(t *testing.T) {
	result := updateDebandInXML("", false)
	if result != "" {
		t.Errorf("expected empty string when no tags and deband=false, got: %s", result)
	}
}

// --- buildMovieTagXML deband test ---

func TestBuildMovieTagXML_DebandTrue_ContainsDeband(t *testing.T) {
	xml := buildMovieTagXML("", "1", "2020-01-01", RatingNone, true)
	if !strings.Contains(xml, "<Name>DEBAND</Name>") {
		t.Errorf("expected DEBAND tag in xml: %s", xml)
	}
	if !strings.Contains(xml, "<String>true</String>") {
		t.Errorf("expected DEBAND value true in xml: %s", xml)
	}
}

func TestBuildMovieTagXML_DebandFalse_NoDeband(t *testing.T) {
	xml := buildMovieTagXML("", "1", "2020-01-01", RatingNone, false)
	if strings.Contains(xml, "DEBAND") {
		t.Errorf("expected no DEBAND tag when deband=false: %s", xml)
	}
}

// --- LoadMovieManualData tests ---

func TestLoadMovieManualData_FileNotExist_ReturnsEmpty(t *testing.T) {
	result, err := LoadMovieManualData(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestLoadMovieManualData_ParsesDeband(t *testing.T) {
	debandTrue := true
	data := map[string]MovieManualEntry{
		"Coco (2017)": {Deband: &debandTrue},
	}
	b, _ := json.Marshal(data)
	path := filepath.Join(t.TempDir(), "manual_data.json")
	os.WriteFile(path, b, 0644)

	result, err := LoadMovieManualData(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := result["Coco (2017)"]
	if !ok {
		t.Fatal("expected entry for 'Coco (2017)'")
	}
	if entry.Deband == nil || !*entry.Deband {
		t.Errorf("expected Deband=true, got %v", entry.Deband)
	}
}

// --- fakeMKVTagger ---

type fakeMKVTagger struct {
	available    bool
	extractedXML string
	extractErr   error
	writeErr     error
	applyErr     error
	writtenXML   string
	appliedPaths []string
	cleanedPaths []string
}

func (f *fakeMKVTagger) Available() bool { return f.available }
func (f *fakeMKVTagger) ExtractTags(_ string) (string, error) {
	return f.extractedXML, f.extractErr
}
func (f *fakeMKVTagger) WriteTempTags(xml string) (string, error) {
	f.writtenXML = xml
	if f.writeErr != nil {
		return "", f.writeErr
	}
	return filepath.Join(os.TempDir(), "fake-tags.xml"), nil
}
func (f *fakeMKVTagger) ApplyTags(filePath, _ string) error {
	f.appliedPaths = append(f.appliedPaths, filePath)
	return f.applyErr
}
func (f *fakeMKVTagger) Cleanup(paths ...string) {
	f.cleanedPaths = append(f.cleanedPaths, paths...)
}

// --- applyDebandOnlyFile tests ---

func TestApplyDebandOnlyFile_TaggerNotAvailable_ReturnsFalse(t *testing.T) {
	tagger := &fakeMKVTagger{available: false}
	timer := &fakeFileTimer{}
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	if applyDebandOnlyFile(path, true, tagger, timer) {
		t.Error("expected false when tagger not available")
	}
}

func TestApplyDebandOnlyFile_ExtractError_ReturnsFalse(t *testing.T) {
	tagger := &fakeMKVTagger{available: true, extractErr: os.ErrNotExist}
	timer := &fakeFileTimer{}
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	if applyDebandOnlyFile(path, true, tagger, timer) {
		t.Error("expected false when extract fails")
	}
}

func TestApplyDebandOnlyFile_Success_CallsApplyAndRestoresTimestamp(t *testing.T) {
	origTime := time.Date(2020, 6, 1, 0, 0, 0, 0, time.Local)
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	os.Chtimes(path, origTime, origTime)

	existingXML := `<Tags><Tag><Targets></Targets></Tag></Tags>`
	tagger := &fakeMKVTagger{available: true, extractedXML: existingXML}
	timer := &fakeFileTimer{}

	if !applyDebandOnlyFile(path, true, tagger, timer) {
		t.Fatal("expected true on success")
	}
	if len(tagger.appliedPaths) != 1 || tagger.appliedPaths[0] != path {
		t.Errorf("expected ApplyTags called with %s, got %v", path, tagger.appliedPaths)
	}
	if !strings.Contains(tagger.writtenXML, "DEBAND") {
		t.Errorf("expected DEBAND in written XML: %s", tagger.writtenXML)
	}
	if len(timer.setTimesCalls) == 0 {
		t.Error("expected SetTimes called to restore timestamp")
	}
}

// --- runDebandOnly tests ---

func TestRunDebandOnly_SkipsFilesNotInManualData(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Coco.2017.mkv"), []byte{}, 0644)

	tagger := &fakeMKVTagger{available: true}
	timer := &fakeFileTimer{}
	stats := runDebandOnly(dir, map[string]MovieManualEntry{}, tagger, timer)

	if len(tagger.appliedPaths) != 0 {
		t.Errorf("expected no apply calls for file not in manualData, got %v", tagger.appliedPaths)
	}
	if stats.Updated != 0 || stats.Errors != 0 {
		t.Errorf("expected zero stats, got %+v", stats)
	}
}

func TestRunDebandOnly_SkipsFilesWithNilDeband(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Coco (2017).mkv"), []byte{}, 0644)

	manualData := map[string]MovieManualEntry{
		"Coco (2017)": {Deband: nil},
	}
	tagger := &fakeMKVTagger{available: true}
	timer := &fakeFileTimer{}
	stats := runDebandOnly(dir, manualData, tagger, timer)

	if len(tagger.appliedPaths) != 0 {
		t.Errorf("expected no apply calls for nil deband entry, got %v", tagger.appliedPaths)
	}
	_ = stats
}

func TestRunDebandOnly_UpdatesConfiguredFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Coco (2017).mkv"), []byte{}, 0644)

	debandTrue := true
	manualData := map[string]MovieManualEntry{
		"Coco (2017)": {Deband: &debandTrue},
	}
	tagger := &fakeMKVTagger{
		available:    true,
		extractedXML: `<Tags><Tag><Targets></Targets></Tag></Tags>`,
	}
	timer := &fakeFileTimer{}
	stats := runDebandOnly(dir, manualData, tagger, timer)

	if stats.Updated != 1 {
		t.Errorf("expected Updated=1, got %+v", stats)
	}
	if len(tagger.appliedPaths) != 1 {
		t.Errorf("expected 1 apply call, got %v", tagger.appliedPaths)
	}
}
