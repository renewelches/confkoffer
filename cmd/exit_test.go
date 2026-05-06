package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/renewelches/confkoffer/internal/config"
	"github.com/renewelches/confkoffer/internal/password"
)

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"random io", errors.New("connection refused"), 1},
		{"missing required", fmt.Errorf("wrap: %w", config.ErrMissingRequired), 2},
		{"prompt exhausted", fmt.Errorf("wrap: %w", password.ErrPromptExhausted), 2},
		{"configError wrap", configError{errors.New("bad name")}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
