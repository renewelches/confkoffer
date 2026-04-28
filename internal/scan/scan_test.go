package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
