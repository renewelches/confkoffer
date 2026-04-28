package archive

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPackUnpackRoundTripPreservesContentAndMode(t *testing.T) {
	w := NewWriter()
	files := map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"a.txt":          {[]byte("alpha"), 0o644},
		"sub/b.conf":     {[]byte("k=v\n"), 0o600},
		"sub/dir/c.bin":  {[]byte{0x00, 0x01, 0x02, 0x03}, 0o755},
		"deep/x/y/z.dat": {[]byte("deep"), 0o644},
	}
	// Add files in a deterministic order so the test is stable.
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, name := range keys {
		spec := files[name]
		if err := w.Add(name, spec.mode, bytes.NewReader(spec.body)); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	blob, err := w.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	out := t.TempDir()
	res, err := Unpack(blob, out, false)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(res.Written) != len(files) {
		t.Fatalf("Written=%v, want %d entries", res.Written, len(files))
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped=%v, want none on fresh extract", res.Skipped)
	}

	for name, spec := range files {
		p := filepath.Join(out, filepath.FromSlash(name))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !bytes.Equal(got, spec.body) {
			t.Errorf("%s: body mismatch", name)
		}
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatalf("stat %s: %v", p, err)
			}
			if fi.Mode().Perm() != spec.mode.Perm() {
				t.Errorf("%s: mode=%v want %v", name, fi.Mode().Perm(), spec.mode.Perm())
			}
		}
	}
}

func TestUnpackSkipsExistingWithoutOverwrite(t *testing.T) {
	w := NewWriter()
	if err := w.Add("hello.txt", 0o644, strings.NewReader("from-zip")); err != nil {
		t.Fatal(err)
	}
	blob, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	target := filepath.Join(out, "hello.txt")
	if err := os.WriteFile(target, []byte("from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Unpack(blob, out, false)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !slices.Contains(res.Skipped, "hello.txt") {
		t.Fatalf("Skipped=%v, want it to contain hello.txt", res.Skipped)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "from-disk" {
		t.Fatalf("file overwritten: got %q want from-disk", got)
	}
}

func TestUnpackOverwriteReplacesFile(t *testing.T) {
	w := NewWriter()
	if err := w.Add("hello.txt", 0o644, strings.NewReader("from-zip")); err != nil {
		t.Fatal(err)
	}
	blob, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	target := filepath.Join(out, "hello.txt")
	if err := os.WriteFile(target, []byte("from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Unpack(blob, out, true)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !slices.Contains(res.Written, "hello.txt") {
		t.Fatalf("Written=%v, want hello.txt", res.Written)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "from-zip" {
		t.Fatalf("got %q want from-zip", got)
	}
}

func TestAddRejectsUnsafePaths(t *testing.T) {
	cases := []string{
		"",
		"/abs/path",
		"../escape",
		"a/../../escape",
		"a/b/../../../escape",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			w := NewWriter()
			err := w.Add(name, 0o644, strings.NewReader("x"))
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Add(%q): err=%v, want ErrUnsafePath", name, err)
			}
		})
	}
}

func TestUnpackRejectsZipSlipEntries(t *testing.T) {
	// Build a zip by hand whose entry name is malicious. We bypass Add
	// so the bad name reaches Unpack and exercises its defenses.
	cases := []string{
		"../escape.txt",
		"a/../../escape.txt",
		"/etc/passwd",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			fw, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write([]byte("pwn")); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}

			out := t.TempDir()
			_, err = Unpack(buf.Bytes(), out, true)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("got %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestEmptyArchiveRoundTrip(t *testing.T) {
	w := NewWriter()
	blob, err := w.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	out := t.TempDir()
	res, err := Unpack(blob, out, false)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(res.Written) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

func TestLargeFileRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-file test in short mode")
	}
	const size = 10 * 1024 * 1024
	body := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, body); err != nil {
		t.Fatal(err)
	}

	w := NewWriter()
	if err := w.Add("big.bin", 0o644, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	blob, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if _, err := Unpack(blob, out, false); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("large file content mismatch")
	}
}
