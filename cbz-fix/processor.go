package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pngenc "image/png"
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true, ".bmp": true,
	".tiff": true, ".tif": true, ".jxl": true,
	".avif": true, ".heic": true, ".heif": true,
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// entry describes one file that will be written into the new CBZ.
type entry struct {
	original *zip.File // source for a raw copy; nil for a blank or converted page
	origIdx  int       // index into the sorted imgs slice; -1 for blank
	newName  string
	data     []byte // populated for blank pages and converted (PNG) pages
}

// pageInfo holds the attributes we care about from a ComicInfo <Page> element.
type pageInfo struct {
	pageType      string
	bookmark      string
	doublePage    bool
	imageSize     int64
	width, height int
}

// processCBZ processes a single CBZ file. It returns (true, nil) when the file
// is already in the expected layout and no work is needed, (false, nil) on
// success (real or dry-run), and (false, err) on failure.
//
// When force is true, files that already look processed are reprocessed anyway
// (e.g. to convert leftover non-PNG pages). A blank page is still inserted for
// those files unless one is already there, so a file first processed with
// --no-blank can be fixed up later.
func processCBZ(path string, dryRun, verbose, noBlank, noBackup, force bool) (skipped bool, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false, fmt.Errorf("open: %w", err)
	}

	// ── Collect files ──────────────────────────────────────────────────────
	var ciFile *zip.File
	var imgs []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		if strings.EqualFold(base, "ComicInfo.xml") {
			ciFile = f
		} else if isImageFile(f.Name) {
			imgs = append(imgs, f)
		}
	}

	if len(imgs) == 0 {
		zr.Close()
		return false, fmt.Errorf("no image files found")
	}

	// ── Sort images ───────────────────────────────────────────────────────
	// For calibre KFX exports (root HTML + *_files/ tree) the XHTML
	// navigation chain defines reading order; image filenames are opaque
	// hashes that sort incorrectly by name.
	if kfxOrder := kfxPageOrder(&zr.Reader); kfxOrder != nil {
		imgs = kfxOrder
	} else {
		sort.Slice(imgs, func(i, j int) bool {
			return naturalLess(imgs[i].Name, imgs[j].Name)
		})
	}

	// ── Idempotency guard ──────────────────────────────────────────────────
	// If the first image is already "cover.*" and the second is "p001.*" the
	// file has been processed before. Skip it unless --force was given, in
	// which case reprocess it.
	coverBase := strings.ToLower(filepath.Base(imgs[0].Name))
	alreadyProcessed := strings.HasPrefix(coverBase, "cover.") &&
		len(imgs) > 1 && strings.HasPrefix(strings.ToLower(filepath.Base(imgs[1].Name)), "p001.")
	if alreadyProcessed {
		if !force {
			zr.Close()
			if verbose {
				fmt.Println("  already processed, skipping")
			}
			return true, nil
		}
		if verbose {
			fmt.Println("  already processed, reprocessing (--force)")
		}
	}

	// ── Parse ComicInfo.xml ────────────────────────────────────────────────
	var ci *ComicInfo
	oldInfo := make(map[int]pageInfo) // Image index → metadata
	if ciFile != nil {
		data, err := readZipEntry(ciFile)
		if err != nil {
			zr.Close()
			return false, fmt.Errorf("read ComicInfo.xml: %w", err)
		}
		ci, err = parseComicInfo(data)
		if err != nil {
			zr.Close()
			return false, fmt.Errorf("parse ComicInfo.xml: %w", err)
		}
		if ci.Pages != nil {
			for _, p := range ci.Pages.Pages {
				oldInfo[p.Image] = pageInfo{
					pageType:   p.Type,
					bookmark:   p.Bookmark,
					doublePage: p.DoublePage,
					imageSize:  p.ImageSize,
					width:      p.ImageWidth,
					height:     p.ImageHeight,
				}
			}
		}
	}

	// ── Convert non-JPEG/PNG pages to PNG ──────────────────────────────────
	// Any page whose stored bytes are not already JPEG or PNG is converted to a
	// white-flattened PNG via ImageMagick. In dry-run mode we only record which
	// pages *would* be converted (so the preview shows .png names) without
	// invoking ImageMagick.
	converted := make(map[int][]byte)
	needConv := make([]bool, len(imgs))
	for i, f := range imgs {
		need, err := needsPNGConversion(f)
		if err != nil {
			zr.Close()
			return false, fmt.Errorf("inspect %s: %w", f.Name, err)
		}
		if !need {
			continue
		}
		needConv[i] = true
		if dryRun {
			continue
		}
		data, err := readZipEntry(f)
		if err != nil {
			zr.Close()
			return false, fmt.Errorf("read %s: %w", f.Name, err)
		}
		png, err := convertImage(data)
		if err != nil {
			zr.Close()
			return false, fmt.Errorf("convert %s to PNG: %w", f.Name, err)
		}
		converted[i] = png
		// Refresh dimensions/size so ComicInfo and double-page detection use
		// the converted image rather than the (undecodable) original.
		if cfg, _, derr := image.DecodeConfig(bytes.NewReader(png)); derr == nil {
			pi := oldInfo[i]
			pi.width, pi.height = cfg.Width, cfg.Height
			pi.imageSize = int64(len(png))
			oldInfo[i] = pi
		}
	}

	// ── Find insertion point ───────────────────────────────────────────────
	// Scan from imgs[1] onward; insert blank before the first non-double page.
	insertBefore := -1
	if !noBlank {
		for i := 1; i < len(imgs); i++ {
			if !isDoublePage(imgs[i], converted[i], oldInfo, i) {
				insertBefore = i
				break
			}
		}
	}
	// On a --force reprocess the page at the insertion point is usually the
	// blank a previous run added; inserting another would duplicate it. Only
	// skip insertion when that page really is blank, so an archive first
	// processed with --no-blank still gets one.
	if alreadyProcessed && insertBefore >= 0 &&
		isBlankPage(imgs[insertBefore], converted[insertBefore]) {
		insertBefore = -1
	}

	// ── Create blank page ──────────────────────────────────────────────────
	var blankData []byte
	var blankExt string
	var blankW, blankH int
	if insertBefore >= 0 {
		blankW, blankH, blankExt, blankData, err = createBlankLike(imgs[insertBefore], converted[insertBefore])
		if err != nil {
			// Non-fatal: warn and skip blank insertion.
			fmt.Printf("  WARNING: could not create blank page (%v); skipping blank insertion\n", err)
			insertBefore = -1
		}
	}

	// ── Build entry list ───────────────────────────────────────────────────
	// mkImageEntry builds an entry for image i. Converted pages carry their PNG
	// bytes in data (written fresh); the rest are raw-copied from the source.
	mkImageEntry := func(i int, newName string) entry {
		if data := converted[i]; data != nil {
			return entry{origIdx: i, newName: newName, data: data}
		}
		return entry{original: imgs[i], origIdx: i, newName: newName}
	}
	imgExt := func(i string, needsConv bool) string {
		if needsConv {
			return ".png"
		}
		return strings.ToLower(filepath.Ext(i))
	}

	entries := []entry{mkImageEntry(0, "cover"+imgExt(imgs[0].Name, needConv[0]))}

	newIdx := 1
	for i := 1; i < len(imgs); i++ {
		if i == insertBefore {
			entries = append(entries, entry{
				origIdx: -1,
				newName: fmt.Sprintf("p%03d%s", newIdx, blankExt),
				data:    blankData,
			})
			newIdx++
		}
		entries = append(entries, mkImageEntry(i, fmt.Sprintf("p%03d%s", newIdx, imgExt(imgs[i].Name, needConv[i]))))
		newIdx++
	}

	// ── Verbose output ─────────────────────────────────────────────────────
	if verbose {
		for _, e := range entries {
			switch {
			case e.origIdx < 0:
				fmt.Printf("  [blank %dx%d] → %s\n", blankW, blankH, e.newName)
			case needConv[e.origIdx]:
				fmt.Printf("  %s → %s (converted to PNG)\n", imgs[e.origIdx].Name, e.newName)
			default:
				fmt.Printf("  %s → %s\n", imgs[e.origIdx].Name, e.newName)
			}
		}
	}

	if dryRun {
		zr.Close()
		return false, nil
	}

	// ── Build updated ComicInfo ────────────────────────────────────────────
	if ci == nil {
		ci = &ComicInfo{}
	}
	ci.PageCount = len(entries)
	ci.Pages = &ComicPages{Pages: buildPages(entries, oldInfo, blankW, blankH)}

	ciData, err := marshalComicInfo(ci)
	if err != nil {
		zr.Close()
		return false, fmt.Errorf("marshal ComicInfo.xml: %w", err)
	}

	// ── Write new zip to a temp file (zr still open for reads) ────────────
	tmpPath, err := writeNewCBZ(filepath.Dir(path), entries, ciData)
	zr.Close()
	if err != nil {
		return false, fmt.Errorf("write new CBZ: %w", err)
	}

	// ── Backup original ────────────────────────────────────────────────────
	if !noBackup {
		backupPath := path + ".bak"
		if err := copyFile(path, backupPath); err != nil {
			os.Remove(tmpPath)
			return false, fmt.Errorf("backup: %w", err)
		}
	}

	// ── Replace original with new zip ─────────────────────────────────────
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("replace original: %w", err)
	}

	return false, nil
}

