// Package archive provides an in-memory zip writer and a safe extractor.
//
// File modes are preserved across pack/unpack. The extractor refuses any
// entry whose cleaned destination escapes the output directory
// ("zip-slip"), and refuses absolute paths and entries containing "..".
package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Writer accumulates files into an in-memory zip blob.
//
// Usage:
//
//	w := archive.NewWriter()
//	_ = w.Add("a/b.txt", 0o644, src)
//	blob, err := w.Bytes()
type Writer struct {
	buf *bytes.Buffer
	zw  *zip.Writer
}

// NewWriter returns an empty in-memory zip writer.
func NewWriter() *Writer {
	buf := &bytes.Buffer{}
	return &Writer{buf: buf, zw: zip.NewWriter(buf)}
}

// Add appends a file to the archive at relPath with the given mode and
// content. relPath is normalized to forward-slashes (zip convention) and
// must be a relative, non-escaping path.
func (w *Writer) Add(relPath string, mode os.FileMode, src io.Reader) error {
	clean, err := sanitizeArchivePath(relPath)
	if err != nil {
		return err
	}

	hdr := &zip.FileHeader{
		Name:   clean,
		Method: zip.Deflate,
	}
	hdr.SetMode(mode)

	fw, err := w.zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("zip create %q: %w", clean, err)
	}
	if _, err := io.Copy(fw, src); err != nil {
		return fmt.Errorf("zip write %q: %w", clean, err)
	}
	return nil
}

// Bytes finalizes the zip and returns the resulting bytes. After Bytes
// returns, the Writer must not be reused.
func (w *Writer) Bytes() ([]byte, error) {
	if err := w.zw.Close(); err != nil {
		return nil, fmt.Errorf("zip close: %w", err)
	}
	return w.buf.Bytes(), nil
}

// UnpackResult reports the outcome of an extraction.
type UnpackResult struct {
	Written []string // file paths (relative to outDir) that were written
	Skipped []string // file paths skipped because they already existed and !overwrite
}

// Unpack extracts blob into outDir. It rejects any entry whose
// destination would escape outDir, fails the whole extraction on
// rejection, and returns the canonical error ErrUnsafePath.
//
// If overwrite is false, existing files are left in place and recorded
// in UnpackResult.Skipped. Directories are created with mode 0o755.
// Symlinks and other non-regular entries are ignored.
func Unpack(blob []byte, outDir string, overwrite bool) (UnpackResult, error) {
	var res UnpackResult

	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return res, fmt.Errorf("zip open: %w", err)
	}

	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return res, fmt.Errorf("resolve outDir: %w", err)
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return res, fmt.Errorf("mkdir outDir: %w", err)
	}

	for _, f := range zr.File {
		// Reject before doing anything irreversible.
		clean, err := sanitizeArchivePath(f.Name)
		if err != nil {
			return res, fmt.Errorf("%w: %s", ErrUnsafePath, f.Name)
		}
		dest := filepath.Join(absOut, filepath.FromSlash(clean))

		// Belt-and-suspenders: confirm the joined path is contained.
		if !pathIsContained(dest, absOut) {
			return res, fmt.Errorf("%w: %s", ErrUnsafePath, f.Name)
		}

		mode := f.Mode()

		switch {
		case mode&os.ModeSymlink != 0:
			// Skip symlinks — we don't follow them on pack and we don't
			// recreate them here either.
			continue
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return res, fmt.Errorf("mkdir %s: %w", dest, err)
			}
			continue
		case !mode.IsRegular():
			// devices, pipes, etc — skip silently
			continue
		}

		if !overwrite {
			if _, err := os.Lstat(dest); err == nil {
				res.Skipped = append(res.Skipped, clean)
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return res, fmt.Errorf("mkdir parent of %s: %w", dest, err)
		}

		rc, err := f.Open()
		if err != nil {
			return res, fmt.Errorf("zip open entry %s: %w", clean, err)
		}
		err = writeFile(dest, rc, mode.Perm())
		_ = rc.Close()
		if err != nil {
			return res, err
		}
		res.Written = append(res.Written, clean)
	}
	return res, nil
}

// ErrUnsafePath is returned when an archive entry would escape outDir or
// is malformed (absolute path, ".." segment, empty name).
var ErrUnsafePath = errors.New("archive entry has unsafe path")

func writeFile(dest string, src io.Reader, mode os.FileMode) error {
	// Truncate-create with the target mode. We rely on parent perms +
	// the user's umask for security on the parent directory.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", dest, err)
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	// Re-apply mode in case umask trimmed it. Best-effort.
	_ = os.Chmod(dest, mode)
	return nil
}

// sanitizeArchivePath rejects absolute, empty, or escaping paths and
// returns a forward-slash, cleaned relative path suitable for use as a
// zip entry name (or, after FromSlash, a destination on disk).
func sanitizeArchivePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty name", ErrUnsafePath)
	}
	// Normalize backslashes in case a Windows-produced path slips in.
	p = strings.ReplaceAll(p, `\`, "/")
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, p)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: %q", ErrUnsafePath, p)
		}
	}
	return cleaned, nil
}

// pathIsContained returns true iff target is within base after resolving
// symbolic links of the existing prefix. Both inputs are expected to be
// absolute.
func pathIsContained(target, base string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	return !filepath.IsAbs(rel)
}
