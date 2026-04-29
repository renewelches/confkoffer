package password

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Prompter reads a password interactively. When Confirm is true (used
// on `pack`) the user must type the same value twice. Up to Attempts
// retries are allowed; ErrPromptExhausted is returned otherwise.
//
// In may be *os.File (real stdin, terminal) or any io.Reader (tests,
// piped input). Out receives the prompt text and error messages.
type Prompter struct {
	In       io.Reader
	Out      io.Writer
	Confirm  bool
	Attempts int

	// NoEcho toggles raw-mode/no-echo when In is a terminal *os.File.
	// Tests should leave it false and use bytes.Buffer for In.
	NoEcho bool
}

// NewDefaultPrompter is the wiring used by cmd/ in production.
func NewDefaultPrompter(confirm bool) *Prompter {
	return &Prompter{
		In:       os.Stdin,
		Out:      os.Stderr,
		Confirm:  confirm,
		Attempts: 3,
		NoEcho:   true,
	}
}

func (p *Prompter) Get(ctx context.Context) ([]byte, error) {
	if p.Attempts <= 0 {
		p.Attempts = 1
	}
	for attempt := 0; attempt < p.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		fmt.Fprint(p.Out, "Password: ")
		pw, err := p.readLine()
		fmt.Fprintln(p.Out)
		if err != nil {
			return nil, err
		}
		if len(pw) == 0 {
			fmt.Fprintln(p.Out, "password must not be empty; try again")
			continue
		}

		if p.Confirm {
			fmt.Fprint(p.Out, "Confirm:  ")
			confirm, err := p.readLine()
			fmt.Fprintln(p.Out)
			if err != nil {
				Zero(pw)
				return nil, err
			}
			if !bytes.Equal(pw, confirm) {
				Zero(pw)
				Zero(confirm)
				fmt.Fprintln(p.Out, "passwords do not match; try again")
				continue
			}
			Zero(confirm)
		}

		return pw, nil
	}
	return nil, ErrPromptExhausted
}

func (p *Prompter) Name() string { return "prompt" }

// readLine reads bytes up to (but not including) the next \n from p.In.
// When NoEcho is set and In is a terminal *os.File, x/term's
// ReadPassword is used to suppress echo.
func (p *Prompter) readLine() ([]byte, error) {
	if p.NoEcho {
		if f, ok := p.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return term.ReadPassword(int(f.Fd()))
		}
	}
	var buf []byte
	var b [1]byte
	for {
		n, err := p.In.Read(b[:])
		if n == 0 {
			if errors.Is(err, io.EOF) {
				if len(buf) == 0 {
					return nil, io.EOF
				}
				return buf, nil
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if b[0] == '\n' {
			return buf, nil
		}
		if b[0] == '\r' {
			continue
		}
		buf = append(buf, b[0])
	}
}
