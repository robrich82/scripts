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

// makePNGShade is like makePNG but fills the image with the given gray level so
// that distinct shades produce byte-distinct (and CRC-distinct) PNGs.
func makePNGShade(w, h int, shade uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
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
		{"page2.jpg", "page10.jpg", true}, // numeric: 2 < 10
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

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
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

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
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

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
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

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
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

	if _, err := processCBZ(path, false, false, false, false, false); err != nil {
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

	if _, err := processCBZ(path, true, false, false, true, false); err != nil {
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

// TestProcessCBZ_Idempotent verifies that an already-processed CBZ is skipped
// and returns skipped=true.
func TestProcessCBZ_Idempotent(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", portrait},
		{"p001.png", portrait}, // blank already present
		{"p002.png", portrait},
	})

	before := readCBZNames(t, path)

	skipped, err := processCBZ(path, false, false, false, true, false)
	if err != nil {
		t.Fatalf("processCBZ: %v", err)
	}
	if !skipped {
		t.Error("expected skipped=true for already-processed CBZ")
	}

	after := readCBZNames(t, path)
	if len(before) != len(after) {
		t.Errorf("idempotency broken: before=%v after=%v", before, after)
	}
}

// TestProcessCBZ_ForceReprocesses verifies that --force reprocesses an
// already-processed CBZ instead of skipping it, and does not insert a second
// blank page (which would shift every page and grow the count).
func TestProcessCBZ_ForceReprocesses(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", portrait},
		{"p001.png", portrait}, // blank already present
		{"p002.png", portrait},
	})

	skipped, err := processCBZ(path, false, false, false, true, true) // force
	if err != nil {
		t.Fatalf("processCBZ: %v", err)
	}
	if skipped {
		t.Error("expected skipped=false when --force reprocesses")
	}

	names := readCBZNames(t, path)
	for _, want := range []string{"cover.png", "p001.png", "p002.png", "ComicInfo.xml"} {
		if !containsEntry(names, want) {
			t.Errorf("missing %q in output; got %v", want, names)
		}
	}
	// A second blank would have introduced p003.png and shifted pages.
	if containsEntry(names, "p003.png") {
		t.Errorf("force must not insert a second blank page; got %v", names)
	}
}

// ── updateCoverCBZ tests ───────────────────────────────────────────────────

// TestUpdateCoverCBZ_ReplacesWhenLarger verifies that when cover.jpg in the
// directory is strictly larger in both dimensions the cover is auto-replaced.
func TestUpdateCoverCBZ_ReplacesWhenLarger(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 150)},
		{"p001.png", makePNG(100, 150)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(200, 300), 0644); err != nil {
		t.Fatal(err)
	}

	skipped, err := updateCoverCBZ(path, false, false, true, nil)
	if err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}
	if skipped {
		t.Error("expected skipped=false when cover.jpg is larger")
	}

	names := readCBZNames(t, path)
	if !containsEntry(names, "cover.jpg") {
		t.Errorf("expected cover.jpg in CBZ; got %v", names)
	}
	if containsEntry(names, "cover.png") {
		t.Error("old cover.png should have been removed")
	}

	data := readCBZEntry(t, path, "cover.jpg")
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode new cover: %v", err)
	}
	if img.Bounds().Dx() != 200 || img.Bounds().Dy() != 300 {
		t.Errorf("new cover dims = %dx%d, want 200x300", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestUpdateCoverCBZ_SkipsWhenSmaller verifies that a cover.jpg smaller than
// the one in the CBZ causes a skip.
func TestUpdateCoverCBZ_SkipsWhenSmaller(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(200, 300)},
		{"p001.png", makePNG(200, 300)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(100, 150), 0644); err != nil {
		t.Fatal(err)
	}

	skipped, err := updateCoverCBZ(path, false, false, true, nil)
	if err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}
	if !skipped {
		t.Error("expected skipped=true when cover.jpg is smaller")
	}

	names := readCBZNames(t, path)
	if containsEntry(names, "cover.jpg") {
		t.Error("cover.jpg should not have been written to CBZ")
	}
}

