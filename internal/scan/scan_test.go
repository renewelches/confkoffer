package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fixture builds a tree under t.TempDir for scan tests.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"main.tf":             "tf",
		"variables.tf":        "tf",
		"terraform.tfvars":    "vars",
		"prod.auto.tfvars":    "vars",
		"terraform.tfstate":   "state",
		".terraform/lock.hcl": "lock",
		".terraform/cache":    "cache",
		"secrets/prod.env":    "env",
		"secrets/.gitignore":  "ignored",
		"docs/readme.md":      "docs",
		"sub/dir/deep.tf":     "deep",
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func relPaths(ms []Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.RelPath
	}
	return out
}

func TestWalkIncludeExcludeBasic(t *testing.T) {
	root := fixture(t)
	got, err := Walk(root, Patterns{
		Include: []string{"**/*.tf", "**/*.tfvars", "secrets/prod.env"},
		Exclude: []string{"**/*.tfstate", ".terraform/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"main.tf",
		"prod.auto.tfvars",
		"secrets/prod.env",
		"sub/dir/deep.tf",
		"terraform.tfvars",
		"variables.tf",
	}
	if diff := slices.Compare(relPaths(got), want); diff != 0 {
		t.Fatalf("got %v\nwant %v", relPaths(got), want)
	}
}

func TestWalkExcludeWinsOverInclude(t *testing.T) {
	root := fixture(t)
	got, err := Walk(root, Patterns{
		Include: []string{"**/*.tf", "**/*.tfstate"},
		Exclude: []string{"**/*.tfstate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if filepath.Ext(m.RelPath) == ".tfstate" {
			t.Fatalf("exclude failed to suppress %q", m.RelPath)
		}
	}
}

func TestWalkLiteralPathInclude(t *testing.T) {
	root := fixture(t)
	got, err := Walk(root, Patterns{
		Include: []string{"docs/readme.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RelPath != "docs/readme.md" {
		t.Fatalf("got %v, want [docs/readme.md]", relPaths(got))
	}
}

func TestWalkRequiresAtLeastOneInclude(t *testing.T) {
	root := fixture(t)
	_, err := Walk(root, Patterns{})
	if err == nil {
		t.Fatal("expected error when Include is empty")
	}
}

func TestWalkInvalidPatternErrors(t *testing.T) {
	root := fixture(t)
	_, err := Walk(root, Patterns{Include: []string{"["}})
	if err == nil {
		t.Fatal("expected compile error for malformed pattern")
	}
}

func TestWalkSkipsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions are flaky on Windows CI")
	}
	root := fixture(t)
	target := filepath.Join(root, "main.tf")
	link := filepath.Join(root, "alias.tf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := Walk(root, Patterns{Include: []string{"**/*.tf"}})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(relPaths(got), "alias.tf") {
		t.Fatalf("symlink should be skipped: %v", relPaths(got))
	}
	// And the real file is still picked up.
	if !slices.Contains(relPaths(got), "main.tf") {
		t.Fatalf("real file missing: %v", relPaths(got))
	}
}

func TestWalkSinglestarDoesNotCrossSlash(t *testing.T) {
	root := fixture(t)
	got, err := Walk(root, Patterns{
		Include: []string{"*.tf"}, // single star — top level only
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range relPaths(got) {
		if m == "sub/dir/deep.tf" {
			t.Fatalf("single * should not cross /, but matched %q", m)
		}
	}
	if !slices.Contains(relPaths(got), "main.tf") {
		t.Fatalf("expected main.tf at top level: %v", relPaths(got))
	}
}

func TestWalkDeterministicOrdering(t *testing.T) {
	root := fixture(t)
	a, err := Walk(root, Patterns{Include: []string{"**/*"}, Exclude: []string{".terraform/**"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Walk(root, Patterns{Include: []string{"**/*"}, Exclude: []string{".terraform/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Compare(relPaths(a), relPaths(b)) != 0 {
		t.Fatalf("non-deterministic order:\n%v\nvs\n%v", relPaths(a), relPaths(b))
	}
}

func TestExpandDoubleStar(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"*.tf", []string{"*.tf"}},
		{".terraform/**", []string{".terraform/**"}},
		{"**/*.tf", []string{"**/*.tf", "*.tf"}},
		{"secrets/**/prod.env", []string{"secrets/**/prod.env", "secrets/prod.env"}},
		{"a/**/b/**/c", []string{"a/**/b/**/c", "a/**/b/c", "a/b/**/c", "a/b/c"}},
		{"**/**/x", []string{"**/**/x", "**/x", "x"}},
		{"**/", []string{"**/"}},
		// Not whole segments, escaped, or inside a class: left alone.
		{"a**/b", []string{"a**/b"}},
		{`\**/x`, []string{`\**/x`}},
		{"[**/]x", []string{"[**/]x"}},
	}
	for _, tt := range tests {
		got, err := expandDoubleStar(tt.in)
		if err != nil {
			t.Fatalf("expandDoubleStar(%q): %v", tt.in, err)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("expandDoubleStar(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExpandDoubleStarLimit(t *testing.T) {
	p := strings.Repeat("**/", maxDoubleStarDirs+1) + "x"
	if _, err := expandDoubleStar(p); err == nil {
		t.Fatalf("expected an error for %d \"**/\" segments", maxDoubleStarDirs+1)
	}
}

// A "**/" segment matches zero or more directories, gitignore-style.
// gobwas/glob v1 alone requires at least one, which would silently drop
// files from a backup, so this guards against a library change undoing it.
func TestWalkDoubleStarMatchesZeroOrMoreDirs(t *testing.T) {
	root := fixture(t)
	tests := []struct {
		include string
		want    string
	}{
		{"secrets/**/prod.env", "secrets/prod.env"}, // zero dirs
		{"sub/**/deep.tf", "sub/dir/deep.tf"},       // one dir
		{"**/**/deep.tf", "sub/dir/deep.tf"},        // two dirs, repeated segment
		{"sub/dir/**/deep.tf", "sub/dir/deep.tf"},   // zero dirs, deeper prefix
		{"**/dir/**/deep.tf", "sub/dir/deep.tf"},    // leading and middle
	}
	for _, tt := range tests {
		got, err := Walk(root, Patterns{Include: []string{tt.include}})
		if err != nil {
			t.Fatalf("%s: %v", tt.include, err)
		}
		if !slices.Contains(relPaths(got), tt.want) {
			t.Errorf("include %q: want %q in %v", tt.include, tt.want, relPaths(got))
		}
	}
}

// Excludes share compileAll, so the zero-directory rule applies there too.
func TestWalkDoubleStarExcludeMatchesZeroDirs(t *testing.T) {
	root := fixture(t)
	got, err := Walk(root, Patterns{
		Include: []string{"**/*"},
		Exclude: []string{"secrets/**/prod.env"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(relPaths(got), "secrets/prod.env") {
		t.Fatalf("secrets/prod.env should be excluded: %v", relPaths(got))
	}
}
