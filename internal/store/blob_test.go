package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gocloud.dev/gcerrors"

	"github.com/renewelches/confkoffer/internal/config"
)

// fileClient returns a BlobClient backed by a real directory, so the
// Put/Get/List paths are exercised end to end without credentials.
func fileClient(t *testing.T) *BlobClient {
	t.Helper()
	cli, err := New(&config.FileConfig{
		ProviderConfig: config.ProviderConfig{Provider: "file"},
		DirPath:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cli
}

func TestPutGetRoundTrip(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()

	const key = "proj/2026-04-28T12-34-56Z-7d4e.enc"
	want := []byte("\x02ciphertext-with-\x00-bytes")

	if err := cli.Put(ctx, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := cli.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Get = %q, want %q", got, want)
	}
}

func TestPutOverwritesSameKey(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()
	const key = "proj/dup.enc"

	if err := cli.Put(ctx, key, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := cli.Put(ctx, key, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := cli.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("Get = %q, want %q", got, "second")
	}
}

// A missing key is a definitive answer, not a flake. If isTransient does
// not recognise the backend's error it falls through to "retry", and
// this burns the whole backoff schedule (10.5s) before reporting what
// the first attempt already knew. The elapsed-time assertion is what
// catches that regression; the error check alone would not.
func TestGetMissingKey(t *testing.T) {
	cli := fileClient(t)
	start := time.Now()
	_, err := cli.Get(context.Background(), "proj/nope.enc")
	if err == nil {
		t.Fatal("Get on a missing key = nil, want error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Get on a missing key took %s — it is being retried; "+
			"isTransient should classify NotFound as permanent", elapsed)
	}
	if gcerrors.Code(err) != gcerrors.NotFound {
		t.Errorf("error code = %v, want NotFound", gcerrors.Code(err))
	}
}

func TestGetEmptyObject(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()
	const key = "proj/empty.enc"
	if err := cli.Put(ctx, key, []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := cli.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get = %q, want empty", got)
	}
}

func TestListReturnsOnlyMatchingPrefix(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()

	want := []string{
		"proj/a.enc",
		"proj/b.enc",
		"proj/nested/c.enc",
	}
	for _, k := range append(append([]string{}, want...), "other/x.enc") {
		if err := cli.Put(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	objs, err := cli.List(ctx, "proj")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != len(want) {
		t.Fatalf("List returned %d objects, want %d: %+v", len(objs), len(want), objs)
	}
	got := map[string]bool{}
	for _, o := range objs {
		got[o.Key] = true
		if !strings.HasPrefix(o.Key, "proj/") {
			t.Errorf("key %q is outside the requested prefix", o.Key)
		}
		if o.Size != 1 {
			t.Errorf("key %q has Size=%d, want 1", o.Key, o.Size)
		}
		if o.LastModified.IsZero() {
			t.Errorf("key %q has zero LastModified", o.Key)
		}
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("List did not return %q", k)
		}
	}
}

// List must return newest-first so unpack's default (objs[0]) and PickAt
// both work. Timestamps can tie at filesystem granularity, so assert the
// ordering is non-increasing rather than a specific permutation.
func TestListIsNewestFirst(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()
	for _, k := range []string{"proj/a.enc", "proj/b.enc", "proj/c.enc"} {
		if err := cli.Put(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	objs, err := cli.List(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(objs); i++ {
		if objs[i].LastModified.After(objs[i-1].LastModified) {
			t.Errorf("objects not newest-first: [%d]=%s is after [%d]=%s",
				i, objs[i].LastModified, i-1, objs[i-1].LastModified)
		}
	}
}

// A caller that gets ErrNoSnapshots can report exit code 1 with a clean
// message instead of a nil-slice panic downstream.
func TestListEmptyIsErrNoSnapshots(t *testing.T) {
	cli := fileClient(t)
	objs, err := cli.List(context.Background(), "proj")
	if !errors.Is(err, ErrNoSnapshots) {
		t.Fatalf("List = (%v, %v), want ErrNoSnapshots", objs, err)
	}
	if objs != nil {
		t.Errorf("List returned %v alongside the error, want nil", objs)
	}
}

// The prefix is normalized with a trailing slash, so "proj" must not
// also match a sibling like "project".
func TestListPrefixIsSlashBounded(t *testing.T) {
	cli := fileClient(t)
	ctx := context.Background()
	if err := cli.Put(ctx, "project/x.enc", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.List(ctx, "proj"); !errors.Is(err, ErrNoSnapshots) {
		t.Errorf("List(\"proj\") matched \"project/\": err = %v", err)
	}
}

func TestNewRejectsNilConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) = nil error, want error")
	}
}

// New runs validateUrl, so a config that cannot be addressed fails at
// construction rather than at the first operation.
func TestNewRejectsRelativeDirPath(t *testing.T) {
	_, err := New(&config.FileConfig{
		ProviderConfig: config.ProviderConfig{Provider: "file"},
		DirPath:        "relative/path",
	})
	if err == nil {
		t.Fatal("New with a relative dirpath = nil error, want error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err = %q, want it to mention absolute", err)
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
		{Key: "n", LastModified: now},                       // 0h ago
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

// PickAt selects on LastModified, not on the timestamp in the key. The
// two diverge whenever objects are copied, synced, or restored from a
// lifecycle tier, so a test where they agree cannot tell which one is
// being read. Here they are deliberately contradictory.
func TestPickAtUsesLastModifiedNotTheKey(t *testing.T) {
	now := time.Now().UTC()
	objs := []Object{
		// Key says it was packed long ago; the store wrote it just now —
		// as happens after an `aws s3 sync` into a fresh bucket.
		{Key: "proj/2020-01-01T00-00-00Z-aaaa.enc", LastModified: now},
		{Key: "proj/2030-01-01T00-00-00Z-bbbb.enc", LastModified: now.Add(-3 * time.Hour)},
	}
	got, err := PickAt(objs, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "proj/2030-01-01T00-00-00Z-bbbb.enc" {
		t.Errorf("PickAt selected %q; it is reading the key's timestamp "+
			"instead of LastModified", got.Key)
	}
}
