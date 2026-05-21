package crypto_test

import (
	"errors"
	"strings"
	"testing"

	"instant.dev/common/crypto"
)

// Cover the Error/Unwrap methods of the typed AES errors. These are surfaced to
// callers who use errors.Is/errors.As to distinguish failure modes.

func TestErrEncrypt_Wrapping(t *testing.T) {
	cause := errors.New("enc boom")
	e := &crypto.ErrEncrypt{Cause: cause}
	if !strings.Contains(e.Error(), "enc boom") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Error("Unwrap should return cause")
	}
}

func TestErrDecrypt_Wrapping(t *testing.T) {
	cause := errors.New("dec boom")
	e := &crypto.ErrDecrypt{Cause: cause}
	if !strings.Contains(e.Error(), "dec boom") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Error("Unwrap should return cause")
	}
}

func TestEncrypt_BadKeyLen(t *testing.T) {
	// 5-byte key — AES rejects.
	_, err := crypto.Encrypt([]byte{1, 2, 3, 4, 5}, "x")
	if err == nil {
		t.Error("expected error for invalid key length")
	}
}

func TestDecrypt_BadKeyLen(t *testing.T) {
	_, err := crypto.Decrypt([]byte{1, 2, 3}, "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBka")
	if err == nil {
		t.Error("expected error for invalid key length")
	}
}
