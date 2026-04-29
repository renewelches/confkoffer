package password

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// cmdLike is the subset of *exec.Cmd we use. *exec.Cmd already
// satisfies it, so the production runner just returns the real cmd.
// Tests provide a fake so no real process is spawned.
type cmdLike interface {
	Output() ([]byte, error)
}

// execRunner constructs a cmdLike for the given argv. The seam lets
// tests swap in a fake without re-execing the test binary or shelling
// out to anything real.
type execRunner func(ctx context.Context, name string, args ...string) cmdLike

// realRunner wires the production exec.CommandContext, passing stderr
// through to the user so `pass` / `op` can prompt for fingerprint or
// gpg-agent unlock.
func realRunner(ctx context.Context, name string, args ...string) cmdLike {
	c := exec.CommandContext(ctx, name, args...)
	c.Stderr = os.Stderr
	return c
}

// runAndCapture runs argv via runner, captures stdout, trims the
// trailing newline, and rejects empty output (ErrEmpty).
func runAndCapture(ctx context.Context, runner execRunner, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("password exec: empty argv")
	}
	cmd := runner(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("password exec %s: %w", describeArgv(argv), err)
	}
	out = trimTrailingNewline(out)
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}