// decodeConfig decodes the image config for entry f. When conv is non-nil (the
// page was converted to PNG) it is decoded from those bytes instead of opening
// the original — the source format may not be decodable by the standard library.
func decodeConfig(f *zip.File, conv []byte) (image.Config, string, error) {
	if conv != nil {
		return image.DecodeConfig(bytes.NewReader(conv))
	}
	rc, err := f.Open()
	if err != nil {
		return image.Config{}, "", err
	}
	defer rc.Close()
	return image.DecodeConfig(rc)
}

// isDoublePage returns true if the image at imgs[imgIdx] is a double-page spread.
// It checks ComicInfo metadata first, falling back to reading image dimensions.
// conv holds the converted PNG bytes when the page was converted, nil otherwise.
func isDoublePage(f *zip.File, conv []byte, oldInfo map[int]pageInfo, imgIdx int) bool {
	if pi, ok := oldInfo[imgIdx]; ok {
		if pi.doublePage || strings.EqualFold(pi.pageType, "DoublePage") {
			return true
		}
		if pi.width > 0 && pi.height > 0 {
			return pi.width > pi.height
		}
	}
	cfg, _, err := decodeConfig(f, conv)
	if err != nil {
		return false
	}
	return cfg.Width > cfg.Height
}

// isBlankPage reports whether every pixel of the image is (near) white — the
// signature of a blank page inserted by an earlier run. Scanned content is
// practically never uniform, so a false positive would take a page that is
// already blank in its own right, in which case skipping insertion is right.
// conv holds the converted PNG bytes when the page was converted, nil otherwise.
func isBlankPage(f *zip.File, conv []byte) bool {
	var (
		img image.Image
		err error
	)
	if conv != nil {
		img, _, err = image.Decode(bytes.NewReader(conv))
	} else {
		rc, oerr := f.Open()
		if oerr != nil {
			return false
		}
		defer rc.Close()
		img, _, err = image.Decode(rc)
	}
	if err != nil {
		return false
	}

	// Tolerance absorbs the rounding a JPEG round-trip applies to a flat white
	// image; 250/255 is still visually blank.
	const nearWhite = 250 * 0x101
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if r < nearWhite || g < nearWhite || bl < nearWhite || a < 0xffff {
				return false
			}
		}
	}
	return true
}

