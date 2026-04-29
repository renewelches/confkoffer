package password

import (
	"context"
	"errors"
	"testing"
)

func TestFlagSourceReturnsValue(t *testing.T) {
	s := &FlagSource{Value: []byte("hunter2")}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q want hunter2", got)
	}
	// Mutating returned slice must not affect Source.
	got[0] = 'X'
	got2, _ := s.Get(context.Background())
	if string(got2) != "hunter2" {
		t.Fatalf("source value mutated externally: %q", got2)
	}
}

func TestFlagSourceEmptyIsErrEmpty(t *testing.T) {
	s := &FlagSource{}
	_, err := s.Get(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v want ErrEmpty", err)
	}
}