// TestUpdateCoverCBZ_SkipsWhenNoCoverJPG verifies that a missing cover.jpg
// sibling is a no-op.
func TestUpdateCoverCBZ_SkipsWhenNoCoverJPG(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 150)},
		{"p001.png", makePNG(100, 150)},
	})

	skipped, err := updateCoverCBZ(path, false, false, true, nil)
	if err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}
	if !skipped {
		t.Error("expected skipped=true when no cover.jpg exists")
	}
}

// TestUpdateCoverCBZ_SkipsWhenNoCoverInCBZ verifies that a CBZ with no cover.*
// entry is a no-op.
func TestUpdateCoverCBZ_SkipsWhenNoCoverInCBZ(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"p001.png", makePNG(100, 150)},
		{"p002.png", makePNG(100, 150)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(200, 300), 0644); err != nil {
		t.Fatal(err)
	}

	skipped, err := updateCoverCBZ(path, false, false, true, nil)
	if err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}
	if !skipped {
		t.Error("expected skipped=true when CBZ has no cover.* entry")
	}
}

// TestUpdateCoverCBZ_MixedDimsCallsPrompt verifies that when one dimension is
// greater and the other is less, the prompt function is called.
func TestUpdateCoverCBZ_MixedDimsCallsPrompt(t *testing.T) {
	// CBZ cover 100×200; external cover is wider but shorter: 150×100.
	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 200)},
		{"p001.png", makePNG(100, 200)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(150, 100), 0644); err != nil {
		t.Fatal(err)
	}

	// User says no → should skip without modifying.
	prompted := false
	skipped, err := updateCoverCBZ(path, false, false, true, func(q string) (bool, error) {
		prompted = true
		return false, nil
	})
	if err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}
	if !prompted {
		t.Error("expected prompt to be called for mixed dimensions")
	}
	if !skipped {
		t.Error("expected skipped=true when user declines replacement")
	}

	// User says yes → cover should be replaced.
	skipped, err = updateCoverCBZ(path, false, false, true, func(q string) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("updateCoverCBZ (yes): %v", err)
	}
	if skipped {
		t.Error("expected skipped=false when user accepts replacement")
	}
	if !containsEntry(readCBZNames(t, path), "cover.jpg") {
		t.Error("cover.jpg should be in CBZ after user accepted")
	}
}

// TestUpdateCoverCBZ_UpdatesComicInfo verifies that when the cover is replaced
// the Image=0 entry in ComicInfo.xml has its width, height and size updated.
// Fields that were absent in the original must not be added.
func TestUpdateCoverCBZ_UpdatesComicInfo(t *testing.T) {
	ci := `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Test</Title>
  <Pages>
    <Page Image="0" ImageHeight="150" ImageSize="999" ImageWidth="100" Type="FrontCover" />
    <Page Image="1" ImageWidth="100" ImageHeight="150" />
  </Pages>
</ComicInfo>`

	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 150)},
		{"p001.png", makePNG(100, 150)},
		{"ComicInfo.xml", []byte(ci)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(200, 300), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := updateCoverCBZ(path, false, false, true, nil); err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}

	ciData := readCBZEntry(t, path, "ComicInfo.xml")
	var out ComicInfo
	if err := xml.Unmarshal(ciData, &out); err != nil {
		t.Fatalf("unmarshal ComicInfo.xml: %v", err)
	}
	if out.Title != "Test" {
		t.Errorf("Title = %q, want %q", out.Title, "Test")
	}
	if out.Pages == nil || len(out.Pages.Pages) == 0 {
		t.Fatal("Pages missing")
	}
	p0 := out.Pages.Pages[0]
	if p0.ImageWidth != 200 {
		t.Errorf("page 0 ImageWidth = %d, want 200", p0.ImageWidth)
	}
	if p0.ImageHeight != 300 {
		t.Errorf("page 0 ImageHeight = %d, want 300", p0.ImageHeight)
	}
	if p0.ImageSize == 0 || p0.ImageSize == 999 {
		t.Errorf("page 0 ImageSize = %d, want updated non-zero value", p0.ImageSize)
	}
}

