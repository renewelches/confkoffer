package password

import "context"

// FlagSource returns a fixed value supplied via the --pass CLI flag.
// Empty value reports ErrEmpty so the chain can fall through.
type FlagSource struct {
	Value []byte
}

func (f *FlagSource) Get(_ context.Context) ([]byte, error) {
	if len(f.Value) == 0 {
		return nil, ErrEmpty
	}
	out := make([]byte, len(f.Value))
	copy(out, f.Value)
	return out, nil
}

func (f *FlagSource) Name() string { return "flag" }
