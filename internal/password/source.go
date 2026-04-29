// Package password supplies the Source abstraction over the various
// places confkoffer can fetch a passphrase: a CLI flag, an env var,
// an interactive prompt, the passwordstore.org `pass` tool, or any
// user-supplied command.
//
// All implementations return passwords as []byte so callers can wipe
// the buffer with Zero after use. Implementations never log the
// password value; only Source.Name() is logged.
package password

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// Source produces a password on demand. Get is allowed to block
// (interactive prompt, exec child) and must honour ctx cancellation.
type Source interface {
	Get(ctx context.Context) ([]byte, error)
	Name() string
}

// Zero overwrites b with zeros. Best-effort secret hygiene; once Go's
// GC has copied the slice, prior copies are out of reach.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ErrPromptExhausted is returned when the interactive prompt has been
// retried too many times without yielding a usable password. cmd/ maps
// this to exit code 2.
var ErrPromptExhausted = errors.New("password prompt: too many attempts")

// ErrEmpty is returned when a Source produced no value (e.g. an
// empty CONFKOFFER_PASS or a blank `pass show` output).
var ErrEmpty = errors.New("password source returned empty value")

// trimTrailingNewline strips the single trailing \n (and optional \r)
// commonly emitted by `pass show` and similar tools. Internal newlines
// are preserved — the caller might intentionally have a multi-line
// passphrase.
func trimTrailingNewline(b []byte) []byte {
	b = bytes.TrimRight(b, "\n")
	b = bytes.TrimRight(b, "\r")
	return b
}

// describeArgv returns a redacted summary suitable for logs: the
// program name only, never its arguments. Arguments may contain
// secret-looking flags or paths that hint at vault layouts.
func describeArgv(argv []string) string {
	if len(argv) == 0 {
		return "(empty)"
	}
	prog := argv[0]
	if i := strings.LastIndexByte(prog, '/'); i >= 0 {
		prog = prog[i+1:]
	}
	return fmt.Sprintf("%s (%d args)", prog, len(argv)-1)
}
