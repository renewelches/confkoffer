package crypto

import (
	"bytes"
	"errors"
	"testing"
)

// testParams is a deliberately weak Argon2id profile so the crypto tests
// run quickly. They exist to verify wiring, not KDF strength.
func testParams() Params {
	return Params{MemoryKiB: 64, Time: 1, Threads: 1}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	password := []byte("correct horse battery staple")

	blob, err := Encrypt(plaintext, password, testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(blob) <= HeaderSize {
		t.Fatalf("blob too short: %d", len(blob))
	}

	got, err := Decrypt(blob, password)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch: got %q want %q", got, plaintext)
	}
}

func TestDecryptWrongPasswordReturnsCanonicalError(t *testing.T) {
	blob, err := Encrypt([]byte("data"), []byte("right"), testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = Decrypt(blob, []byte("wrong"))
	if !errors.Is(err, ErrDecryption) {
		t.Fatalf("got %v, want ErrDecryption", err)
	}
}

func TestTamperedCiphertextFailsAuth(t *testing.T) {
	blob, err := Encrypt([]byte("data"), []byte("pw"), testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Flip a byte in the ciphertext region (after the 35-byte header).
	tampered := append([]byte{}, blob...)
	tampered[HeaderSize+1] ^= 0xFF

	_, err = Decrypt(tampered, []byte("pw"))
	if !errors.Is(err, ErrDecryption) {
		t.Fatalf("got %v, want ErrDecryption", err)
	}
}

func TestTamperedHeaderFailsAADCheck(t *testing.T) {
	// Specifically: modifying the recorded `time` byte must fail decryption.
	// This proves the header is bound to the ciphertext via AAD.
	blob, err := Encrypt([]byte("data"), []byte("pw"), testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte{}, blob...)
	tampered[5] = 99 // overwrite Time byte

	_, err = Decrypt(tampered, []byte("pw"))
	if !errors.Is(err, ErrDecryption) {
		t.Fatalf("got %v, want ErrDecryption", err)
	}
}

func TestTamperedSaltFailsAADCheck(t *testing.T) {
	blob, err := Encrypt([]byte("data"), []byte("pw"), testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte{}, blob...)
	tampered[7] ^= 0xFF // first salt byte

	_, err = Decrypt(tampered, []byte("pw"))
	if !errors.Is(err, ErrDecryption) {
		t.Fatalf("got %v, want ErrDecryption", err)
	}
}

func TestUnsupportedVersionRejected(t *testing.T) {
	blob, err := Encrypt([]byte("data"), []byte("pw"), testParams())
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte{}, blob...)
	tampered[0] = 0x01 // older / unknown version

	_, err = Decrypt(tampered, []byte("pw"))
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got %v, want ErrUnsupportedVersion", err)
	}
}

func TestTruncatedHeaderRejected(t *testing.T) {
	short := make([]byte, HeaderSize-1)
	_, err := Decrypt(short, []byte("pw"))
	if !errors.Is(err, ErrTruncatedHeader) {
		t.Fatalf("got %v, want ErrTruncatedHeader", err)
	}
}

func TestTruncatedCiphertextRejected(t *testing.T) {
	// Header present but ciphertext shorter than the GCM tag (16 bytes).
	header := Header{Version: Version, Params: testParams()}
	blob := append(header.MarshalBinary(), []byte{0x00}...) // 1 byte of "ct"
	_, err := Decrypt(blob, []byte("pw"))
	if !errors.Is(err, ErrTruncatedHeader) {
		t.Fatalf("got %v, want ErrTruncatedHeader", err)
	}
}

func TestEmptyPasswordRejectedOnEncrypt(t *testing.T) {
	_, err := Encrypt([]byte("data"), nil, testParams())
	if err == nil {
		t.Fatal("expected error for empty password on encrypt")
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	cases := []Params{
		{MemoryKiB: 0, Time: 1, Threads: 1},
		{MemoryKiB: 64, Time: 0, Threads: 1},
		{MemoryKiB: 64, Time: 1, Threads: 0},
	}
	for i, p := range cases {
		_, err := Encrypt([]byte("data"), []byte("pw"), p)
		if err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, p)
		}
	}
}

func TestSaltAndNonceAreRandomized(t *testing.T) {
	// Encrypting the same plaintext twice must produce different blobs;
	// otherwise salt/nonce randomness is broken.
	password := []byte("pw")
	blob1, err := Encrypt([]byte("data"), password, testParams())
	if err != nil {
		t.Fatal(err)
	}
	blob2, err := Encrypt([]byte("data"), password, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(blob1, blob2) {
		t.Fatal("two encryptions produced identical output — randomness broken")
	}
	// Salts and nonces should also differ.
	if bytes.Equal(blob1[7:23], blob2[7:23]) {
		t.Fatal("salts identical across encryptions")
	}
	if bytes.Equal(blob1[23:35], blob2[23:35]) {
		t.Fatal("nonces identical across encryptions")
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	in := Header{
		Version: Version,
		Params:  Params{MemoryKiB: 19456, Time: 2, Threads: 1},
	}
	for i := range in.Salt {
		in.Salt[i] = byte(i)
	}
	for i := range in.Nonce {
		in.Nonce[i] = byte(i + 100)
	}

	out, err := UnmarshalHeader(in.MarshalBinary())
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestParamsCarriedInBlob(t *testing.T) {
	// A blob encrypted with non-default params must decrypt with the
	// same non-default params even if the caller supplies no params on
	// decrypt — the header is the source of truth.
	custom := Params{MemoryKiB: 256, Time: 3, Threads: 2}
	blob, err := Encrypt([]byte("data"), []byte("pw"), custom)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(blob, []byte("pw"))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("got %q want %q", got, "data")
	}

	// And the header parses to the same params.
	h, err := UnmarshalHeader(blob)
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if h.Params != custom {
		t.Fatalf("header params %+v != stored %+v", h.Params, custom)
	}
}

func TestZeroOverwrites(t *testing.T) {
	b := []byte{1, 2, 3, 4}
	Zero(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("b[%d]=%d, want 0", i, v)
		}
	}
}
