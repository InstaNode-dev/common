package crypto

// Internal tests that exercise hard-to-reach branches via package-private
// affordances (swappable random sources). Kept in `package crypto` rather
// than `crypto_test` so the rand-reader vars can be mocked without exporting
// them to the public API.

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// errReader always fails — used to drive the io.ReadFull error paths in
// Encrypt and GenerateAPIKey.
type errReader struct{ err error }

func (r errReader) Read(_ []byte) (int, error) { return 0, r.err }

func TestEncrypt_NonceReadFails(t *testing.T) {
	// Swap the package-level rand source for one that always errors.
	orig := randReader
	defer func() { randReader = orig }()
	sentinel := errors.New("rand-source dead")
	randReader = errReader{err: sentinel}

	key, err := ParseAESKey("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("ParseAESKey: %v", err)
	}

	_, err = Encrypt(key, "plaintext")
	if err == nil {
		t.Fatal("expected error when nonce read fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel error, got %v", err)
	}
	var ee *ErrEncrypt
	if !errors.As(err, &ee) {
		t.Errorf("expected *ErrEncrypt, got %T", err)
	}
}

func TestGenerateAPIKey_RandReadFails(t *testing.T) {
	orig := tokenRandReader
	defer func() { tokenRandReader = orig }()
	sentinel := errors.New("rng failure")
	tokenRandReader = errReader{err: sentinel}

	_, err := GenerateAPIKey()
	if err == nil {
		t.Fatal("expected error when rand.Read fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel error, got %v", err)
	}
	var te *ErrTokenGenerate
	if !errors.As(err, &te) {
		t.Errorf("expected *ErrTokenGenerate, got %T", err)
	}
	// Error message should mention the underlying cause.
	if !strings.Contains(err.Error(), "rng failure") {
		t.Errorf("expected error to mention cause, got %q", err.Error())
	}
}

// Sanity: the default randReader/tokenRandReader are non-nil. Documents
// invariant relied on by the production path.
func TestDefaultRandReaders_NonNil(t *testing.T) {
	if randReader == nil {
		t.Error("randReader is nil")
	}
	if tokenRandReader == nil {
		t.Error("tokenRandReader is nil")
	}
}

// shortReader returns fewer bytes than requested, then io.EOF — exercises the
// io.ReadFull short-read path (distinct from outright error).
type shortReader struct {
	calls int
}

func (r *shortReader) Read(p []byte) (int, error) {
	r.calls++
	if len(p) == 0 {
		return 0, nil
	}
	// Fill 1 byte then signal EOF — io.ReadFull turns this into
	// io.ErrUnexpectedEOF.
	p[0] = 0xaa
	return 1, io.EOF
}

func TestGenerateAPIKey_ShortRead(t *testing.T) {
	orig := tokenRandReader
	defer func() { tokenRandReader = orig }()
	tokenRandReader = &shortReader{}

	_, err := GenerateAPIKey()
	if err == nil {
		t.Fatal("expected error from short rand read")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("expected ErrUnexpectedEOF, got %v", err)
	}
}

func TestEncrypt_ShortNonceRead(t *testing.T) {
	orig := randReader
	defer func() { randReader = orig }()
	randReader = &shortReader{}

	key, _ := ParseAESKey("0000000000000000000000000000000000000000000000000000000000000000")
	_, err := Encrypt(key, "x")
	if err == nil {
		t.Fatal("expected error from short nonce read")
	}
}

// TestVerifyOnboardingJWT_WrongAlg exercises the keyfunc alg-confusion guard
// in VerifyOnboardingJWT (mirrors TestVerifyJWT_WrongAlg in the external test
// file). A token claiming alg=RS256 must be rejected because the keyfunc only
// returns the HMAC key.
func TestVerifyOnboardingJWT_WrongAlg(t *testing.T) {
	bad := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJmcCI6ImEifQ.sig"
	_, err := VerifyOnboardingJWT([]byte("secret"), bad)
	if err == nil {
		t.Fatal("expected error for non-HMAC alg")
	}
	var ve *ErrJWTVerify
	if !errors.As(err, &ve) {
		t.Errorf("expected *ErrJWTVerify, got %T", err)
	}
}

// failingSigningMethod is a jwt.SigningMethod whose Sign always errors —
// used to exercise the SignedString error path in SignJWT and
// SignOnboardingJWT.
type failingSigningMethod struct{ err error }

