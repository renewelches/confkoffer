package password

import (
	"context"
	"os"
)

// EnvSource reads the password from the environment variable named Var
// (defaults to "CONFKOFFER_PASS").
type EnvSource struct {
	Var string
}

func (e *EnvSource) varName() string {
	if e.Var == "" {
		return "CONFKOFFER_PASS"
	}
	return e.Var
}

func (e *EnvSource) Get(_ context.Context) ([]byte, error) {
	v, ok := os.LookupEnv(e.varName())
	if !ok || v == "" {
		return nil, ErrEmpty
	}
	return []byte(v), nil
}

func (e *EnvSource) Name() string { return "env" }
