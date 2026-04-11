package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// ── helpers ────────────────────────────────────────────────────────────────

// makePNG returns a minimal PNG-encoded image of the given dimensions.
func makePNG(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type zipEntry struct {
	name string
	data []byte
}

// makeCBZ writes a CBZ file containing the given entries and returns its path.
func makeCBZ(t *testing.T, entries []zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.cbz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

// readCBZNames returns the file names inside a CBZ in the order they appear.
func readCBZNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// readCBZEntry returns the bytes of a named entry inside a CBZ.
func readCBZEntry(t *testing.T, path, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			var buf bytes.Buffer
			buf.ReadFrom(rc)
			return buf.Bytes()
		}
	}
	t.Fatalf("entry %q not found in %s", name, path)
	return nil
}

func containsEntry(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// ── naturalLess tests ──────────────────────────────────────────────────────

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"001.jpg", "002.jpg", true},
		{"002.jpg", "001.jpg", false},
		{"page2.jpg", "page10.jpg", true},   // numeric: 2 < 10
		{"page10.jpg", "page2.jpg", false},
		{"abc", "abd", true},
		{"abc", "abc", false},
		{"", "a", true},
		{"a", "", false},
		{"Page1.jpg", "page2.jpg", true}, // case-insensitive
		{"sub/001.jpg", "sub/002.jpg", true},
	}
	for _, tc := range cases {
		got := naturalLess(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ── processCBZ integration tests ───────────────────────────────────────────

// portrait (100×200) and landscape (200×100) helpers.
var (
	portrait  = makePNG(100, 200)
	landscape = makePNG(200, 100)
)

// TestProcessCBZ_BasicRename verifies the simplest case: three portrait pages,
// no ComicInfo. Expects cover renamed, blank inserted, remaining pages numbered.
func TestProcessCBZ_BasicRename(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
		{"003.png", portrait},
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	names := readCBZNames(t, path)

	if !containsEntry(names, "cover.png") {
		t.Error("expected cover.png in output")
	}
	if !containsEntry(names, "p001.png") {
		t.Error("expected p001.png (blank) in output")
	}
	if !containsEntry(names, "p002.png") {
		t.Error("expected p002.png in output")
	}
	if !containsEntry(names, "p003.png") {
		t.Error("expected p003.png in output")
	}
	if !containsEntry(names, "ComicInfo.xml") {
		t.Error("expected ComicInfo.xml in output")
	}
	// Original names must be gone.
	for _, old := range []string{"001.png", "002.png", "003.png"} {
		if containsEntry(names, old) {
			t.Errorf("original entry %q should have been removed", old)
		}
	}
}

// TestProcessCBZ_BlankIsWhite verifies the inserted blank page is a valid
// white image with the same dimensions as the first non-cover page.
func TestProcessCBZ_BlankIsWhite(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	blankData := readCBZEntry(t, path, "p001.png")
	img, _, err := image.Decode(bytes.NewReader(blankData))
	if err != nil {
		t.Fatalf("decode blank: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 100 || bounds.Dy() != 200 {
		t.Errorf("blank dimensions = %dx%d, want 100x200", bounds.Dx(), bounds.Dy())
	}
	// Check that a sample pixel is white.
	r, g, b, _ := img.At(50, 100).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff {
		t.Errorf("blank pixel at center is not white: r=%x g=%x b=%x", r, g, b)
	}
}

// TestProcessCBZ_DoublePageSkipped verifies that a landscape (double) page
// between the cover and the first portrait page is left in place and the blank
// is inserted before the first portrait page.
func TestProcessCBZ_DoublePageSkipped(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},  // cover
		{"002.png", landscape}, // double-page spread
		{"003.png", portrait},  // first non-double page
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	names := readCBZNames(t, path)
	// Expected order: cover.png, p001.png (double), p002.png (blank), p003.png
	if !containsEntry(names, "cover.png") {
		t.Error("missing cover.png")
	}
	if !containsEntry(names, "p001.png") {
		t.Error("missing p001.png (double-page)")
	}
	if !containsEntry(names, "p002.png") {
		t.Error("missing p002.png (blank)")
	}
	if !containsEntry(names, "p003.png") {
		t.Error("missing p003.png")
	}

	// Blank should be portrait (same size as 003.png = 100×200).
	blankData := readCBZEntry(t, path, "p002.png")
	img, _, err := image.Decode(bytes.NewReader(blankData))
	if err != nil {
		t.Fatalf("decode blank: %v", err)
	}
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 200 {
		t.Errorf("blank size = %dx%d, want 100x200", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestProcessCBZ_ComicInfoUpdated verifies PageCount and Page entries are
// updated correctly when a ComicInfo.xml is present.
func TestProcessCBZ_ComicInfoUpdated(t *testing.T) {
	ci := `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Test Comic</Title>
  <PageCount>3</PageCount>
  <Pages>
    <Page Image="0" Type="FrontCover" ImageWidth="100" ImageHeight="200" />
    <Page Image="1" ImageWidth="100" ImageHeight="200" />
    <Page Image="2" ImageWidth="100" ImageHeight="200" />
  </Pages>
</ComicInfo>`

	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
		{"003.png", portrait},
		{"ComicInfo.xml", []byte(ci)},
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	ciData := readCBZEntry(t, path, "ComicInfo.xml")
	var out ComicInfo
	if err := xml.Unmarshal(ciData, &out); err != nil {
		t.Fatalf("unmarshal ComicInfo.xml: %v", err)
	}

	// 3 original pages + 1 blank = 4 pages.
	if out.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", out.PageCount)
	}
	if out.Pages == nil || len(out.Pages.Pages) != 4 {
		t.Errorf("Pages count = %d, want 4", len(out.Pages.Pages))
	}
	// Page 0 must be FrontCover.
	if out.Pages.Pages[0].Type != "FrontCover" {
		t.Errorf("page 0 type = %q, want FrontCover", out.Pages.Pages[0].Type)
	}
	// Title must be preserved.
	if out.Title != "Test Comic" {
		t.Errorf("Title = %q, want %q", out.Title, "Test Comic")
	}
}

// TestProcessCBZ_BackupCreated verifies a .bak file is created alongside the
// original.
func TestProcessCBZ_BackupCreated(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	if _, err := os.Stat(path + ".bak"); os.IsNotExist(err) {
		t.Error("expected .bak file to exist")
	}
}

// TestProcessCBZ_DryRun verifies that --dry-run leaves the file unchanged.
func TestProcessCBZ_DryRun(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
	})

	// Record original names.
	before := readCBZNames(t, path)

	if err := processCBZ(path, true, false, false); err != nil {
		t.Fatalf("processCBZ dry-run: %v", err)
	}

	after := readCBZNames(t, path)
	if len(before) != len(after) {
		t.Errorf("dry-run changed file: before=%v after=%v", before, after)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("dry-run must not create a .bak file")
	}
}

// TestProcessCBZ_Idempotent verifies that an already-processed CBZ is skipped.
func TestProcessCBZ_Idempotent(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", portrait},
		{"p001.png", portrait}, // blank already present
		{"p002.png", portrait},
	})

	before := readCBZNames(t, path)

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	after := readCBZNames(t, path)
	if len(before) != len(after) {
		t.Errorf("idempotency broken: before=%v after=%v", before, after)
	}
}

// TestProcessCBZ_SubdirImages verifies that images stored inside a subdirectory
// in the zip are moved to the root.
func TestProcessCBZ_SubdirImages(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"Comic Name/001.png", portrait},
		{"Comic Name/002.png", portrait},
		{"Comic Name/003.png", portrait},
	})

	if err := processCBZ(path, false, false, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	names := readCBZNames(t, path)
	for _, want := range []string{"cover.png", "p001.png", "p002.png", "p003.png"} {
		if !containsEntry(names, want) {
			t.Errorf("missing %q in output; got %v", want, names)
		}
	}
	// Subdirectory paths must be gone.
	for _, old := range []string{"Comic Name/001.png", "Comic Name/002.png", "Comic Name/003.png"} {
		if containsEntry(names, old) {
			t.Errorf("subdirectory entry %q should have been removed", old)
		}
	}
}
