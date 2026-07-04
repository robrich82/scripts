package main

import (
	"bytes"
	"image"
	"testing"
)

// fakeNonImage returns bytes that are neither a JPEG nor a PNG (a stand-in for
// a WebP/BMP/etc. page). Only the leading signature is inspected during
// detection, so the payload can be arbitrary.
func fakeNonImage() []byte {
	return append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
}

func TestIsJPEGorPNG(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true},
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), true},
		{"webp", fakeNonImage(), false},
		{"gif", []byte("GIF89a"), false},
		{"empty", nil, false},
		{"short", []byte{0xFF, 0xD8}, false},
	}
	for _, tc := range cases {
		if got := isJPEGorPNG(tc.data); got != tc.want {
			t.Errorf("isJPEGorPNG(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestProcessCBZ_ConvertsNonJPEGPNG verifies that pages stored in a format
// other than JPEG/PNG are converted to PNG (via the stubbed converter),
// renamed with a .png extension, and that the originals are dropped.
func TestProcessCBZ_ConvertsNonJPEGPNG(t *testing.T) {
	restore := convertImage
	var calls int
	convertImage = func(src []byte) ([]byte, error) {
		calls++
		return portrait, nil // 100x200 PNG
	}
	defer func() { convertImage = restore }()

	path := makeCBZ(t, []zipEntry{
		{"001.webp", fakeNonImage()}, // cover, needs conversion
		{"002.webp", fakeNonImage()}, // page, needs conversion
		{"003.png", portrait},        // already PNG, raw-copied
	})

	if _, err := processCBZ(path, false, false, false, true, false); err != nil {
		t.Fatalf("processCBZ: %v", err)
	}
	if calls != 2 {
		t.Errorf("convertImage called %d times, want 2", calls)
	}

	names := readCBZNames(t, path)
	for _, want := range []string{"cover.png", "p001.png", "p002.png", "p003.png", "ComicInfo.xml"} {
		if !containsEntry(names, want) {
			t.Errorf("missing %q in output; got %v", want, names)
		}
	}
	// The original .webp names must be gone.
	for _, old := range []string{"001.webp", "002.webp"} {
		if containsEntry(names, old) {
			t.Errorf("original entry %q should have been removed", old)
		}
	}

	// The converted cover must be a valid, decodable PNG of the expected size.
	cover := readCBZEntry(t, path, "cover.png")
	img, format, err := image.Decode(bytes.NewReader(cover))
	if err != nil {
		t.Fatalf("decode converted cover: %v", err)
	}
	if format != "png" {
		t.Errorf("converted cover format = %q, want png", format)
	}
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 200 {
		t.Errorf("converted cover dims = %dx%d, want 100x200", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestProcessCBZ_ConvertDryRunSkipsMagick verifies that --dry-run detects pages
// needing conversion but never invokes the converter and leaves the file alone.
func TestProcessCBZ_ConvertDryRunSkipsMagick(t *testing.T) {
	restore := convertImage
	var calls int
	convertImage = func(src []byte) ([]byte, error) {
		calls++
		return portrait, nil
	}
	defer func() { convertImage = restore }()

	path := makeCBZ(t, []zipEntry{
		{"001.webp", fakeNonImage()},
		{"002.webp", fakeNonImage()},
	})
	before := readCBZNames(t, path)

	skipped, err := processCBZ(path, true, false, false, true, false) // dry-run
	if err != nil {
		t.Fatalf("processCBZ dry-run: %v", err)
	}
	if skipped {
		t.Error("expected skipped=false for a CBZ that needs conversion")
	}
	if calls != 0 {
		t.Errorf("convertImage called %d times in dry-run, want 0", calls)
	}
	if after := readCBZNames(t, path); len(before) != len(after) {
		t.Errorf("dry-run changed file: before=%v after=%v", before, after)
	}
}

// TestProcessCBZ_ForceConvertsLeftoverNonPNG covers the motivating case: an
// already-processed archive still contains a non-PNG page (e.g. from an older
// run). --force reprocesses it, converting the leftover page to PNG without
// adding a second blank.
func TestProcessCBZ_ForceConvertsLeftoverNonPNG(t *testing.T) {
	restore := convertImage
	var calls int
	convertImage = func(src []byte) ([]byte, error) {
		calls++
		return portrait, nil
	}
	defer func() { convertImage = restore }()

	path := makeCBZ(t, []zipEntry{
		{"cover.png", portrait},
		{"p001.png", portrait},        // existing blank
		{"p002.webp", fakeNonImage()}, // leftover non-PNG page
	})

	// Without --force it would be skipped.
	skipped, err := processCBZ(path, false, false, false, true, false)
	if err != nil {
		t.Fatalf("processCBZ (no force): %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true without --force")
	}
	if calls != 0 {
		t.Errorf("convertImage called %d times without --force, want 0", calls)
	}

	// With --force the leftover page is converted; no second blank is added.
	skipped, err = processCBZ(path, false, false, false, true, true)
	if err != nil {
		t.Fatalf("processCBZ (force): %v", err)
	}
	if skipped {
		t.Error("expected skipped=false with --force")
	}
	if calls != 1 {
		t.Errorf("convertImage called %d times with --force, want 1", calls)
	}

	names := readCBZNames(t, path)
	for _, want := range []string{"cover.png", "p001.png", "p002.png", "ComicInfo.xml"} {
		if !containsEntry(names, want) {
			t.Errorf("missing %q in output; got %v", want, names)
		}
	}
	if containsEntry(names, "p002.webp") {
		t.Error("leftover p002.webp should have been converted away")
	}
	if containsEntry(names, "p003.png") {
		t.Errorf("force must not insert a second blank page; got %v", names)
	}
}

// TestProcessCBZ_ConvertError propagates a converter failure as a fatal error
// so the file is left untouched.
func TestProcessCBZ_ConvertError(t *testing.T) {
	restore := convertImage
	convertImage = func(src []byte) ([]byte, error) {
		return nil, errConvert
	}
	defer func() { convertImage = restore }()

	path := makeCBZ(t, []zipEntry{
		{"001.webp", fakeNonImage()},
		{"002.png", portrait},
	})

	if _, err := processCBZ(path, false, false, false, true, false); err == nil {
		t.Fatal("expected error when conversion fails, got nil")
	}
}

var errConvert = &convertErr{}

type convertErr struct{}

func (*convertErr) Error() string { return "boom" }
