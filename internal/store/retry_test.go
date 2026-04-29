package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

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

func TestWithRetryRetriesOn5xx(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		if calls < 3 {
			return minio.ErrorResponse{StatusCode: 503, Code: "ServiceUnavailable", Message: "try later"}
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

func TestWithRetryAbortsOn4xx(t *testing.T) {
	calls := 0
	want := minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}
	err := withRetry(context.Background(), fastRetry(), func(context.Context) error {
		calls++
		return want
	})
	if !errors.As(err, &minio.ErrorResponse{}) {
		t.Fatalf("err=%v not a minio.ErrorResponse", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (no retry on 4xx)", calls)
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
		return minio.ErrorResponse{StatusCode: 500}
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
		{"500", minio.ErrorResponse{StatusCode: 500}, true},
		{"502", minio.ErrorResponse{StatusCode: 502}, true},
		{"403", minio.ErrorResponse{StatusCode: 403}, false},
		{"404", minio.ErrorResponse{StatusCode: 404}, false},
		{"network", errors.New("dial tcp: connection refused"), true},
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransient(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSortByLastModifiedDesc(t *testing.T) {
	now := time.Now()
	objs := []Object{
		{Key: "old", LastModified: now.Add(-2 * time.Hour)},
		{Key: "newest", LastModified: now},
		{Key: "mid", LastModified: now.Add(-1 * time.Hour)},
	}
	sortByLastModifiedDesc(objs)
	want := []string{"newest", "mid", "old"}
	for i, k := range want {
		if objs[i].Key != k {
			t.Fatalf("at %d: got %q want %q", i, objs[i].Key, k)
		}
	}
}

func TestPickAtPicksNewestAtOrBefore(t *testing.T) {
	now := time.Now().UTC()
	objs := []Object{
		{Key: "n", LastModified: now},                   // 0h ago
		{Key: "h-1", LastModified: now.Add(-1 * time.Hour)}, // 1h ago
		{Key: "h-3", LastModified: now.Add(-3 * time.Hour)}, // 3h ago
	}
	got, err := PickAt(objs, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "h-3" {
		t.Fatalf("got %q want h-3", got.Key)
	}
}

func TestPickAtTooOldIsErrNoSnapshots(t *testing.T) {
	now := time.Now().UTC()
	objs := []Object{{Key: "only", LastModified: now}}
	_, err := PickAt(objs, now.Add(-1*time.Hour))
	if !errors.Is(err, ErrNoSnapshots) {
		t.Fatalf("err=%v want ErrNoSnapshots", err)
	}
}
