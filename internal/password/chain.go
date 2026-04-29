package password

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Chain tries each Source in order and returns the first successful
// password. Sources that report ErrEmpty are skipped silently — that's
// the contract (fall through to the next source). Other errors abort
// the chain immediately so a misconfigured pass/command source isn't
// papered over by an interactive prompt.
type Chain struct {
	Sources []Source
}

// NewChain returns a Chain over the given sources. nil entries are skipped.
func NewChain(sources ...Source) *Chain {
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			out = append(out, s)
		}
	}
	return &Chain{Sources: out}
}

func (c *Chain) Get(ctx context.Context) ([]byte, error) {
	if len(c.Sources) == 0 {
		return nil, errors.New("password chain: no sources configured")
	}
	for _, s := range c.Sources {
		pw, err := s.Get(ctx)
		if err == nil {
			slog.Debug("password: source produced value", "source", s.Name())
			return pw, nil
		}
		if errors.Is(err, ErrEmpty) {
			slog.Debug("password: source empty, falling through", "source", s.Name())
			continue
		}
		return nil, fmt.Errorf("password source %q: %w", s.Name(), err)
	}
	return nil, fmt.Errorf("password chain exhausted: %w", ErrEmpty)
}

func (c *Chain) Name() string {
	names := make([]string, 0, len(c.Sources))
	for _, s := range c.Sources {
		names = append(names, s.Name())
	}
	return fmt.Sprintf("chain(%v)", names)
}
