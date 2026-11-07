package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
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
//	magick <format>:<in> -background white -flatten <out>.png
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
		return nil, errUnrecognisedFormat
	}
	if _, err := exec.LookPath(magickBin); err != nil {
		return nil, fmt.Errorf("ImageMagick %q not found on PATH: %w", magickBin, err)
	}
	cmd := exec.Command(magickBin, magickArgs(coder)...)
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

// errUnrecognisedFormat is a page that is neither JPEG/PNG nor a format
// magickToPNG converts.
var errUnrecognisedFormat = errors.New("unrecognised image format (converts JPEG XL, WebP, GIF, BMP, TIFF, AVIF, HEIC, JPEG 2000)")

// magickArgs is the magick argv converting coder-format stdin to PNG on stdout.
func magickArgs(coder string) []string {
	return []string{coder + ":-", "-background", "white", "-flatten", "png:-"}
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
	case bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cjP  \r\n\x87\n")):
		return "jp2"
	case bytes.HasPrefix(b, []byte{0xFF, 0x4F, 0xFF, 0x51}):
		return "j2k"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return heifCoder(b)
	}
	return ""
}

// heifCoder returns the coder for an ISO-BMFF file whose ftyp box (at the start
// of b) names an AVIF or HEIF brand, as its major or a compatible brand.
func heifCoder(b []byte) string {
	end := int(binary.BigEndian.Uint32(b))
	if end > len(b) {
		end = len(b)
	}
	brands := []string{string(b[8:12])}
	for i := 16; i+4 <= end; i += 4 {
		brands = append(brands, string(b[i:i+4]))
	}
	coder := ""
	for _, brand := range brands {
		switch brand {
		case "avif", "avis":
			return "avif"
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "mif2", "miaf", "avci":
			coder = "heic"
		}
	}
	return coder
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
// be converted to PNG. A format magickToPNG can't convert is
// errUnrecognisedFormat here, so a dry run fails the same way a real one does.
func needsPNGConversion(f *zip.File) (bool, error) {
	rc, err := f.Open()
	if err != nil {
		return false, err
	}
	defer rc.Close()
	buf := make([]byte, 256) // room for an ftyp box's compatible brands
	n, err := io.ReadFull(rc, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	head := buf[:n]
	if isJPEGorPNG(head) {
		return false, nil
	}
	if imageCoder(head) == "" {
		return false, errUnrecognisedFormat
	}
	return true, nil
}
