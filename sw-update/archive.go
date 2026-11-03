package main

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// errArchivePassword marks a failure to decrypt an archive: no archive_password
// set, a wrong one, or data too corrupt to tell the difference.
var errArchivePassword = errors.New("archive is password-protected")

// errChecksum marks extracted data that doesn't match the archive's CRC-32.
var errChecksum = errors.New("checksum mismatch")

const (
	zipFlagEncrypted       = 0x1
	zipFlagDataDescriptor  = 0x8
	zipFlagStrongEncrypted = 0x40
	zipMethodAES           = 99
)

// passwordErr wraps cause as errArchivePassword, with a hint that depends on
// whether a password was configured.
func passwordErr(password string, cause error) error {
	if password == "" {
		return fmt.Errorf("%w: set archive_password for this app in sw-update.json: %w", errArchivePassword, cause)
	}
	return fmt.Errorf("%w: archive_password is wrong or the archive is corrupt: %w", errArchivePassword, cause)
}

// extractArchive extracts a .zip or .7z archive to destDir, decrypting it with
// password if it is encrypted.
func extractArchive(path, destDir, password string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".zip":
		return extractZip(path, destDir, password)
	case ".7z":
		return extract7z(path, destDir, password)
	}
	return fmt.Errorf("unsupported archive type: %s", filepath.Base(path))
}

