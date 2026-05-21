package crypto_test

import (
	"errors"
	"strings"
	"testing"

	"instant.dev/common/crypto"
)

func TestGenerateAPIKey_Prefix(t *testing.T) {
	k1, err := crypto.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(k1, "inst_live_") {
		t.Errorf("missing prefix: %q", k1)
	}
	// 32-byte body → base64url ~43 chars; full key length is 10 + ~43.
	if len(k1) < 30 {
		t.Errorf("key suspiciously short: %q", k1)
	}
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	a, _ := crypto.GenerateAPIKey()
	b, _ := crypto.GenerateAPIKey()
	if a == b {
		t.Errorf("two keys collided: %q == %q", a, b)
	}
}

func TestErrTokenGenerate_Wrapping(t *testing.T) {
	cause := errors.New("rng broke")
	e := &crypto.ErrTokenGenerate{Cause: cause}
	if !strings.Contains(e.Error(), "rng broke") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Errorf("Unwrap should return cause")
	}
}
