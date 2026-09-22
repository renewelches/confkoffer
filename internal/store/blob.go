// Package store wraps gocloud.dev/blob to put, get, and list confkoffer
// blobs in an object store. The wrapper adds:
//
//   - one provider-agnostic surface over S3, Azure, GCS, and a plain
//     directory, selected by the storage block in .confkoffer.yaml;
//   - retry-with-backoff on transient errors, classified by gcerrors
//     code so a definitive answer (NotFound, PermissionDenied) fails
//     fast instead of burning the schedule;
//   - newest-first listing sorted by LastModified;
//   - a small Object struct that hides the driver types from callers.
//
// Credentials always come from the environment — AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY, AZURE_STORAGE_ACCOUNT and its key, or
// Application Default Credentials — never from CLI flags, to avoid
// shell history exposure.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/renewelches/confkoffer/internal/config"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

// MaxBlobSize caps how many bytes Get will read for a single object.
// Guards against OOM on corrupted or adversarial oversized objects.
const MaxBlobSize = 256 << 20 // 256 MiB

// ErrTooLarge is returned by Get when the object exceeds MaxBlobSize.
var ErrTooLarge = errors.New("object exceeds maximum blob size")

// Object is the slimmed-down listing entry returned by List.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// BlobClient is the high-level confkoffer storage handle. It implements
// Storage.
type BlobClient struct {
	Config config.BlobConfig
	Retry  retryConfig
}

// New builds a BlobClient. Secure transport is on by default; pass
// Insecure=true (or an http:// endpoint) for local testing.
func New(cfg config.BlobConfig) (*BlobClient, error) {
	if cfg == nil {
		return nil, errors.New("BlobConfig is required")
	}
	if err := validateUrl(cfg); err != nil {
		return nil, err
	}
	return &BlobClient{
		Config: cfg,
		Retry:  defaultRetry(),
	}, nil
}

// normalizeEndpoint accepts "host:port", "http://host", or "https://host"
// and returns the bare host:port plus the secure bit. Whenever the
// result is insecure, a warning is logged — cleartext transport exposes
// the AWS credentials in the request headers, not just the blob.
//
// A scheme written on the endpoint outranks forceInsecure (the
// `insecure` config key), in both directions:
//
//	endpoint                insecure   result
//	https://host            true       https  — scheme wins
//	http://host             false      http   — scheme wins, warns
//	host                    true       http   — insecure decides
//	host                    false      https  — default
//
// The scheme is the more specific statement: it names the transport for
// that one endpoint, where `insecure` is a standing setting on the
// block. Letting `insecure` downgrade an explicit https:// would put
// credentials in cleartext against a host the config said to reach over
// TLS, which is the one direction that must never happen silently.
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

// cutSchemeFold strips scheme from the front of in, matching
// case-insensitively. Returns the remainder and whether it matched.
func cutSchemeFold(in, scheme string) (string, bool) {
	if len(in) >= len(scheme) && strings.EqualFold(in[:len(scheme)], scheme) {
		return in[len(scheme):], true
	}
	return in, false
}

// validateUrl normalizes the provider's location field in place and
// rejects a malformed one. Each provider addresses its store
// differently, so each gets its own rule:
//
//	s3     endpoint  — scheme stripped, transport security recorded
//	azure  containerid — optional azblob:// stripped, no slashes
//	file   dirpath   — optional file:// stripped, must be absolute
//	gcp    —           nothing to normalize; see the note below
//
// Presence of required fields is already checked by
// config.BlobConfig.Validate. This function is about shape.
func validateUrl(cfg config.BlobConfig) error {
	switch c := cfg.(type) {
	case *config.S3Config:
		// An empty endpoint is legitimate for provider "aws" — it means
		// AWS S3 itself. Normalizing it would fail on "empty endpoint".
		if c.Endpoint == "" {
			return nil
		}
		endpoint, secure, err := normalizeEndpoint(c.Endpoint, c.Insecure)
		if err != nil {
			return fmt.Errorf("storage (%s): %w", c.GetProvider(), err)
		}
		c.Endpoint = endpoint
		// normalizeEndpoint reports whether transport is *secure*.
		// Insecure is its inverse — assigning it directly would flip an
		// https:// endpoint to cleartext.
		c.Insecure = !secure

	case *config.AzureConfig:
		id := strings.TrimSpace(c.ContainerID)
		// azblob:// is accepted for symmetry with file:// but is not
		// required — a bare container name is the normal way to write it.
		if stripped, ok := cutSchemeFold(id, "azblob://"); ok {
			id = stripped
		}
		if id == "" {
			return fmt.Errorf("storage (%s): containerid is empty", c.GetProvider())
		}
		// gocloud puts the container in the URL host, which cannot hold a
		// path. A slash here means an account or path was pasted in.
		if strings.Contains(id, "/") {
			return fmt.Errorf("storage (%s): containerid %q must be a container name, not a path",
				c.GetProvider(), c.ContainerID)
		}
		c.ContainerID = id

	case *config.FileConfig:
		dir := strings.TrimSpace(c.DirPath)
		if dir == "" {
			return fmt.Errorf("storage (%s): dirpath is empty", c.GetProvider())
		}
		// file:// is accepted for symmetry with the other providers but
		// is not required — a bare path is the normal way to write this.
		if stripped, ok := cutSchemeFold(dir, "file://"); ok {
			dir = stripped
		}
		dir = filepath.Clean(dir)
		// Relative paths would resolve against the process working
		// directory, so the same config would name a different
		// destination depending on where confkoffer was run from. For a
		// backup target that is a silent way to lose snapshots.
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("storage (%s): dirpath %q must be absolute",
				c.GetProvider(), c.DirPath)
		}
		c.DirPath = dir

	case *config.GCPConfig:
		// Nothing to normalize. GCS is addressed as gs://<bucket> with no
		// scheme or endpoint to strip, and no region — a bucket's location
		// is fixed at creation. Presence of the bucket is covered by
		// Validate.
	}
	return nil
}

