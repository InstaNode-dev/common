package crypto_test

import (
	"strings"
	"testing"

	"instant.dev/common/crypto"
)

var testKey = func() []byte {
	k, err := crypto.ParseAESKey("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		panic(err)
	}
	return k
}()

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	plaintext := "postgres://usr:pass@host/db"
	enc, err := crypto.Encrypt(testKey, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if enc == "" {
		t.Fatal("expected non-empty ciphertext")
	}

	dec, err := crypto.Decrypt(testKey, enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, dec)
	}
}

func TestEncrypt_ProducesDistinctCiphertexts(t *testing.T) {
	// Two encryptions of the same plaintext must differ (nonce is random).
	plaintext := "same-secret"
	enc1, _ := crypto.Encrypt(testKey, plaintext)
	enc2, _ := crypto.Encrypt(testKey, plaintext)
	if enc1 == enc2 {
		t.Fatal("expected distinct ciphertexts for the same plaintext (nonce must be random)")
	}
}

func TestDecrypt_WrongKey_ReturnsError(t *testing.T) {
	enc, _ := crypto.Encrypt(testKey, "secret")

	wrongKey, _ := crypto.ParseAESKey("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	_, err := crypto.Decrypt(wrongKey, enc)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestDecrypt_TamperedCiphertext_ReturnsError(t *testing.T) {
	enc, _ := crypto.Encrypt(testKey, "secret")
	tampered := enc[:len(enc)-4] + "XXXX" // corrupt the last bytes
	_, err := crypto.Decrypt(testKey, tampered)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

func TestDecrypt_InvalidBase64_ReturnsError(t *testing.T) {
	_, err := crypto.Decrypt(testKey, "!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecrypt_TooShort_ReturnsError(t *testing.T) {
	// A valid base64 string that's too short to contain a nonce.
	_, err := crypto.Decrypt(testKey, "YQ==") // decodes to single byte "a"
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestEncryptDecrypt_EmptyString(t *testing.T) {
	enc, err := crypto.Encrypt(testKey, "")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	dec, err := crypto.Decrypt(testKey, enc)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if dec != "" {
		t.Fatalf("expected empty string, got %q", dec)
	}
}

func TestEncryptDecrypt_LongValue(t *testing.T) {
	// Connection URLs can be long.
	long := "postgres://usr_" + strings.Repeat("x", 200) + ":pass@host/db"
	enc, err := crypto.Encrypt(testKey, long)
	if err != nil {
		t.Fatalf("Encrypt long: %v", err)
	}
	dec, err := crypto.Decrypt(testKey, enc)
	if err != nil {
		t.Fatalf("Decrypt long: %v", err)
	}
	if dec != long {
		t.Fatalf("roundtrip failed for long value")
	}
}

func TestParseAESKey_ValidHex(t *testing.T) {
	key, err := crypto.ParseAESKey("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(key))
	}
}

func TestParseAESKey_TooShort_ReturnsError(t *testing.T) {
	_, err := crypto.ParseAESKey("deadbeef") // only 4 bytes
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestParseAESKey_InvalidHex_ReturnsError(t *testing.T) {
	_, err := crypto.ParseAESKey("zzzz")
	if err == nil {
		t.Fatal("expected error for non-hex input")
	}
}
