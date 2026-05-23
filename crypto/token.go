package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

const tokenPrefix = "inst_live_"

// tokenRandReader is the source of randomness for API-key generation.
// Overridable in tests to exercise the rand.Read error path; production code
// always uses crypto/rand.Reader.
var tokenRandReader io.Reader = rand.Reader

// ErrTokenGenerate is returned when secure random bytes cannot be read.
type ErrTokenGenerate struct {
	Cause error
}

func (e *ErrTokenGenerate) Error() string {
	return fmt.Sprintf("token generation failed: %v", e.Cause)
}

func (e *ErrTokenGenerate) Unwrap() error { return e.Cause }

// GenerateAPIKey produces a secure API key of the form inst_live_<base64url(32 random bytes)>.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(tokenRandReader, b); err != nil {
		return "", &ErrTokenGenerate{Cause: err}
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}
