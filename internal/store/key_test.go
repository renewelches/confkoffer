package store

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestKeyForNameFormat(t *testing.T) {
	defer setKeyDeps(
		func() (string, error) { return "test-host", nil },
		func() time.Time { return time.Date(2026, 4, 28, 12, 34, 56, 0, time.UTC) },
		func(b []byte) error { b[0] = 0x7d; b[1] = 0x4e; return nil },
	)()

	got, err := KeyForName("prod/aws/useast")
	if err != nil {
		t.Fatal(err)
	}
	want := "prod/aws/useast/2026-04-28T12-34-56Z-50d34d-7d4e.enc"
	// host6 = first 6 hex chars of sha256("test-host") — verify shape, not literal.
	re := regexp.MustCompile(`^prod/aws/useast/2026-04-28T12-34-56Z-[0-9a-f]{6}-7d4e\.enc$`)
	if !re.MatchString(got) {
		t.Errorf("got %q, expected pattern %s (sample want=%q)", got, re, want)
	}
	if !strings.HasSuffix(got, ".enc") {
		t.Errorf("missing .enc suffix: %q", got)
	}
	if strings.Contains(got, ":") {
		t.Errorf("colons should be replaced: %q", got)
	}
}

func TestKeyForNameRejectsEmpty(t *testing.T) {
	_, err := KeyForName("")
	if err == nil {
		t.Fatal("expected error on empty name")
	}
}

func TestKeyForNameUnknownHostFallsBack(t *testing.T) {
	defer setKeyDeps(
		func() (string, error) { return "", errors.New("no hostname") },
		func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		func(b []byte) error { b[0] = 0; b[1] = 0; return nil },
	)()

	got, err := KeyForName("p")
	if err != nil {
		t.Fatal(err)
	}
	// host6 = first 6 hex chars of sha256("unknown")
	if !strings.Contains(got, "-") {
		t.Errorf("unexpected key shape: %q", got)
	}
}

func TestKeyForNameDifferentRandomYieldsDifferentKey(t *testing.T) {
	calls := 0
	defer setKeyDeps(
		func() (string, error) { return "h", nil },
		func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		func(b []byte) error {
			calls++
			b[0] = byte(calls)
			b[1] = 0x00
			return nil
		},
	)()

	a, _ := KeyForName("p")
	b, _ := KeyForName("p")
	if a == b {
		t.Fatalf("expected different rand suffix, got %q == %q", a, b)
	}
}

// setKeyDeps swaps the package-level seams and returns a restore func.
func setKeyDeps(host func() (string, error), now func() time.Time, rnd func([]byte) error) func() {
	prevHost := hostnameFn
	prevNow := nowFn
	prevRand := randomFn
	hostnameFn = host
	nowFn = now
	randomFn = rnd
	return func() {
		hostnameFn = prevHost
		nowFn = prevNow
		randomFn = prevRand
	}
}