// TestUpdateCoverCBZ_ComicInfoAbsentFieldsNotAdded verifies that fields absent
// in the original ComicInfo page 0 are not added after a cover replace.
func TestUpdateCoverCBZ_ComicInfoAbsentFieldsNotAdded(t *testing.T) {
	// Page 0 has no ImageSize or ImageWidth — only ImageHeight.
	ci := `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Pages>
    <Page Image="0" ImageHeight="150" Type="FrontCover" />
  </Pages>
</ComicInfo>`

	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 150)},
		{"p001.png", makePNG(100, 150)},
		{"ComicInfo.xml", []byte(ci)},
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(200, 300), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := updateCoverCBZ(path, false, false, true, nil); err != nil {
		t.Fatalf("updateCoverCBZ: %v", err)
	}

	ciData := readCBZEntry(t, path, "ComicInfo.xml")
	var out ComicInfo
	if err := xml.Unmarshal(ciData, &out); err != nil {
		t.Fatalf("unmarshal ComicInfo.xml: %v", err)
	}
	p0 := out.Pages.Pages[0]
	if p0.ImageHeight != 300 {
		t.Errorf("page 0 ImageHeight = %d, want 300", p0.ImageHeight)
	}
	if p0.ImageWidth != 0 {
		t.Errorf("page 0 ImageWidth = %d, want 0 (was absent in original)", p0.ImageWidth)
	}
	if p0.ImageSize != 0 {
		t.Errorf("page 0 ImageSize = %d, want 0 (was absent in original)", p0.ImageSize)
	}
}

// ── KFX / calibre-HTML ordering tests ─────────────────────────────────────

// makeKFXCBZ creates a CBZ with the calibre KFX export structure:
// a root HTML file with calibreANext navigation pointing to XHTML pages
// inside a matching *_files/OEBPS/ directory.
// pages is a slice of image names (without directory prefix) that define
// reading order via the XHTML navigation chain; images are portrait PNGs.
func makeKFXCBZ(t *testing.T, pages []string) string {
	t.Helper()

	const filesDir = "kfxtest_files/OEBPS/"

	// Build XHTML content for each page with next-page links.
	xhtmlFor := func(imgName, nextPage string) string {
		next := ""
		if nextPage != "" {
			next = `<a href="` + nextPage + `" class="calibreANext">next page</a>`
		}
		return `<html><body>` +
			`<img src="` + imgName + `" alt="page"/>` +
			next +
			`</body></html>`
	}

	var entries []zipEntry

	// Root HTML whose calibreANext points to the first XHTML page.
	firstXHTML := ""
	if len(pages) > 0 {
		firstXHTML = filesDir + "page_" + pages[0] + ".xhtml"
	}
	rootHTML := `<html><body><a href="` + firstXHTML + `" class="calibreANext">start</a></body></html>`
	entries = append(entries, zipEntry{"kfxtest.html", []byte(rootHTML)})

	for i, img := range pages {
		xhtmlName := "page_" + img + ".xhtml"
		nextXHTML := ""
		if i+1 < len(pages) {
			nextXHTML = "page_" + pages[i+1] + ".xhtml"
		}
		entries = append(entries, zipEntry{filesDir + xhtmlName, []byte(xhtmlFor(img+".png", nextXHTML))})
		entries = append(entries, zipEntry{filesDir + img + ".png", portrait})
	}

	return makeCBZ(t, entries)
}

