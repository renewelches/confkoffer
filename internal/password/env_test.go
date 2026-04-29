package password

import (
	"context"
	"errors"
	"testing"
)

func TestEnvSourceReadsVar(t *testing.T) {
	t.Setenv("MY_PW", "from-env")
	s := &EnvSource{Var: "MY_PW"}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestEnvSourceUnsetIsErrEmpty(t *testing.T) {
	s := &EnvSource{Var: "DOES_NOT_EXIST_TEST_PW"}
	_, err := s.Get(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v want ErrEmpty", err)
	}
}

func TestEnvSourceDefaultVarName(t *testing.T) {
	t.Setenv("CONFKOFFER_PASS", "default-var")
	s := &EnvSource{}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "default-var" {
		t.Fatalf("got %q", got)
	}
}
