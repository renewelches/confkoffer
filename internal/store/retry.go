package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/minio/minio-go/v7"
)

// DefaultBackoff is the schedule used between retry attempts. The
// total wait before giving up is 500ms + 2s + 8s = 10.5s for a
// 3-attempt retry on transient errors.
var DefaultBackoff = []time.Duration{
	500 * time.Millisecond,
	2 * time.Second,
	8 * time.Second,
}

// retryConfig is internal so tests can inject a fast schedule.
type retryConfig struct {
	backoff []time.Duration
	sleep   func(time.Duration) // injectable for tests
}

func defaultRetry() retryConfig {
	return retryConfig{
		backoff: DefaultBackoff,
		sleep:   time.Sleep,
	}
}

// isTransient classifies err as worth retrying. minio-go reports HTTP
// errors via minio.ErrorResponse with a StatusCode; >=500 means the
// remote bricked, <500 means the request itself is wrong (auth,
// missing bucket, etc.) and retrying won't help.
//
// context cancellation and deadline exhaustion are NOT transient: the
// caller has explicitly asked us to stop.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		if er.StatusCode >= 500 {
			return true
		}
		// 4xx — definitively a client error. Do not retry.
		return false
	}
	// Unknown error class (network blip, DNS, broken pipe). Treat as
	// transient — better to wait and retry than fail loud on a flake.
	return true
}

// withRetry runs op until it returns nil or a non-transient error, or
// until the backoff schedule is exhausted. The first attempt happens
// immediately; subsequent attempts wait for cfg.backoff[i-1].
func withRetry(ctx context.Context, cfg retryConfig, op func(context.Context) error) error {
	var lastErr error
	maxAttempts := len(cfg.backoff) + 1 // schedule entries + initial attempt
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransient(err) {
			return err
		}
		if attempt == maxAttempts-1 {
			break
		}
		wait := cfg.backoff[attempt]
		slog.Warn("store: transient error, retrying",
			"attempt", attempt+1,
			"wait", wait.String(),
			"err", err.Error(),
		)
		cfg.sleep(wait)
	}
	return lastErr
}
