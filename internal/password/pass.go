package password

import (
	"context"
	"fmt"
)

// PassSource fetches a password from passwordstore.org via
// `pass show <path>`. Stderr is passed through to the user so
// gpg-agent prompts work.
type PassSource struct {
	Path string

	runner execRunner // tests inject a fake; default is realRunner
}

// NewPassSource is the production constructor.
func NewPassSource(path string) *PassSource {
	return &PassSource{Path: path, runner: realRunner}
}

func (p *PassSource) Get(ctx context.Context) ([]byte, error) {
	if p.Path == "" {
		return nil, fmt.Errorf("pass: empty path")
	}
	r := p.runner
	if r == nil {
		r = realRunner
	}
	return runAndCapture(ctx, r, []string{"pass", "show", p.Path})
}

func (p *PassSource) Name() string { return "pass" }
