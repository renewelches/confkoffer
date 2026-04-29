package password

import (
	"context"
	"errors"
	"testing"
)

// stubSource is a configurable Source for chain tests.
type stubSource struct {
	name string
	val  []byte
	err  error
}

func (s *stubSource) Get(_ context.Context) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.val, nil
}
func (s *stubSource) Name() string { return s.name }

func TestChainStopsAtFirstSuccess(t *testing.T) {
	a := &stubSource{name: "a", err: ErrEmpty}
	b := &stubSource{name: "b", val: []byte("found")}
	c := &stubSource{name: "c", val: []byte("never-reached")}
	chain := NewChain(a, b, c)

	got, err := chain.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "found" {
		t.Fatalf("got %q want found", got)
	}
}

func TestChainSkipsErrEmpty(t *testing.T) {
	a := &stubSource{name: "a", err: ErrEmpty}
	b := &stubSource{name: "b", err: ErrEmpty}
	c := &stubSource{name: "c", val: []byte("end")}
	chain := NewChain(a, b, c)
	got, err := chain.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "end" {
		t.Fatalf("got %q want end", got)
	}
}

func TestChainAbortsOnNonEmptyError(t *testing.T) {
	// A misconfigured pass source should not be silently masked by a
	// fallback prompt — that would invite operator confusion.
	a := &stubSource{name: "pass", err: errors.New("gpg-agent timed out")}
	b := &stubSource{name: "prompt", val: []byte("would-mask")}
	chain := NewChain(a, b)
	_, err := chain.Get(context.Background())
	if err == nil {
		t.Fatal("expected error to abort chain")
	}
}

func TestChainAllEmptyExhausts(t *testing.T) {
	a := &stubSource{name: "a", err: ErrEmpty}
	b := &stubSource{name: "b", err: ErrEmpty}
	chain := NewChain(a, b)
	_, err := chain.Get(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v want ErrEmpty wrap", err)
	}
}

func TestChainEmptyConfig(t *testing.T) {
	chain := NewChain()
	_, err := chain.Get(context.Background())
	if err == nil {
		t.Fatal("expected error for empty chain")
	}
}

func TestChainSkipsNilSources(t *testing.T) {
	a := &stubSource{name: "a", val: []byte("ok")}
	chain := NewChain(nil, a, nil)
	got, err := chain.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}
