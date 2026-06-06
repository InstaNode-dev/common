package analyticsevent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// AllowedAttributes is the explicit PII allowlist: ONLY keys in this set survive
// [Sanitize]. Any attribute key not present here is dropped before the event
// leaves the process, so a new emit site cannot leak a raw email, auth token, or
// connection string by passing it under an unexpected key — the default for an
// unknown key is "drop", not "pass".
//
// AttrEmail is deliberately NOT in this set: a raw email is never emitted. When
// present it is hashed by [Sanitize] into [AttrEmailHash] (which IS allowlisted).
//
// Keep this in sync with the AttrX constants in events.go — the contract test
// asserts every non-PII Attr constant is allowlisted and the PII ones are not.
var AllowedAttributes = map[string]struct{}{
	// identity / segmentation
	AttrActor:         {},
	AttrTier:          {},
	AttrEnv:           {},
	AttrCohort:        {},
	AttrTeamID:        {},
	AttrResourceToken: {},
	AttrFingerprint:   {},
	AttrCommitID:      {},
	AttrServiceName:   {},
	// funnel
	AttrFunnelStep: {},
	// flow-test
	AttrFlow:           {},
	AttrLayer:          {},
	AttrResult:         {},
	AttrReason:         {},
	AttrLatencyMs:      {},
	AttrSyntheticRunID: {},
	AttrHTTPStatus:     {},
	// generic metadata
	AttrService:    {},
	AttrReasonCode: {},
	// the hashed form of an email (raw AttrEmail is intentionally absent)
	AttrEmailHash: {},
}

// emailHashLen is how many hex chars of the sha256 digest we keep. 16 hex chars
// (64 bits) is plenty to join a hashed email across events without being
// reversible, and matches plan F1's sha256(lower(email))[:16] spec.
const emailHashLen = 16

// Sanitize returns a NEW attribute map containing only PII-safe, allowlisted
// keys. It is the single chokepoint enforcing the package's PII policy:
//
//   - keys in [AllowedAttributes] are copied through verbatim;
//   - an [AttrEmail] key (any case-insensitive match) is HASHED via [HashEmail]
//     and re-emitted under [AttrEmailHash]; the raw value is never copied;
//   - every other key is DROPPED (default-deny).
//
// The input map is never mutated. A nil input returns nil (a no-attribute event
// is valid). Sanitize is pure and safe for concurrent use.
func Sanitize(attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		// Email gets hashed regardless of allowlist membership (it isn't on it).
		if strings.EqualFold(k, AttrEmail) {
			if s, ok := v.(string); ok {
				if h := HashEmail(s); h != "" {
					out[AttrEmailHash] = h
				}
			}
			continue
		}
		if _, ok := AllowedAttributes[k]; ok {
			out[k] = v
		}
		// else: dropped (default-deny) — no token / connection string / arbitrary
		// PII can escape under an un-allowlisted key.
	}
	return out
}

// HashEmail returns sha256(lower(trim(email)))[:emailHashLen] as lowercase hex,
// or "" for an empty/whitespace-only input. This is the ONLY representation an
// email may take in an analytics event — the raw address never leaves the
// process. Stable across events so a hashed email can be FACETed/joined in NRQL
// without ever being reversible to the original address.
func HashEmail(email string) string {
	norm := strings.ToLower(strings.TrimSpace(email))
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:emailHashLen]
}
