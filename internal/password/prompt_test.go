package password

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func newScripted(in string) *Prompter {
	return &Prompter{
		In:       strings.NewReader(in),
		Out:      &bytes.Buffer{},
		Attempts: 3,
		// NoEcho stays false so we use the line-reader fallback.
	}
}

func TestPromptHappyPathNoConfirm(t *testing.T) {
	p := newScripted("hunter2\n")
	got, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptDoubleConfirmHappy(t *testing.T) {
	p := newScripted("hunter2\nhunter2\n")
	p.Confirm = true
	got, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptMismatchRetries(t *testing.T) {
	// First pair mismatches; second pair matches.
	p := newScripted("aaa\nbbb\nccc\nccc\n")
	p.Confirm = true
	got, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ccc" {
		t.Fatalf("got %q want ccc", got)
	}
	out := p.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "do not match") {
		t.Fatalf("expected mismatch message in output, got %q", out)
	}
}

func TestPromptThreeAttemptsExhausts(t *testing.T) {
	// Three mismatched pairs in a row -> ErrPromptExhausted.
	p := newScripted("a\nb\nc\nd\ne\nf\n")
	p.Confirm = true
	_, err := p.Get(context.Background())
	if !errors.Is(err, ErrPromptExhausted) {
		t.Fatalf("got %v want ErrPromptExhausted", err)
	}
}

func TestPromptEmptyRetries(t *testing.T) {
	p := newScripted("\nhunter2\n")
	got, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptStripsCR(t *testing.T) {
	p := newScripted("hunter2\r\n")
	got, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q (% x)", got, got)
	}
}

func TestPromptHonoursContextCancel(t *testing.T) {
	p := newScripted("never-read\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Get(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want Canceled", err)
	}
}