// withBucket opens the configured bucket, runs fn against it, and
// closes it again.
//
// The bucket is opened per call rather than held on the client. This is
// a one-shot CLI: pack and list perform a single operation, unpack two,
// and the process exits immediately after. Holding one open would save
// a single credential resolution on unpack, at the cost of every caller
// having to remember a Close. Here the deferred close is local and
// unmissable.
//
// The deferred Close runs when withBucket returns, which is after fn has
// finished — so fn may use the bucket freely.
func (bc *BlobClient) withBucket(ctx context.Context, fn func(context.Context, *blob.Bucket) error) error {
	u, err := bucketURL(bc.Config)
	if err != nil {
		return err
	}
	b, err := blob.OpenBucket(ctx, u)
	if err != nil {
		return fmt.Errorf("store: open bucket: %w", err)
	}
	defer b.Close()
	return fn(ctx, b)
}

// Put uploads body to key with content-type application/octet-stream.
// Retries on transient errors per DefaultBackoff.
func (bc *BlobClient) Put(ctx context.Context, key string, body []byte) error {
	return bc.withBucket(ctx, func(ctx context.Context, b *blob.Bucket) error {
		return withRetry(ctx, bc.Retry, func(ctx context.Context) error {
			return b.WriteAll(ctx, key, body, &blob.WriterOptions{
				ContentType: "application/octet-stream",
			})
		})
	})
}

// Get downloads the object at key and returns its bytes. Retries on
// transient errors. Objects larger than MaxBlobSize are rejected with
// ErrTooLarge.
func (bc *BlobClient) Get(ctx context.Context, key string) ([]byte, error) {
	var out []byte
	err := bc.withBucket(ctx, func(ctx context.Context, b *blob.Bucket) error {
		return withRetry(ctx, bc.Retry, func(ctx context.Context) error {
			r, err := b.NewReader(ctx, key, nil)
			if err != nil {
				return err
			}
			defer r.Close()
			// Read one byte past the cap so "exactly at the limit" is
			// distinguishable from "over it".
			buf, err := io.ReadAll(io.LimitReader(r, MaxBlobSize+1))
			if err != nil {
				return err
			}
			if len(buf) > MaxBlobSize {
				return fmt.Errorf("%w: %s (limit %d bytes)", ErrTooLarge, key, MaxBlobSize)
			}
			out = buf
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List returns all objects under prefix, sorted by LastModified
// descending (newest first). Empty result yields ErrNoSnapshots so
// callers can map it to exit code 1 with a clean message.
//
// The whole prefix is read. That is deliberate, and a max-count cap
// here would be a correctness bug rather than a safeguard:
//
// Drivers return keys in ascending lexicographic order, and because
// KeyForName writes fixed-width zero-padded RFC3339, lexicographic
// order is chronological order — so the objects arrive OLDEST FIRST.
// Truncating the scan keeps the oldest N and discards the newest.
// unpack then takes objects[0] and restores a stale snapshot, exit
// code 0, no warning.
//
// Stopping early is not available on the mtime path either: PickAt and
// the newest-first default select on LastModified, which the store
// alone knows and which need not agree with key order (see PickAt). You
// cannot tell which object has the greatest mtime without seeing all of
// them.
//
// Memory is not the constraint that would justify the risk — roughly
// 112 bytes per object, so 100k snapshots is ~11 MB, against a 256 MiB
// MaxBlobSize for a single Get and a pack path that holds the whole
// archive in memory. The real cost of a large prefix is round trips
// (1000 keys per request on S3), which retention fixes at the source:
// https://github.com/renewelches/confkoffer/issues/21
func (bc *BlobClient) List(ctx context.Context, prefix string) ([]Object, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var objects []Object
	err := bc.withBucket(ctx, func(ctx context.Context, b *blob.Bucket) error {
		return withRetry(ctx, bc.Retry, func(ctx context.Context) error {
			objects = nil // a retry must not append to a half-built list
			it := b.List(&blob.ListOptions{Prefix: prefix})
			for {
				obj, err := it.Next(ctx)
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				// Skip directory placeholders; some backends synthesise them.
				if obj.IsDir || strings.HasSuffix(obj.Key, "/") {
					continue
				}
				objects = append(objects, Object{
					Key:          obj.Key,
					Size:         obj.Size,
					LastModified: obj.ModTime,
				})
			}
		})
	})
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("%w under %q", ErrNoSnapshots, strings.TrimSuffix(prefix, "/"))
	}
	sortByLastModifiedDesc(objects)
	return objects, nil
}

// ErrNoSnapshots is returned when List finds zero objects under prefix.
var ErrNoSnapshots = errors.New("no snapshots found")

// PickAt returns the newest object whose LastModified is at or before t.
// Returns ErrNoSnapshots when no object satisfies the cutoff.
//
// The cutoff is applied to LastModified — the store's own timestamp —
// not to the timestamp embedded in the key. The two normally agree, but
// LastModified is when the object was last written to *this* store,
// which is what a point-in-time restore is asking about. The key's
// timestamp records when the snapshot was originally packed and travels
// with the bytes: copy, sync, or restore an object from a lifecycle
// tier and the two diverge. Selecting on the key would then hand back a
// snapshot that was not in this bucket at time t.
//
// The practical consequence is that a store with a skewed clock skews
// --at with it. That is the intended tradeoff — the server is the
// authority on what it held and when.
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