// makeKFXCBZWithCover is like makeKFXCBZ but also writes a cover.jpeg at the
// *_files/ root. If linkedAtEnd is true, the last XHTML page links to the
// cover via "../cover.jpeg" (vol-11 pattern); otherwise the cover is unlinked
// (ONIMAI pattern).
func makeKFXCBZWithCover(t *testing.T, pages []string, linkedAtEnd bool) string {
	t.Helper()

	const filesDir = "kfxtest_files/OEBPS/"

	xhtmlFor := func(imgName, nextPage string) string {
		next := ""
		if nextPage != "" {
			next = `<a href="` + nextPage + `" class="calibreANext">next page</a>`
		}
		return `<html><body>` +
			`<img src="` + imgName + `" alt="page"/>` +
			next +
			`</body></html>`
	}

	var entries []zipEntry
	entries = append(entries, zipEntry{"kfxtest.html", []byte(
		`<html><body><a href="` + filesDir + `page_` + pages[0] + `.xhtml" class="calibreANext">start</a></body></html>`,
	)})

	for i, img := range pages {
		xhtmlName := "page_" + img + ".xhtml"
		nextXHTML := ""
		if i+1 < len(pages) {
			nextXHTML = "page_" + pages[i+1] + ".xhtml"
		} else if linkedAtEnd {
			nextXHTML = "../cover.jpeg" // last page links back to cover
		}
		entries = append(entries, zipEntry{filesDir + xhtmlName, []byte(xhtmlFor(img+".png", nextXHTML))})
		// Distinct shade per page so the cover is not byte-identical to any page.
		entries = append(entries, zipEntry{filesDir + img + ".png", makePNGShade(100, 200, uint8(i+1))})
	}

	// cover.jpeg lives at *_files/ root, not inside OEBPS/. Use a distinct shade
	// so it is a genuinely separate image from the interior pages.
	entries = append(entries, zipEntry{"kfxtest_files/cover.jpeg", makePNGShade(100, 200, 200)})

	return makeCBZ(t, entries)
}

// TestKFXPageOrder verifies that kfxPageOrder returns images in the order
// defined by the XHTML navigation chain, not alphabetical filename order.
func TestKFXPageOrder(t *testing.T) {
	// Natural sort: img_a, img_b, img_c. HTML chain: img_c → img_a → img_b.
	pages := []string{"img_c", "img_a", "img_b"}
	path := makeKFXCBZ(t, pages)

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil for KFX CBZ")
	}
	if len(ordered) != 3 {
		t.Fatalf("got %d images, want 3", len(ordered))
	}

	want := []string{
		"kfxtest_files/OEBPS/img_c.png",
		"kfxtest_files/OEBPS/img_a.png",
		"kfxtest_files/OEBPS/img_b.png",
	}
	for i, f := range ordered {
		if f.Name != want[i] {
			t.Errorf("ordered[%d] = %q, want %q", i, f.Name, want[i])
		}
	}
}

// makeKFXCBZSVG is like makeKFXCBZ but XHTML pages embed images via SVG
// <image xlink:href="..."> instead of <img src="...">, matching the format
// produced by some Calibre KFX exports.
func makeKFXCBZSVG(t *testing.T, pages []string) string {
	t.Helper()

	const filesDir = "kfxtest_files/OEBPS/"

	xhtmlFor := func(imgName, nextPage string) string {
		next := ""
		if nextPage != "" {
			next = `<a href="` + nextPage + `" class="calibreANext">next page</a>`
		}
		return `<html><body>` +
			`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">` +
			`<image xlink:href="` + imgName + `" width="100" height="200"/>` +
			`</svg>` +
			next +
			`</body></html>`
	}

	var entries []zipEntry

	firstXHTML := ""
	if len(pages) > 0 {
		firstXHTML = filesDir + "page_" + pages[0] + ".xhtml"
	}
	rootHTML := `<html><body><a href="` + firstXHTML + `" class="calibreANext">start</a></body></html>`
	entries = append(entries, zipEntry{"kfxtest.html", []byte(rootHTML)})

	for i, img := range pages {
		xhtmlName := "page_" + img + ".xhtml"
		nextXHTML := ""
		if i+1 < len(pages) {
			nextXHTML = "page_" + pages[i+1] + ".xhtml"
		}
		entries = append(entries, zipEntry{filesDir + xhtmlName, []byte(xhtmlFor(img+".png", nextXHTML))})
		entries = append(entries, zipEntry{filesDir + img + ".png", portrait})
	}

	return makeCBZ(t, entries)
}

