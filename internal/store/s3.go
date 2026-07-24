// Package store wraps minio-go to put, get, and list confkoffer blobs
// in an S3-compatible bucket. The wrapper adds:
//
//   - retry-with-backoff on transient (5xx, network) errors;
//   - newest-first listing sorted by LastModified;
//   - a small Object struct that hides the minio types from callers.
//
// AWS credentials always come from the environment (AWS_ACCESS_KEY_ID
// / AWS_SECRET_ACCESS_KEY) — never from CLI flags, to avoid shell
// history exposure.
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config carries the connection parameters needed to construct a Client.
type Config struct {
	Bucket   string
	Endpoint string
	Region   string

	// Insecure forces http:// instead of https://. Useful for local
	// MinIO; wired up by cmd/ from --endpoint scheme.
	Insecure bool
}

// Object is the slimmed-down listing entry returned by List.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Client is the high-level confkoffer storage handle.
type Client struct {
	bucket  string
	mc      *minio.Client
	retry   retryConfig
}

// New builds a Client. Secure transport is on by default; pass
// Insecure=true (or an http:// endpoint) for local testing.
func New(cfg Config) (*Client, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("store: bucket is required")
	}
	if cfg.Endpoint == "" {
		return nil, errors.New("store: endpoint is required")
	}

	endpoint, secure, err := normalizeEndpoint(cfg.Endpoint, cfg.Insecure)
	if err != nil {
		return nil, err
	}

	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewEnvAWS(),
		Secure: secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("minio.New: %w", err)
	}

	return &Client{
		bucket: cfg.Bucket,
		mc:     mc,
		retry:  defaultRetry(),
	}, nil
}

// normalizeEndpoint accepts "host:port", "http://host", or "https://host"
// and returns the bare host:port plus the secure bit. Whenever the
// result is insecure, a warning is logged — cleartext transport exposes
// the AWS credentials in the request headers, not just the blob.
func normalizeEndpoint(in string, forceInsecure bool) (string, bool, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", false, errors.New("empty endpoint")
	}
	secure := !forceInsecure

	// Scheme comparison is case-insensitive per RFC 3986 ("HTTP://" is
	// valid), so cut by length after an EqualFold check rather than a
	// literal prefix match.
	if host, ok := cutSchemeFold(in, "https://"); ok {
		return host, true, nil
	}
	if host, ok := cutSchemeFold(in, "http://"); ok {
		warnInsecure(host)
		return host, false, nil
	}
	if !secure {
		warnInsecure(in)
	}
	return in, secure, nil
}

// cutSchemeFold strips scheme from the front of in, matching
// case-insensitively. Returns the remainder and whether it matched.
func cutSchemeFold(in, scheme string) (string, bool) {
	if len(in) >= len(scheme) && strings.EqualFold(in[:len(scheme)], scheme) {
		return in[len(scheme):], true
	}
	return in, false
}

// warnInsecure logs the TLS-disabled warning, skipping loopback hosts
// where cleartext is the expected local-MinIO workflow.
func warnInsecure(host string) {
	bare := host
	if i := strings.LastIndex(bare, ":"); i >= 0 {
		bare = bare[:i]
	}
	if bare == "localhost" || bare == "127.0.0.1" || bare == "::1" || bare == "[::1]" {
		return
	}
	slog.Warn("store: TLS disabled — AWS credentials will be sent in cleartext", "endpoint", host)
}

// Put uploads body to key with content-type application/octet-stream.
// Retries on transient errors per DefaultBackoff.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	op := func(ctx context.Context) error {
		_, err := c.mc.PutObject(ctx, c.bucket, key,
			bytes.NewReader(body), int64(len(body)),
			minio.PutObjectOptions{ContentType: "application/octet-stream"},
		)
		return err
	}
	return withRetry(ctx, c.retry, op)
}

// MaxBlobSize caps how many bytes Get will read for a single object.
// Guards against OOM on corrupted or adversarial oversized objects.
const MaxBlobSize = 256 << 20 // 256 MiB

// ErrTooLarge is returned by Get when the object exceeds MaxBlobSize.
var ErrTooLarge = errors.New("object exceeds maximum blob size")

// Get downloads the object at key and returns its bytes. Retries on
// transient errors. Objects larger than MaxBlobSize are rejected with
// ErrTooLarge.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	var out []byte
	op := func(ctx context.Context) error {
		obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return err
		}
		defer obj.Close()
		buf, err := io.ReadAll(io.LimitReader(obj, MaxBlobSize+1))
		if err != nil {
			return err
		}
		if len(buf) > MaxBlobSize {
			return fmt.Errorf("%w: %s (limit %d bytes)", ErrTooLarge, key, MaxBlobSize)
		}
		out = buf
		return nil
	}
	if err := withRetry(ctx, c.retry, op); err != nil {
		return nil, err
	}
	return out, nil
}

// ErrNoSnapshots is returned when List finds zero objects under prefix.
var ErrNoSnapshots = errors.New("no snapshots found")

// List returns all objects under prefix, sorted by LastModified
// descending (newest first). Empty result yields ErrNoSnapshots so
// callers can map it to exit code 1 with a clean message.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var objects []Object
	for info := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if info.Err != nil {
			return nil, info.Err
		}
		// Skip "directory" placeholders.
		if strings.HasSuffix(info.Key, "/") {
			continue
		}
		objects = append(objects, Object{
			Key:          info.Key,
			Size:         info.Size,
			LastModified: info.LastModified,
		})
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("%w under %q", ErrNoSnapshots, strings.TrimSuffix(prefix, "/"))
	}
	sortByLastModifiedDesc(objects)
	return objects, nil
}

// PickAt returns the newest object whose LastModified is at or before t.
// Returns ErrNoSnapshots when no object satisfies the cutoff.
func PickAt(objects []Object, t time.Time) (Object, error) {
	// objects is expected to be newest-first (as returned by List).
	for _, o := range objects {
		if !o.LastModified.After(t) {
			return o, nil
		}
	}
	return Object{}, fmt.Errorf("%w at or before %s", ErrNoSnapshots, t.UTC().Format(time.RFC3339))
}

func sortByLastModifiedDesc(objs []Object) {
	sort.SliceStable(objs, func(i, j int) bool {
		return objs[i].LastModified.After(objs[j].LastModified)
	})
}