func extractZip(zipPath, destDir, password string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	fmt.Printf("  Extracting %s...\n", filepath.Base(zipPath))

	for _, f := range r.File {
		err := writeEntry(destDir, f.Name, f.FileInfo().IsDir(), func() (io.ReadCloser, error) {
			return openZipEntry(f, password)
		})
		if errors.Is(err, zip.ErrAlgorithm) {
			err = fmt.Errorf("%w (method %d)", err, f.Method)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

func extract7z(archivePath, destDir, password string) error {
	r, err := sevenzip.OpenReaderWithPassword(archivePath, password)
	if err != nil {
		return fmt.Errorf("open 7z %s: %w", archivePath, sevenZipErr(err, password))
	}
	defer r.Close()

	fmt.Printf("  Extracting %s...\n", filepath.Base(archivePath))

	for _, f := range r.File {
		err := writeEntry(destDir, f.Name, f.FileInfo().IsDir(), func() (io.ReadCloser, error) {
			rc, err := f.Open()
			// sevenzip doesn't verify file CRCs, and AES has no password check
			// value, so a wrong key on uncompressed data would read as garbage.
			// No CRC recorded reads as 0, so only check non-empty files with one.
			if err != nil || f.UncompressedSize == 0 || f.CRC32 == 0 {
				return rc, err
			}
			return &crcCheckReader{rc: rc, want: f.CRC32}, nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, sevenZipErr(err, password))
		}
	}
	return nil
}

// sevenZipErr reports decode failures in an encrypted 7z, and CRC mismatches
// when a password was given, as password problems. A CRC mismatch with no
// password can't be told apart from corruption, so it says both.
func sevenZipErr(err error, password string) error {
	var rpe *sevenzip.ReadError
	if errors.As(err, &rpe) && rpe.Encrypted {
		return passwordErr(password, err)
	}
	if errors.Is(err, errChecksum) {
		if password != "" {
			return passwordErr(password, err)
		}
		return fmt.Errorf("%w (corrupt archive, or encrypted and archive_password not set)", err)
	}
	return err
}

// writeEntry writes one archive entry under destDir, rejecting names that
// would escape it. A partially written file is removed on failure.
func writeEntry(destDir, name string, isDir bool, open func() (io.ReadCloser, error)) error {
	if !filepath.IsLocal(name) || filepath.Clean(name) == "." {
		return fmt.Errorf("unsafe path in archive: %q", name)
	}
	destPath := filepath.Join(destDir, name)

	if isDir {
		return os.MkdirAll(destPath, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	return nil
}

// openZipEntry opens a zip entry, decrypting it if it is ZipCrypto-encrypted.
func openZipEntry(f *zip.File, password string) (io.ReadCloser, error) {
	if f.Flags&zipFlagEncrypted == 0 {
		return f.Open()
	}
	if f.Method == zipMethodAES || f.Flags&zipFlagStrongEncrypted != 0 {
		return nil, errors.New("AES/strong-encrypted zip entries are not supported; extract it manually")
	}
	if password == "" {
		return nil, passwordErr(password, errors.New("entry is encrypted"))
	}

	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	// The 12-byte encryption header's last byte decrypts to the CRC's high byte,
	// or with a data descriptor (per Info-ZIP) the DOS mod time's; some writers
	// use the CRC either way. The CRC check at EOF is the real password test.
	checks := []byte{byte(f.CRC32 >> 24)}
	if f.Flags&zipFlagDataDescriptor != 0 {
		checks = append(checks, byte(f.ModifiedTime>>8))
	}
	dec, err := newZipCryptoReader(raw, password, checks)
	if err != nil {
		return nil, err
	}

	var body io.ReadCloser
	switch f.Method {
	case zip.Store:
		body = io.NopCloser(dec)
	case zip.Deflate:
		body = flate.NewReader(dec)
	default:
		return nil, fmt.Errorf("%w (method %d)", zip.ErrAlgorithm, f.Method)
	}
	return &crcCheckReader{rc: body, want: f.CRC32, password: password, encrypted: true}, nil
}

// zipCryptoReader decrypts the traditional PKWARE ("ZipCrypto") stream cipher.
type zipCryptoReader struct {
	r    io.Reader
	keys [3]uint32
}

// newZipCryptoKeys returns a cipher keyed with password. Passwords are used as
// raw bytes, so only ASCII ones are portable between zip tools.
func newZipCryptoKeys(password string) *zipCryptoReader {
	z := &zipCryptoReader{keys: [3]uint32{0x12345678, 0x23456789, 0x34567890}}
	for _, b := range []byte(password) {
		z.update(b)
	}
	return z
}

func newZipCryptoReader(r io.Reader, password string, checks []byte) (*zipCryptoReader, error) {
	z := newZipCryptoKeys(password)
	z.r = r
	var hdr [12]byte
	if _, err := io.ReadFull(z, hdr[:]); err != nil {
		return nil, fmt.Errorf("reading encryption header: %w", err)
	}
	for _, c := range checks {
		if hdr[11] == c {
			return z, nil
		}
	}
	return nil, passwordErr(password, errors.New("encryption header check failed"))
}

func (z *zipCryptoReader) update(b byte) {
	z.keys[0] = crc32.IEEETable[byte(z.keys[0])^b] ^ (z.keys[0] >> 8)
	z.keys[1] = (z.keys[1]+(z.keys[0]&0xff))*134775813 + 1
	z.keys[2] = crc32.IEEETable[byte(z.keys[2])^byte(z.keys[1]>>24)] ^ (z.keys[2] >> 8)
}

// keystream returns the next byte to XOR with the data.
func (z *zipCryptoReader) keystream() byte {
	t := uint16(z.keys[2]) | 2
	return byte((uint32(t) * uint32(t^1)) >> 8)
}

func (z *zipCryptoReader) Read(p []byte) (int, error) {
	n, err := z.r.Read(p)
	for i := range p[:n] {
		p[i] ^= z.keystream()
		z.update(p[i])
	}
	return n, err
}

// crcCheckReader verifies data against an expected CRC-32 at EOF, for readers
// that skip it (zip OpenRaw, sevenzip). For encrypted zip entries it is the
// reliable wrong-password test, as the header check passes 1 in 256 wrong keys.
type crcCheckReader struct {
	rc        io.ReadCloser
	want, crc uint32
	encrypted bool
	password  string
}

func (c *crcCheckReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.crc = crc32.Update(c.crc, crc32.IEEETable, p[:n])
	var pathErr *fs.PathError
	switch {
	case err == io.EOF && c.crc != c.want:
		if c.encrypted {
			return n, passwordErr(c.password, errChecksum)
		}
		return n, errChecksum
	case c.encrypted && err != nil && err != io.EOF && !errors.As(err, &pathErr):
		// Data decrypted with the wrong key usually fails to inflate first.
		return n, passwordErr(c.password, err)
	}
	return n, err
}

func (c *crcCheckReader) Close() error { return c.rc.Close() }
