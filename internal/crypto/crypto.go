// Package crypto encrypts and decrypts confkoffer blobs.
//
// Blob layout (version 0x02):
//
//	+---------------------------------------------------+
//	|  1 byte : version = 0x02                          |
//	|  4 bytes: argon2id memory KiB (uint32 big-endian) |
//	|  1 byte : argon2id time                           |
//	|  1 byte : argon2id threads                        |
//	| 16 bytes: salt                                    |
//	| 12 bytes: AES-GCM nonce                           |
//	+---------------------------------------------------+
//	|  N bytes: AES-256-GCM(ciphertext + 16-byte tag)   |
//	+---------------------------------------------------+
//
// The 35-byte header is passed as AAD to AES-GCM, so any tampering with
// the header (e.g. weakening the recorded KDF parameters) makes
// decryption fail with a tag mismatch. Each blob carries the parameters
// it was encrypted with — changing the project's KDF config later does
// not invalidate older blobs.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// Version is the only blob version this build understands.
	Version byte = 0x02

	// HeaderSize is the fixed cleartext header length in bytes.
	HeaderSize = 1 + 4 + 1 + 1 + 16 + 12 // 35

	saltSize  = 16
	nonceSize = 12
	keySize   = 32 // AES-256
)

// Params controls argon2id work factors. They are persisted in each blob's
// header so callers can change them later without breaking old blobs.
type Params struct {
	MemoryKiB uint32 // Argon2id memory cost in KiB
	Time      uint8  // Argon2id time cost (iterations)
	Threads   uint8  // Argon2id parallelism
}

// DefaultParams matches the OWASP "second-choice" Argon2id profile
// (19 MiB / t=2 / p=1). Strong enough for interactive use while staying
// runnable on small machines.
func DefaultParams() Params {
	return Params{
		MemoryKiB: 19456,
		Time:      2,
		Threads:   1,
	}
}

// Validate rejects nonsense parameter combinations early.
func (p Params) Validate() error {
	if p.MemoryKiB < 8*uint32(p.Threads) || p.MemoryKiB == 0 {
		return fmt.Errorf("argon2id memory_kib %d is too small (must be >= 8*threads and > 0)", p.MemoryKiB)
	}
	if p.Time == 0 {
		return errors.New("argon2id time must be >= 1")
	}
	if p.Threads == 0 {
		return errors.New("argon2id threads must be >= 1")
	}
	return nil
}

// Header is the parsed cleartext blob header.
type Header struct {
	Version byte
	Params  Params
	Salt    [saltSize]byte
	Nonce   [nonceSize]byte
}

// MarshalBinary serializes the header to its 35-byte wire form.
func (h Header) MarshalBinary() []byte {
	buf := make([]byte, HeaderSize)
	buf[0] = h.Version
	binary.BigEndian.PutUint32(buf[1:5], h.Params.MemoryKiB)
	buf[5] = h.Params.Time
	buf[6] = h.Params.Threads
	copy(buf[7:23], h.Salt[:])
	copy(buf[23:35], h.Nonce[:])
	return buf
}

// UnmarshalHeader parses a 35-byte header. Returns ErrTruncatedHeader if
// the input is too short and ErrUnsupportedVersion if the version byte is
// not recognised by this build.
func UnmarshalHeader(b []byte) (Header, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, ErrTruncatedHeader
	}
	h.Version = b[0]
	if h.Version != Version {
		return h, fmt.Errorf("%w: 0x%02x", ErrUnsupportedVersion, h.Version)
	}
	h.Params.MemoryKiB = binary.BigEndian.Uint32(b[1:5])
	h.Params.Time = b[5]
	h.Params.Threads = b[6]
	copy(h.Salt[:], b[7:23])
	copy(h.Nonce[:], b[23:35])
	return h, nil
}

// Sentinel errors. Decrypt always returns ErrDecryption on auth failure
// regardless of whether the cause was a wrong password, a tampered
// ciphertext, or a tampered header — never reveal which.
var (
	ErrUnsupportedVersion = errors.New("unsupported blob version")
	ErrTruncatedHeader    = errors.New("blob truncated: header incomplete")
	ErrDecryption         = errors.New("decryption failed: invalid password or corrupted blob")
)

// Encrypt seals plaintext under password using argon2id+AES-256-GCM and
// returns header||ciphertext. Salt and nonce are drawn from crypto/rand.
// The returned slice is safe to write to disk or upload as-is.
func Encrypt(plaintext, password []byte, p Params) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if len(password) == 0 {
		return nil, errors.New("password must not be empty")
	}

	h := Header{Version: Version, Params: p}
	if _, err := io.ReadFull(rand.Reader, h.Salt[:]); err != nil {
		return nil, fmt.Errorf("read salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, h.Nonce[:]); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}

	key := deriveKey(password, h.Salt[:], p)
	defer Zero(key)

	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	headerBytes := h.MarshalBinary()
	// Seal appends ciphertext+tag to the destination; we hand it the header
	// so the final blob is header||ciphertext laid out in one contiguous
	// allocation. The header is also AAD, so any tamper invalidates the tag.
	out := gcm.Seal(headerBytes, h.Nonce[:], plaintext, headerBytes)
	return out, nil
}

// Decrypt verifies and opens a blob produced by Encrypt. The KDF
// parameters used are read from the blob's own header.
func Decrypt(blob, password []byte) ([]byte, error) {
	h, err := UnmarshalHeader(blob)
	if err != nil {
		return nil, err
	}
	if len(blob) < HeaderSize+16 { // GCM tag is 16 bytes
		return nil, ErrTruncatedHeader
	}
	if err := h.Params.Validate(); err != nil {
		// A blob with invalid recorded params is treated as corrupt, not
		// as a config error — we never trust attacker-controlled bytes.
		return nil, ErrDecryption
	}
	if len(password) == 0 {
		return nil, errors.New("password must not be empty")
	}

	key := deriveKey(password, h.Salt[:], h.Params)
	defer Zero(key)

	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	headerBytes := blob[:HeaderSize]
	ciphertext := blob[HeaderSize:]

	plaintext, err := gcm.Open(nil, h.Nonce[:], ciphertext, headerBytes)
	if err != nil {
		return nil, ErrDecryption
	}
	return plaintext, nil
}

// Zero overwrites b with zeros — best-effort memory hygiene for password
// and key material. Go's GC may have already copied the buffer, but we do
// what we can.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func deriveKey(password, salt []byte, p Params) []byte {
	return argon2.IDKey(password, salt, uint32(p.Time), p.MemoryKiB, p.Threads, keySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return gcm, nil
}