// TestKFXPageOrder_SVGImage verifies that kfxPageOrder correctly extracts
// images referenced via SVG <image xlink:href="..."> rather than <img src>.
func TestKFXPageOrder_SVGImage(t *testing.T) {
	// Natural sort: img_a, img_b, img_c. HTML chain: img_c → img_a → img_b.
	pages := []string{"img_c", "img_a", "img_b"}
	path := makeKFXCBZSVG(t, pages)

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil for SVG-image KFX CBZ")
	}
	if len(ordered) != 3 {
		t.Fatalf("got %d images, want 3", len(ordered))
	}

	want := []string{
		"kfxtest_files/OEBPS/img_c.png",
		"kfxtest_files/OEBPS/img_a.png",
		"kfxtest_files/OEBPS/img_b.png",
	}
	for i, f := range ordered {
		if f.Name != want[i] {
			t.Errorf("ordered[%d] = %q, want %q", i, f.Name, want[i])
		}
	}
}

// makeKFXCBZHTMLPages creates a CBZ matching the Calibre KFX export style where
// page files use .html (not .xhtml) and live in a text/ subdirectory, with
// images in a sibling images/ directory referenced via SVG xlink:href.
func makeKFXCBZHTMLPages(t *testing.T, pages []string) string {
	t.Helper()

	const filesDir = "kfxtest_files/"
	const textDir = filesDir + "text/"
	const imagesDir = filesDir + "images/"

	pageFor := func(imgName, nextPage string) string {
		next := ""
		if nextPage != "" {
			next = `<a href="` + nextPage + `" class="calibreANext">next page</a>`
		}
		return `<html><body>` +
			`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">` +
			`<image xlink:href="../images/` + imgName + `" width="100" height="200"/>` +
			`</svg>` +
			next +
			`</body></html>`
	}

	var entries []zipEntry

	firstPage := ""
	if len(pages) > 0 {
		firstPage = textDir + "part" + pages[0] + ".html"
	}
	rootHTML := `<html><body><a href="` + firstPage + `" class="calibreANext">start</a></body></html>`
	entries = append(entries, zipEntry{"kfxtest.html", []byte(rootHTML)})

	for i, img := range pages {
		pageName := "part" + img + ".html"
		nextPage := ""
		if i+1 < len(pages) {
			nextPage = "part" + pages[i+1] + ".html"
		}
		entries = append(entries, zipEntry{textDir + pageName, []byte(pageFor(img+".png", nextPage))})
		entries = append(entries, zipEntry{imagesDir + img + ".png", portrait})
	}

	return makeCBZ(t, entries)
}

// TestKFXPageOrder_HTMLPages verifies that kfxPageOrder follows .html page
// chains (not just .xhtml), matching the text/partNNNN.html export style.
func TestKFXPageOrder_HTMLPages(t *testing.T) {
	// Natural sort: p_a, p_b, p_c. HTML chain: p_c → p_a → p_b.
	pages := []string{"c", "a", "b"}
	path := makeKFXCBZHTMLPages(t, pages)

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil for .html-page KFX CBZ")
	}
	if len(ordered) != 3 {
		t.Fatalf("got %d images, want 3", len(ordered))
	}

	want := []string{
		"kfxtest_files/images/c.png",
		"kfxtest_files/images/a.png",
		"kfxtest_files/images/b.png",
	}
	for i, f := range ordered {
		if f.Name != want[i] {
			t.Errorf("ordered[%d] = %q, want %q", i, f.Name, want[i])
		}
	}
}