// createBlankLike creates a white image matching the dimensions of f.
// conv holds the converted PNG bytes when the page was converted, nil otherwise.
// Returns width, height, extension (with dot), encoded bytes, error.
func createBlankLike(f *zip.File, conv []byte) (w, h int, ext string, data []byte, err error) {
	cfg, format, decErr := decodeConfig(f, conv)
	if decErr != nil {
		return 0, 0, "", nil, fmt.Errorf("decode config %s: %w", f.Name, decErr)
	}
	w, h = cfg.Width, cfg.Height

	// Build a white grayscale image.
	img := newWhiteGray(w, h)

	var buf bytes.Buffer
	if format == "jpeg" {
		ext = ".jpg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	} else {
		ext = ".png"
		err = pngenc.Encode(&buf, img)
	}
	if err != nil {
		return 0, 0, "", nil, fmt.Errorf("encode blank: %w", err)
	}
	return w, h, ext, buf.Bytes(), nil
}

// newWhiteGray returns a white grayscale image of the given dimensions.
func newWhiteGray(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

// buildPages constructs the updated ComicInfo <Pages> list.
func buildPages(entries []entry, oldInfo map[int]pageInfo, blankW, blankH int) []ComicPage {
	pages := make([]ComicPage, len(entries))
	for newIdx, e := range entries {
		page := ComicPage{Image: newIdx}
		switch {
		case newIdx == 0:
			page.Type = "FrontCover"
			if pi, ok := oldInfo[e.origIdx]; ok {
				page.ImageWidth = pi.width
				page.ImageHeight = pi.height
				page.ImageSize = pi.imageSize
			}
		case e.origIdx < 0:
			// Blank page — no type, no double-page flag.
			page.ImageWidth = blankW
			page.ImageHeight = blankH
		default:
			if pi, ok := oldInfo[e.origIdx]; ok {
				page.Type = pi.pageType
				page.Bookmark = pi.bookmark
				page.DoublePage = pi.doublePage
				page.ImageWidth = pi.width
				page.ImageHeight = pi.height
				page.ImageSize = pi.imageSize
			}
		}
		pages[newIdx] = page
	}
	return pages
}

// writeNewCBZ writes entries (and ComicInfo.xml) into a new temp file.
// Returns the temp file path on success.
//
// All entries are written via CreateRaw so that CRC32 and sizes appear in
// the local file header rather than a trailing data descriptor (ZIP bit 3).
// 7-Zip's update handler cannot modify archives that contain data descriptors.
func writeNewCBZ(dir string, entries []entry, ciData []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, "cbzfix-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	zw := zip.NewWriter(tmp)

	cleanup := func() {
		zw.Close()
		tmp.Close()
		os.Remove(tmpPath)
	}

	for _, e := range entries {
		if e.original != nil {
			// Copy the source entry's raw compressed bytes directly.
			// Clearing bit 3 (data descriptor) lets us write CRC32 and sizes
			// in the local header — values come from the central directory,
			// which always has them even when bit 3 was set in the source.
			fh := e.original.FileHeader
			fh.Name = e.newName
			fh.Flags &^= 0x8
			w, err := zw.CreateRaw(&fh)
			if err != nil {
				cleanup()
				return "", fmt.Errorf("create entry %s: %w", e.newName, err)
			}
			rc, err := e.original.OpenRaw()
			if err != nil {
				cleanup()
				return "", fmt.Errorf("open raw source entry %s: %w", e.original.Name, err)
			}
			_, cpErr := io.Copy(w, rc)
			if cpErr != nil {
				cleanup()
				return "", fmt.Errorf("copy entry %s: %w", e.original.Name, cpErr)
			}
		} else {
			if err := writeStored(zw, e.newName, e.data); err != nil {
				cleanup()
				return "", fmt.Errorf("write blank page: %w", err)
			}
		}
	}

	if err := writeStored(zw, "ComicInfo.xml", ciData); err != nil {
		cleanup()
		return "", fmt.Errorf("write ComicInfo.xml: %w", err)
	}

	if err := zw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("close zip writer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

// writeStored writes data as an uncompressed (Store) zip entry using CreateRaw
// with all header fields pre-populated, ensuring no data descriptor (bit 3).
func writeStored(zw *zip.Writer, name string, data []byte) error {
	fh := &zip.FileHeader{
		Name:               name,
		Method:             zip.Store,
		Modified:           time.Now(),
		CRC32:              crc32.ChecksumIEEE(data),
		UncompressedSize64: uint64(len(data)),
		CompressedSize64:   uint64(len(data)),
	}
	w, err := zw.CreateRaw(fh)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()
	_, err = io.Copy(df, sf)
	return err
}