func (m *failingSigningMethod) Alg() string { return "FAIL" }
func (m *failingSigningMethod) Sign(_ string, _ interface{}) (string, error) {
	return "", m.err
}
func (m *failingSigningMethod) Verify(_, _ string, _ interface{}) error { return m.err }

func TestSignJWT_SignedStringFails(t *testing.T) {
	orig := jwtSigningMethod
	defer func() { jwtSigningMethod = orig }()
	sentinel := errors.New("signing dead")
	jwtSigningMethod = &failingSigningMethod{err: sentinel}

	_, err := SignJWT([]byte("secret"), InstantClaims{Fingerprint: "x"})
	if err == nil {
		t.Fatal("expected error when signing fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got %v", err)
	}
	var se *ErrJWTSign
	if !errors.As(err, &se) {
		t.Errorf("expected *ErrJWTSign, got %T", err)
	}
}

// TestVerifyJWT_FutureIssuedAt_OurCheck drives the second-line-of-defense iat
// check inside VerifyJWT (lines after the library's err path). jwt/v4's
// RegisteredClaims.Valid uses jwt.TimeFunc — by setting TimeFunc to a moment
// in the future, the library's parse passes; our own time.Now().UTC()
// comparison then catches the future-iat and returns ValidationErrorIssuedAt.
// Guards against jwt/v4 upstream silently dropping the iat check.
func TestVerifyJWT_FutureIssuedAt_OurCheck(t *testing.T) {
	origTimeFunc := jwt.TimeFunc
	defer func() { jwt.TimeFunc = origTimeFunc }()
	// Pretend "now" inside the library is 1 day from now — so future-iat
	// tokens validate at the library layer but our code still flags them.
	jwt.TimeFunc = func() time.Time { return time.Now().UTC().Add(24 * time.Hour) }

	claims := InstantClaims{Fingerprint: "fp"}
	claims.IssuedAt = jwt.NewNumericDate(time.Now().UTC().Add(30 * time.Minute))
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().UTC().Add(48 * time.Hour))
	signed, err := SignJWT([]byte("sec"), claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	_, err = VerifyJWT([]byte("sec"), signed)
	if err == nil {
		t.Fatal("expected our iat-future check to flag the token")
	}
	var ve *jwt.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *jwt.ValidationError, got %T", err)
	} else if ve.Errors&jwt.ValidationErrorIssuedAt == 0 {
		t.Errorf("expected ValidationErrorIssuedAt flag, got %d", ve.Errors)
	}
}

// TestVerifyOnboardingJWT_FutureIssuedAt_OurCheck — sibling of the InstantClaims
// test above. SignOnboardingJWT stamps iat from real time.Now(), so we must
// hand-craft a token with a future iat and verify it under a library TimeFunc
// that lets the iat-check pass at the library layer.
func TestVerifyOnboardingJWT_FutureIssuedAt_OurCheck(t *testing.T) {
	origTimeFunc := jwt.TimeFunc
	defer func() { jwt.TimeFunc = origTimeFunc }()
	jwt.TimeFunc = func() time.Time { return time.Now().UTC().Add(24 * time.Hour) }

	claims := OnboardingClaims{Fingerprint: "fp"}
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ID:        "test-jti",
		IssuedAt:  jwt.NewNumericDate(time.Now().UTC().Add(30 * time.Minute)),
		ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(72 * time.Hour)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte("sec"))
	if err != nil {
		t.Fatalf("manual sign: %v", err)
	}
	_, err = VerifyOnboardingJWT([]byte("sec"), signed)
	if err == nil {
		t.Fatal("expected our iat-future check to flag the token")
	}
	var ve *jwt.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *jwt.ValidationError, got %T", err)
	} else if ve.Errors&jwt.ValidationErrorIssuedAt == 0 {
		t.Errorf("expected ValidationErrorIssuedAt flag, got %d", ve.Errors)
	}
}

func TestSignOnboardingJWT_SignedStringFails(t *testing.T) {
	orig := jwtSigningMethod
	defer func() { jwtSigningMethod = orig }()
	sentinel := errors.New("onboarding signing dead")
	jwtSigningMethod = &failingSigningMethod{err: sentinel}

	_, _, err := SignOnboardingJWT([]byte("secret"), OnboardingClaims{Fingerprint: "x"})
	if err == nil {
		t.Fatal("expected error when signing fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got %v", err)
	}
	var se *ErrJWTSign
	if !errors.As(err, &se) {
		t.Errorf("expected *ErrJWTSign, got %T", err)
	}
}
