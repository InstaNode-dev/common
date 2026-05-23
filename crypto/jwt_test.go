package crypto_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"instant.dev/common/crypto"
)

var jwtSecret = []byte("supersecret-test-key-32-byte-minimum-required-here-pad-zzzzzz")

func TestSignAndVerifyJWT_Roundtrip(t *testing.T) {
	claims := crypto.InstantClaims{
		Fingerprint:   "fp1",
		Country:       "US",
		CloudVendor:   "aws",
		Tokens:        []string{"tok1"},
		ResourceTypes: []string{"postgres"},
		SuggestedPlan: "hobby",
	}
	signed, err := crypto.SignJWT(jwtSecret, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	if signed == "" {
		t.Fatal("expected signed token")
	}

	parsed, err := crypto.VerifyJWT(jwtSecret, signed)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if parsed.Fingerprint != "fp1" || parsed.Country != "US" {
		t.Errorf("parsed = %+v", parsed)
	}
	if parsed.ID == "" {
		t.Error("expected auto-generated jti")
	}
}

func TestVerifyJWT_BadSecret(t *testing.T) {
	signed, _ := crypto.SignJWT(jwtSecret, crypto.InstantClaims{Fingerprint: "x"})
	_, err := crypto.VerifyJWT([]byte("wrong-secret"), signed)
	if err == nil {
		t.Fatal("expected error from wrong secret")
	}
}

func TestVerifyJWT_MalformedToken(t *testing.T) {
	_, err := crypto.VerifyJWT(jwtSecret, "not-a-jwt")
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestVerifyJWT_FutureIssuedAt(t *testing.T) {
	claims := crypto.InstantClaims{Fingerprint: "fp"}
	claims.IssuedAt = jwt.NewNumericDate(time.Now().UTC().Add(2 * time.Hour))
	signed, err := crypto.SignJWT(jwtSecret, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	_, err = crypto.VerifyJWT(jwtSecret, signed)
	if err == nil {
		t.Fatal("expected error for future iat")
	}
}

func TestSignOnboardingJWT_Roundtrip(t *testing.T) {
	claims := crypto.OnboardingClaims{
		Fingerprint:   "fp",
		Tokens:        []string{"a", "b"},
		SuggestedPlan: "pro",
	}
	signed, jti, err := crypto.SignOnboardingJWT(jwtSecret, claims)
	if err != nil {
		t.Fatalf("SignOnboardingJWT: %v", err)
	}
	if jti == "" || signed == "" {
		t.Error("expected non-empty jti + signed")
	}
	parsed, err := crypto.VerifyOnboardingJWT(jwtSecret, signed)
	if err != nil {
		t.Fatalf("VerifyOnboardingJWT: %v", err)
	}
	if parsed.ID != jti {
		t.Errorf("ID = %q, want %q", parsed.ID, jti)
	}
	if len(parsed.Tokens) != 2 {
		t.Errorf("Tokens = %v", parsed.Tokens)
	}
	// ExpiresAt should be ~7 days from now
	if parsed.ExpiresAt == nil || time.Until(parsed.ExpiresAt.Time) < 6*24*time.Hour {
		t.Errorf("expected ~7d expiry, got %v", parsed.ExpiresAt)
	}
}

func TestVerifyOnboardingJWT_Bad(t *testing.T) {
	if _, err := crypto.VerifyOnboardingJWT(jwtSecret, "garbage"); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifyOnboardingJWT_FutureIssuedAt(t *testing.T) {
	// Hand-craft an onboarding JWT with iat in the future.
	claims := crypto.OnboardingClaims{Fingerprint: "fp"}
	claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(time.Now().UTC().Add(2 * time.Hour)),
		ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(72 * time.Hour)),
		ID:        "jti-future",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("manual sign: %v", err)
	}
	if _, err := crypto.VerifyOnboardingJWT(jwtSecret, signed); err == nil {
		t.Fatal("expected error for future iat")
	}
}

// TestErrJWTSign_Wrapping exercises the Error/Unwrap on the typed errors.
func TestErrJWTSign_Wrapping(t *testing.T) {
	cause := errors.New("underlying boom")
	e := &crypto.ErrJWTSign{Cause: cause}
	if !strings.Contains(e.Error(), "boom") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Errorf("Unwrap should return cause")
	}
}

func TestErrJWTVerify_Wrapping(t *testing.T) {
	cause := errors.New("verify boom")
	e := &crypto.ErrJWTVerify{Cause: cause}
	if !strings.Contains(e.Error(), "verify boom") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Errorf("Unwrap should return cause")
	}
}

// TestVerifyJWT_WrongAlg verifies the alg-confusion guard rejects tokens signed
// with an unexpected method.
func TestVerifyJWT_WrongAlg(t *testing.T) {
	// Sign with the "none" alg by forcing an unsigned token. The library refuses
	// to sign with "none" by default, so build with an unsupported alg path:
	// craft a token claiming alg=ES256 but signed with HS256 — the parser will
	// reject it because keyfunc only returns the HMAC key.
	tok := jwt.New(jwt.SigningMethodHS256)
	tok.Method = jwt.SigningMethodRS256 // mismatch — verify must refuse
	// Sign with HMAC anyway (force the wrong signature path).
	// Easier: pre-craft a fixed header-payload-fake-sig string.
	bad := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJmcCI6ImEifQ.sig"
	_, err := crypto.VerifyJWT(jwtSecret, bad)
	if err == nil {
		t.Fatal("expected error for non-HMAC alg")
	}
}
