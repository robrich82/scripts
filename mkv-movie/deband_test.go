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
	result := debandXML(t, xml, true)
	if !strings.Contains(result, "<Name>DEBAND</Name>") {
		t.Errorf("expected DEBAND element in output: %s", result)
	}
	if !strings.Contains(result, "<String>true</String>") {
		t.Errorf("expected DEBAND value true in output: %s", result)
	}
}

func TestUpdateDebandInXML_RemoveWhenPresent(t *testing.T) {
	xml := `<Tags><Tag><Targets></Targets><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	result := debandXML(t, xml, false)
	if strings.Contains(result, "DEBAND") {
		t.Errorf("expected DEBAND element removed, got: %s", result)
	}
}

func TestUpdateDebandInXML_PreservesOtherElements(t *testing.T) {
	xml := `<Tags><Tag><Targets></Targets><Simple><Name>TMDB_ID</Name><String>99</String></Simple><Simple><Name>DEBAND</Name><String>true</String></Simple></Tag></Tags>`
	result := debandXML(t, xml, false)
	if !strings.Contains(result, "TMDB_ID") {
		t.Errorf("TMDB_ID should be preserved after deband removal: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_DebandTrue_BuildsMinimalXML(t *testing.T) {
	result := debandXML(t, "", true)
	if !strings.Contains(result, "<Tags>") {
		t.Errorf("expected Tags wrapper in fallback XML: %s", result)
	}
	if !strings.Contains(result, "DEBAND") {
		t.Errorf("expected DEBAND in fallback XML: %s", result)
	}
}

func TestUpdateDebandInXML_NoTagBlock_DebandFalse_ReturnsEmptyTagsDoc(t *testing.T) {
	if got := debandXML(t, "", false); got != emptyTagsDoc {
		t.Errorf("got %q, want %q", got, emptyTagsDoc)
	}
}

func TestUpdateDebandInXML_InsertsIntoMovieBlockNotTrack(t *testing.T) {
	xml := `<Tags>` +
		`<Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets><Simple><Name>YEAR</Name><String>2020</String></Simple></Tag>` +
		`<Tag><Targets><TrackUID>111</TrackUID></Targets><Simple><Name>BPS</Name><String>1</String></Simple></Tag>` +
		`</Tags>`
	result := debandXML(t, xml, true)
	di := strings.Index(result, "DEBAND")
	bi := strings.Index(result, "<Name>BPS</Name>")
	if di < 0 || bi < 0 || di > bi {
		t.Errorf("DEBAND should land in the movie-level block, before the track tag: %s", result)
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
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}

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
	if _, ok := applyDebandOnlyFile(path, true, tagger, timer); ok {
		t.Error("expected false when tagger not available")
	}
}

func TestApplyDebandOnlyFile_ExtractError_ReturnsFalse(t *testing.T) {
	tagger := &fakeMKVTagger{available: true, extractErr: os.ErrNotExist}
	timer := &fakeFileTimer{}
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	if _, ok := applyDebandOnlyFile(path, true, tagger, timer); ok {
		t.Error("expected false when extract fails")
	}
}

func TestApplyDebandOnlyFile_Success_CallsApplyAndRestoresTimestamp(t *testing.T) {
	origTime := time.Date(2020, 6, 1, 0, 0, 0, 0, time.Local)
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	if err := os.Chtimes(path, origTime, origTime); err != nil {
		t.Fatal(err)
	}

	existingXML := `<Tags><Tag><Targets></Targets></Tag></Tags>`
	tagger := &fakeMKVTagger{available: true, extractedXML: existingXML}
	timer := &fakeFileTimer{}

	if _, ok := applyDebandOnlyFile(path, true, tagger, timer); !ok {
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

func TestApplyDebandOnlyFile_StatError_DoesNotTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Missing.2020.mkv")
	tagger := &fakeMKVTagger{available: true, extractedXML: `<Tags><Tag><Targets></Targets></Tag></Tags>`}

	var ok bool
	out := captureStdout(t, func() { _, ok = applyDebandOnlyFile(path, true, tagger, &fakeFileTimer{}) })
	if ok {
		t.Error("expected false when the file can't be stat'd")
	}
	if len(tagger.appliedPaths) != 0 {
		t.Errorf("ApplyTags called with %v, want no call: timestamps couldn't be restored", tagger.appliedPaths)
	}
	if !strings.Contains(out, "not tagging") {
		t.Errorf("output doesn't explain the skip:\n%s", out)
	}
}

// debandApplied reports whether applyDebandOnlyFile wrote the tags.
func debandApplied(path string, deband bool, tagger MKVTagger, timer FileTimer) bool {
	_, ok := applyDebandOnlyFile(path, deband, tagger, timer)
	return ok
}

func TestApplyDebandOnlyFile_TimestampRestoreFails_NamesSteps(t *testing.T) {
	path := makeTempMKVNamed(t, "Movie.2020.mkv")
	tagger := &fakeMKVTagger{available: true, extractedXML: `<Tags><Tag><Targets></Targets></Tag></Tags>`}
	timer := &fakeFileTimer{setTimesErr: os.ErrPermission, setCreationTimeErr: os.ErrPermission}

	failed, ok := applyDebandOnlyFile(path, true, tagger, timer)
	if !ok {
		t.Fatal("ok = false, want true: the tags were written")
	}
	if strings.Join(failed, ",") != "mtime,ctime" {
		t.Errorf("failed = %v, want [mtime ctime]", failed)
	}
}

// --- runDebandOnly tests ---

func TestRunDebandOnly_TimestampRestoreFails_CountsAsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Coco (2017).mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	debandTrue := true
	manualData := map[string]MovieManualEntry{"Coco (2017)": {Deband: &debandTrue}}
	tagger := &fakeMKVTagger{available: true, extractedXML: `<Tags><Tag><Targets></Targets></Tag></Tags>`}
	timer := &fakeFileTimer{setCreationTimeErr: os.ErrPermission}

	var stats Stats
	out := captureStdout(t, func() { stats = runDebandOnly(dir, manualData, tagger, timer) })

	if stats.Updated != 0 || stats.Errors != 1 {
		t.Errorf("stats = %+v, want Updated=0 Errors=1", stats)
	}
	if len(tagger.appliedPaths) != 1 {
		t.Errorf("ApplyTags calls = %v, want 1", tagger.appliedPaths)
	}
	if !strings.Contains(out, "[ERR] Deband updated: Coco (2017).mkv (failed: ctime)") {
		t.Errorf("result line doesn't name the failed step:\n%s", out)
	}
	if strings.Contains(out, "[✓]") {
		t.Errorf("output reports success:\n%s", out)
	}
}

func TestRunDebandOnly_SkipsFilesNotInManualData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Coco.2017.mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, "Coco (2017).mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, "Coco (2017).mkv"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

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