// TestKFXPageOrder_NonKFX verifies that kfxPageOrder returns nil for a
// regular CBZ (no HTML file).
func TestKFXPageOrder_NonKFX(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
	})
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	if got := kfxPageOrder(&zr.Reader); got != nil {
		t.Errorf("kfxPageOrder returned non-nil for non-KFX CBZ: %v", got)
	}
}

// TestKFXPageOrder_CoverUnlinked verifies that a cover.jpeg at *_files/ root
// that is NOT referenced by any XHTML page (ONIMAI pattern) is promoted to
// position 0 of the ordered list.
func TestKFXPageOrder_CoverUnlinked(t *testing.T) {
	cbzPath := makeKFXCBZWithCover(t, []string{"p1", "p2", "p3"}, false)

	zr, err := zip.OpenReader(cbzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil")
	}
	if len(ordered) != 4 {
		t.Fatalf("got %d images, want 4", len(ordered))
	}
	if filepath.Base(ordered[0].Name) != "cover.jpeg" {
		t.Errorf("ordered[0] = %q, want cover.jpeg", ordered[0].Name)
	}
}

// TestKFXPageOrder_CoverLinkedAtEnd verifies that a cover.jpeg linked from the
// last XHTML page (vol-11 pattern) is moved from the end to position 0.
func TestKFXPageOrder_CoverLinkedAtEnd(t *testing.T) {
	cbzPath := makeKFXCBZWithCover(t, []string{"p1", "p2", "p3"}, true)

	zr, err := zip.OpenReader(cbzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil")
	}
	if len(ordered) != 4 {
		t.Fatalf("got %d images, want 4 (3 pages + cover); got %v",
			len(ordered), func() []string {
				var ns []string
				for _, f := range ordered {
					ns = append(ns, f.Name)
				}
				return ns
			}())
	}
	if filepath.Base(ordered[0].Name) != "cover.jpeg" {
		t.Errorf("ordered[0] = %q, want cover.jpeg", ordered[0].Name)
	}
	// cover must not also appear at the end
	if filepath.Base(ordered[len(ordered)-1].Name) == "cover.jpeg" {
		t.Error("cover.jpeg appears at both first and last positions")
	}
}

// TestKFXPageOrder_CoverDuplicatedAsFirstPage verifies that when the cover is
// stored both as *_files/cover.* and as a byte-identical first reading page
// under a different name (Calibre AZW3 export pattern), the duplicate page is
// dropped and only the promoted cover remains at position 0.
func TestKFXPageOrder_CoverDuplicatedAsFirstPage(t *testing.T) {
	const filesDir = "kfxtest_files/OEBPS/"

	xhtmlFor := func(imgName, nextPage string) string {
		next := ""
		if nextPage != "" {
			next = `<a href="` + nextPage + `" class="calibreANext">next page</a>`
		}
		return `<html><body><img src="` + imgName + `" alt="page"/>` + next + `</body></html>`
	}

	coverBytes := makePNGShade(100, 200, 42)
	pages := []string{"00001", "00002", "00003"}

	var entries []zipEntry
	entries = append(entries, zipEntry{"kfxtest.html", []byte(
		`<html><body><a href="` + filesDir + `page_` + pages[0] + `.xhtml" class="calibreANext">start</a></body></html>`,
	)})
	for i, img := range pages {
		nextXHTML := ""
		if i+1 < len(pages) {
			nextXHTML = "page_" + pages[i+1] + ".xhtml"
		}
		entries = append(entries, zipEntry{filesDir + "page_" + img + ".xhtml", []byte(xhtmlFor(img+".jpeg", nextXHTML))})
		// First page is byte-identical to the cover; the rest are distinct.
		data := makePNGShade(100, 200, uint8(i+1))
		if i == 0 {
			data = coverBytes
		}
		entries = append(entries, zipEntry{filesDir + img + ".jpeg", data})
	}
	// cover.jpeg at *_files/ root, byte-identical to images/00001.jpeg.
	entries = append(entries, zipEntry{"kfxtest_files/cover.jpeg", coverBytes})

	path := makeCBZ(t, entries)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	ordered := kfxPageOrder(&zr.Reader)
	if ordered == nil {
		t.Fatal("kfxPageOrder returned nil")
	}
	// 3 pages, but page 1 duplicates the cover → cover + 2 unique pages = 3.
	if len(ordered) != 3 {
		var ns []string
		for _, f := range ordered {
			ns = append(ns, f.Name)
		}
		t.Fatalf("got %d images, want 3 (cover + 2 unique pages); got %v", len(ordered), ns)
	}
	if filepath.Base(ordered[0].Name) != "cover.jpeg" {
		t.Errorf("ordered[0] = %q, want cover.jpeg", ordered[0].Name)
	}
	for _, f := range ordered[1:] {
		if filepath.Base(f.Name) == "00001.jpeg" {
			t.Error("byte-identical duplicate 00001.jpeg should have been removed")
		}
	}
}

