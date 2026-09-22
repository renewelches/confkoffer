package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gocloud.dev/gcerrors"
)

// DefaultBackoff is the schedule used between retry attempts: four
// attempts with 500ms + 2s + 8s = 10.5s of deliberate waiting between
// them.
//
// 10.5s is the wait this package adds, not the wall-clock time a
// caller sees. s3blob retries underneath us: gocloud's
// aws.V2ConfigFromURLParams installs retry.NewStandard (3 attempts)
// unconditionally, so each of our four attempts is itself up to three
// requests. Measured against a refused endpoint, `list` takes ~20s,
// not 10.5s.
//
// That inner layer is not tunable from here — gocloud appends its
// WithRetryer after the environment-derived options, so AWS_MAX_ATTEMPTS
// is overridden and has no effect. Shortening the real worst case means
// either trimming this schedule or opening the bucket through a custom
// s3blob.URLOpener with our own aws.Config.
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

// isTransient classifies err as worth retrying.
//
// Classification is by gcerrors code: gocloud normalises every
// backend's errors to one, so a single switch covers S3, Azure, GCS,
// and the filesystem rather than one error type per SDK.
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
	// An oversized object will not shrink on retry.
	if errors.Is(err, ErrTooLarge) {
		return false
	}
	// Without this switch a plain "not found" falls through to the
	// unknown branch below and burns the whole backoff schedule —
	// 10.5s — before reporting what the backend already said
	// definitively on the first attempt.
	switch gcerrors.Code(err) {
	case gcerrors.NotFound,
		gcerrors.PermissionDenied,
		gcerrors.InvalidArgument,
		gcerrors.FailedPrecondition,
		gcerrors.AlreadyExists,
		gcerrors.Unimplemented:
		// The request itself is wrong. Retrying cannot change the answer.
		return false
	case gcerrors.Internal, gcerrors.ResourceExhausted:
		// Backend-side failure or throttling — worth waiting out.
		return true
	case gcerrors.Canceled, gcerrors.DeadlineExceeded:
		// The caller asked us to stop.
		return false
	}

	// gcerrors.Unknown — a driver error gocloud could not classify,
	// which in practice is the transport layer: DNS failure, refused
	// connection, broken pipe, TLS handshake reset. Treat as transient;
	// better to wait and retry than to fail loud on a flake.
	return true
}

// withRetry runs op until it returns nil or a non-transient error, or
// until the backoff schedule is exhausted. The first attempt happens
// immediately; subsequent attempts wait for cfg.backoff[i-1].
func withRetry(ctx context.Context, cfg retryConfig, op func(context.Context) error) error {
	var lastErr error
	maxAttempts := len(cfg.backoff) + 1 // schedule entries + initial attempt
	for attempt := range maxAttempts {
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
