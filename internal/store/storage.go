package store

import (
	"context"
)

// Storage is the object-store surface pack, unpack, and list depend on.
//
// PickAt is deliberately not a member: it is a pure function over the
// []Object that List already returned, so putting it here would oblige
// every implementation — including a test fake — to reimplement
// identical cutoff logic. It stays a package-level function.
type Storage interface {
	// Put uploads body to key with content-type application/octet-stream.
	// Retries on transient errors per DefaultBackoff.
	Put(ctx context.Context, key string, body []byte) error

	// Get downloads the object at key and returns its bytes. Retries on
	// transient errors. Objects larger than MaxBlobSize are rejected with
	// ErrTooLarge.
	Get(ctx context.Context, key string) ([]byte, error)

	// List returns all objects under prefix, sorted by LastModified
	// descending (newest first). Empty result yields ErrNoSnapshots so
	// callers can map it to exit code 1 with a clean message.
	List(ctx context.Context, prefix string) ([]Object, error)
}

// Nothing in the tree consumed Storage, so *BlobClient drifted out of
// conformance without anything failing to build. This assertion is what
// makes the interface load-bearing.
var _ Storage = (*BlobClient)(nil)
