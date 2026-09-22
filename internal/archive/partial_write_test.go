package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// badCRCZip builds a structurally valid zip whose single entry carries a
// wrong CRC32. archive/zip verifies the checksum at EOF, so io.Copy in
// writeFile fails *after* the bytes have already reached the disk.
func badCRCZip(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.DefaultCompression)
	fw.Write(payload)
	fw.Close()

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	hdr := &zip.FileHeader{
		Name:               name,
		Method:             zip.Deflate,
		CRC32:              0xDEADBEEF, // deliberately wrong
		CompressedSize64:   uint64(comp.Len()),
		UncompressedSize64: uint64(len(payload)),
	}
	hdr.SetMode(0o600)
	w, err := zw.CreateRaw(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(comp.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnpackLeavesNoFileOnCopyError(t *testing.T) {
	out := t.TempDir()
	blob := badCRCZip(t, "app.env", bytes.Repeat([]byte("SECRET=1\n"), 100))

	if _, err := Unpack(blob, out, false); err == nil {
		t.Fatal("Unpack of a corrupt entry = nil error, want error")
	}
	dest := filepath.Join(out, "app.env")
	if b, err := os.ReadFile(dest); err == nil {
		t.Errorf("Unpack failed but left %s on disk (%d bytes)", dest, len(b))
	}
}

// With --overwrite, O_TRUNC destroys the existing file before the new
// content is written. A mid-write failure must not cost the original.
func TestUnpackPreservesOriginalOnCopyError(t *testing.T) {
	out := t.TempDir()
	dest := filepath.Join(out, "app.env")
	original := []byte("ORIGINAL=keep-me\n")
	if err := os.WriteFile(dest, original, 0o600); err != nil {
		t.Fatal(err)
	}

	blob := badCRCZip(t, "app.env", bytes.Repeat([]byte("SECRET=1\n"), 100))
	if _, err := Unpack(blob, out, true /* overwrite */); err == nil {
		t.Fatal("Unpack of a corrupt entry = nil error, want error")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("original file is gone: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("original clobbered: got %q, want %q", got, original)
	}
}

// No temp file may survive either outcome. The pattern is only
// acceptable if the strays are confined to a hard kill.
func TestUnpackLeavesNoTempFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"successful extraction", func() []byte {
			w := NewWriter()
			if err := w.Add("app.env", 0o600, bytes.NewReader([]byte("OK=1\n"))); err != nil {
				t.Fatal(err)
			}
			b, err := w.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			return b
		}()},
		{"failed extraction", badCRCZip(t, "app.env", []byte("SECRET=1\n"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			_, _ = Unpack(tc.blob, out, false)
			entries, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".confkoffer-tmp-") {
					t.Errorf("temp file %q left behind in %s", e.Name(), out)
				}
			}
		})
	}
}

// The mode must survive the temp-file detour. fchmod on the temp file
// is not subject to umask, so the result should be exact.
func TestUnpackAppliesExactModeThroughRename(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	w := NewWriter()
	if err := w.Add("script.sh", 0o755, bytes.NewReader([]byte("#!/bin/sh\n"))); err != nil {
		t.Fatal(err)
	}
	blob, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := Unpack(blob, out, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(out, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %o, want 755 — a umask of 077 must not reach the extracted file", got)
	}
}
