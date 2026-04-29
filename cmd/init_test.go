package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runRootWith builds a fresh cobra root with init wired up and runs it.
// Returning a fresh root each call also reinitialises rootOpts via the
// StringVar bindings, so tests don't leak global state.
func runRootWith(t *testing.T, args ...string) error {
	t.Helper()
	c := newRoot()
	addInit(c)
	c.SetArgs(args)
	c.SetOut(&strings.Builder{})
	c.SetErr(&strings.Builder{})
	return c.Execute()
}

func TestInitWritesTemplateInEmptyDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".confkoffer.yaml")

	if err := runRootWith(t, "init", "--config", path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "name: my-project") {
		t.Fatalf("template missing 'name' line: %s", got)
	}
}

func TestInitRefusesExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".confkoffer.yaml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runRootWith(t, "init", "--config", path)
	if err == nil {
		t.Fatal("expected error when file exists and --force not set")
	}
	var ce configError
	if !errors.As(err, &ce) {
		t.Fatalf("expected configError (exit 2), got %T: %v", err, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "# existing\n" {
		t.Fatalf("file overwritten without --force: %q", got)
	}
}

func TestInitOverwritesWithForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".confkoffer.yaml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runRootWith(t, "init", "--config", path, "--force"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "name: my-project") {
		t.Fatalf("template not written on --force: %s", got)
	}
}