// TestProcessCBZ_KFXOrdering verifies that processCBZ renames pages using the
// HTML navigation order rather than the natural sort of image filenames.
func TestProcessCBZ_KFXOrdering(t *testing.T) {
	// HTML chain: img_c → img_a → img_b (all portrait → blank inserted after cover).
	path := makeKFXCBZ(t, []string{"img_c", "img_a", "img_b"})

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}

	names := readCBZNames(t, path)

	// cover should be img_c (first in chain), not img_a (first alphabetically).
	if !containsEntry(names, "cover.png") {
		t.Error("expected cover.png in output")
	}
	// p001 is blank, p002 = img_a, p003 = img_b.
	for _, want := range []string{"p001.png", "p002.png", "p003.png", "ComicInfo.xml"} {
		if !containsEntry(names, want) {
			t.Errorf("missing %q in output; got %v", want, names)
		}
	}
	// Original KFX XHTML and HTML entries are not images and are dropped.
	for _, old := range []string{"kfxtest_files/OEBPS/img_a.png", "kfxtest_files/OEBPS/img_b.png", "kfxtest_files/OEBPS/img_c.png"} {
		if containsEntry(names, old) {
			t.Errorf("original KFX entry %q should have been removed", old)
		}
	}
}

// TestUpdateCoverCBZ_DryRun verifies that dry-run leaves the file unchanged.
func TestUpdateCoverCBZ_DryRun(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"cover.png", makePNG(100, 150)},
		{"p001.png", makePNG(100, 150)},
	})
	before := readCBZNames(t, path)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "cover.jpg"), makePNG(200, 300), 0644); err != nil {
		t.Fatal(err)
	}

	skipped, err := updateCoverCBZ(path, true, false, true, nil)
	if err != nil {
		t.Fatalf("updateCoverCBZ dry-run: %v", err)
	}
	if skipped {
		t.Error("expected skipped=false (would update) in dry-run")
	}
	after := readCBZNames(t, path)
	if len(before) != len(after) {
		t.Errorf("dry-run changed file: before=%v after=%v", before, after)
	}
}

// TestProcessCBZ_NeedsFixing verifies that a CBZ requiring processing returns
// skipped=false.
func TestProcessCBZ_NeedsFixing(t *testing.T) {
	path := makeCBZ(t, []zipEntry{
		{"001.png", portrait},
		{"002.png", portrait},
	})

	skipped, err := processCBZ(path, true, false, false, true, false) // dry-run
	if err != nil {
		t.Fatalf("processCBZ: %v", err)
	}
	if skipped {
		t.Error("expected skipped=false for CBZ that needs fixing")
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

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
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
