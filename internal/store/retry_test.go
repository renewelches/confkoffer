package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gocloud.dev/gcerrors"
)

// gcerr returns a real, gocloud-coded error of the given kind.
//
// gcerrors codes cannot be constructed directly: the concrete type
// lives in gocloud.dev/internal/gcerr, which is import-restricted. So
// provoke the real thing from a real driver — the fileblob bucket
// behind fileClient — which also keeps these fixtures honest about
// what isTransient actually receives at runtime.
func gcerrNotFound(t *testing.T) error {
	t.Helper()
	_, err := fileClient(t).Get(context.Background(), "absent/key.enc")
	if err == nil {
		t.Fatal("Get on an absent key returned nil, want a NotFound error")
	}
	if got := gcerrors.Code(err); got != gcerrors.NotFound {
		t.Fatalf("fixture has code %v, want NotFound", got)
	}
	return err
}

func fastRetry() retryConfig {
	return retryConfig{
		backoff: []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond},
		sleep:   func(time.Duration) {},
	}
}

func TestWithRetrySucceedsFirstTry(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

// An unclassified driver error is the transport layer failing, which is
// exactly the case retrying exists for.
func TestWithRetryRetriesOnTransient(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("dial tcp: connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d want 3", calls)
	}
}

// A definitive answer from the backend must be returned as-is on the
// first attempt — retrying cannot change it, and the wait is pure delay
// in front of an error the caller already had.
func TestWithRetryAbortsOnDefinitiveError(t *testing.T) {
	want := gcerrNotFound(t)
	calls := 0
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want the original error unwrapped", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (no retry on a definitive error)", calls)
	}
}

func TestWithRetryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := withRetry(ctx, fastRetry(), func(context.Context) error {
		calls++
		return errors.New("ignored")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d want 0 — should bail before first attempt", calls)
	}
}

func TestWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		return errors.New("connection reset by peer")
	})
	if err == nil {
		t.Fatal("expected error after exhaustion")
	}
	if calls != 4 { // 1 initial + 3 retries (len(backoff))
		t.Fatalf("calls=%d want 4", calls)
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"gcerrors.NotFound", gcerrNotFound(t), false},
		{"ErrTooLarge", ErrTooLarge, false},
		{"wrapped ErrTooLarge", fmt.Errorf("get %q: %w", "k", ErrTooLarge), false},
		{"network", errors.New("dial tcp: connection refused"), true},
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"wrapped context.Canceled", fmt.Errorf("op: %w", context.Canceled), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransient(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
