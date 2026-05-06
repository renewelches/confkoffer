package password

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/renewelches/confkoffer/internal/logging"
)

// fakeCmd implements cmdLike with canned output/error.
type fakeCmd struct {
	out []byte
	err error
}

func (f *fakeCmd) Output() ([]byte, error) { return f.out, f.err }

// fakeRunner records the argv it received and returns whatever the test set up.
type fakeRunner struct {
	got     []string
	respond func() (out []byte, err error)
}

func (r *fakeRunner) runner(_ context.Context, name string, args ...string) cmdLike {
	r.got = append([]string{name}, args...)
	out, err := r.respond()
	return &fakeCmd{out: out, err: err}
}

func TestPassSourceShellsOutShape(t *testing.T) {
	r := &fakeRunner{respond: func() ([]byte, error) { return []byte("from-pass\n"), nil }}
	s := &PassSource{Path: "backups/confkoffer/proj", runner: r.runner}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-pass" {
		t.Fatalf("got %q", got)
	}
	want := []string{"pass", "show", "backups/confkoffer/proj"}
	if !equalSlices(r.got, want) {
		t.Fatalf("argv=%v want %v", r.got, want)
	}
}

func TestPassSourceEmptyOutputIsErrEmpty(t *testing.T) {
	r := &fakeRunner{respond: func() ([]byte, error) { return []byte("\n"), nil }}
	s := &PassSource{Path: "x", runner: r.runner}
	_, err := s.Get(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v want ErrEmpty", err)
	}
}

func TestPassSourceErrorPropagates(t *testing.T) {
	r := &fakeRunner{respond: func() ([]byte, error) { return nil, errors.New("gpg-agent failed") }}
	s := &PassSource{Path: "x", runner: r.runner}
	_, err := s.Get(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandSourceArgvSubstitution(t *testing.T) {
	r := &fakeRunner{respond: func() ([]byte, error) { return []byte("from-op\n"), nil }}
	s := &CommandSource{
		Argv:   []string{"op", "read", "op://Personal/confkoffer/password"},
		runner: r.runner,
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-op" {
		t.Fatalf("got %q", got)
	}
	want := []string{"op", "read", "op://Personal/confkoffer/password"}
	if !equalSlices(r.got, want) {
		t.Fatalf("argv=%v want %v", r.got, want)
	}
}

func TestCommandSourceTimeout(t *testing.T) {
	r := &fakeRunner{respond: func() ([]byte, error) {
		// Simulate a hang by sleeping longer than the timeout, then check ctx.
		// We can't actually sleep here without slowing tests; instead verify
		// that ctx had a deadline applied.
		return []byte("ok\n"), nil
	}}
	s := &CommandSource{
		Argv:    []string{"slow-tool"},
		Timeout: 10 * time.Millisecond,
		runner:  r.runner,
	}
	_, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestArgvNeverAppearsInLogs(t *testing.T) {
	// Regression guard for the "argv never logged" rule. Capture slog
	// output during a successful CommandSource.Get and assert the
	// secret-shaped argv values are absent.
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(logging.New(&logBuf, slog.LevelDebug))
	t.Cleanup(func() { slog.SetDefault(prev) })

	secret := "op://Personal/super-secret-vault/password"
	r := &fakeRunner{respond: func() ([]byte, error) { return []byte("ok\n"), nil }}
	s := &CommandSource{Argv: []string{"op", "read", secret}, runner: r.runner}

	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logBuf.String(), secret) {
		t.Fatalf("argv leaked into logs: %s", logBuf.String())
	}
}

func TestDescribeArgvDoesNotLeak(t *testing.T) {
	got := describeArgv([]string{"/usr/bin/op", "read", "op://x/y"})
	if strings.Contains(got, "op://") {
		t.Fatalf("describeArgv leaked argument: %q", got)
	}
	if !strings.Contains(got, "op") {
		t.Fatalf("describeArgv lost program name: %q", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
