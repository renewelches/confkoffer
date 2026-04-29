package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// HostFingerprintLen is the number of hex chars taken from the hostname
// hash to embed in object keys.
const HostFingerprintLen = 6

// SuffixExt is the canonical extension for confkoffer blobs.
const SuffixExt = ".enc"

// hostnameFn is a seam for tests; returns the machine hostname.
var hostnameFn = os.Hostname

// nowFn is a seam for tests; returns the current UTC time.
var nowFn = func() time.Time { return time.Now().UTC() }

// randomFn is a seam for tests; fills b with cryptographic randomness.
var randomFn = func(b []byte) error {
	_, err := io.ReadFull(rand.Reader, b)
	return err
}

// KeyForName builds the S3 object key for a new snapshot under name.
// Format: <name>/<RFC3339-utc-with-colons-as-dashes>-<host6>-<rand4>.enc
//
// Example: prod/aws/useast/2026-04-28T12-34-56Z-a3f91c-7d4e.enc
func KeyForName(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("KeyForName: empty name")
	}
	host, err := hostnameFn()
	if err != nil || host == "" {
		host = "unknown"
	}
	sum := sha256.Sum256([]byte(host))
	host6 := hex.EncodeToString(sum[:])[:HostFingerprintLen]

	var rb [2]byte
	if err := randomFn(rb[:]); err != nil {
		return "", fmt.Errorf("KeyForName: rand: %w", err)
	}
	rand4 := hex.EncodeToString(rb[:])

	ts := strings.ReplaceAll(nowFn().Format(time.RFC3339), ":", "-")

	return fmt.Sprintf("%s/%s-%s-%s%s", name, ts, host6, rand4, SuffixExt), nil
}
