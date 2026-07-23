package store

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestKeyForNameFormat(t *testing.T) {
	defer setKeyDeps(
		func() time.Time { return time.Date(2026, 4, 28, 12, 34, 56, 0, time.UTC) },
		func(b []byte) error { b[0] = 0x7d; b[1] = 0x4e; return nil },
	)()

	got, err := KeyForName("prod/aws/useast")
	if err != nil {
		t.Fatal(err)
	}
	want := "prod/aws/useast/2026-04-28T12-34-56Z-7d4e.enc"
	re := regexp.MustCompile(`^prod/aws/useast/2026-04-28T12-34-56Z-7d4e\.enc$`)
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

func TestKeyForNameDifferentRandomYieldsDifferentKey(t *testing.T) {
	calls := 0
	defer setKeyDeps(
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
func setKeyDeps(now func() time.Time, rnd func([]byte) error) func() {
	prevNow := nowFn
	prevRand := randomFn
	nowFn = now
	randomFn = rnd
	return func() {
		nowFn = prevNow
		randomFn = prevRand
	}
}
