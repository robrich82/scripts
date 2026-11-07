package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// magickBin is the ImageMagick v7 CLI used to convert non-JPEG/PNG pages.
const magickBin = "magick"

// convertImage converts arbitrary image bytes into a white-flattened PNG.
// It is a package-level variable so tests can substitute a stub that does not
// depend on ImageMagick being installed.
var convertImage = magickToPNG

// magickToPNG shells out to ImageMagick, mirroring the command
//
//	magick <in> -background white -flatten <out>.png
//
// but streaming the source in via stdin and the PNG back out via stdout so no
// temporary files are needed. -flatten composites any transparency or layers
// onto a solid white background, matching how these pages should render.
//
// The input format is named explicitly: left to guess, ImageMagick also reads
// SVG and similar formats that can pull in local files or URLs.
func magickToPNG(src []byte) ([]byte, error) {
	coder := imageCoder(src)
	if coder == "" {
		return nil, fmt.Errorf("unrecognised image format (converts JPEG XL, WebP, GIF, BMP, TIFF, AVIF, HEIC)")
	}
	if _, err := exec.LookPath(magickBin); err != nil {
		return nil, fmt.Errorf("ImageMagick %q not found on PATH: %w", magickBin, err)
	}
	cmd := exec.Command(magickBin, coder+":-", "-background", "white", "-flatten", "png:-")
	cmd.Stdin = bytes.NewReader(src)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("magick: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("magick: %w", err)
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("magick produced no output")
	}
	return out.Bytes(), nil
}

// imageCoder returns the ImageMagick coder for b's raster format, judged by its
// signature, or "" if it isn't one magickToPNG converts.
func imageCoder(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(b, []byte("BM")):
		return "bmp"
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")),
		bytes.HasPrefix(b, []byte("II+\x00")), bytes.HasPrefix(b, []byte("MM\x00+")): // BigTIFF
		return "tiff"
	case bytes.HasPrefix(b, []byte{0xFF, 0x0A}),
		bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")):
		return "jxl"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch string(b[8:12]) {
		case "avif", "avis":
			return "avif"
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "heic"
		}
	}
	return ""
}

// isJPEGorPNG reports whether b begins with a JPEG or PNG signature.
func isJPEGorPNG(b []byte) bool {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return true // JPEG: FF D8 FF
	}
	return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) // PNG
}

// needsPNGConversion reports whether image entry f is stored in a format other
// than JPEG or PNG (judged by content, not extension) and therefore needs to
// be converted to PNG.
func needsPNGConversion(f *zip.File) (bool, error) {
	rc, err := f.Open()
	if err != nil {
		return false, err
	}
	defer rc.Close()
	buf := make([]byte, 8)
	n, err := io.ReadFull(rc, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	return !isJPEGorPNG(buf[:n]), nil
}
