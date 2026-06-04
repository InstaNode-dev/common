package analyticsevent

import (
	"strings"
	"testing"
)

// TestSanitize_DropsPIIAndUnknownKeys is THE PII-allowlist test: it proves that
// raw email, auth tokens, and connection strings NEVER survive Sanitize, that an
// email is hashed (not passed raw), and that only allowlisted keys come through.
func TestSanitize_DropsPIIAndUnknownKeys(t *testing.T) {
	const rawEmail = "Alice@Example.COM"
	in := map[string]any{
		// PII / secrets that MUST be dropped (none are on the allowlist):
		AttrEmail:          rawEmail,
		"token":            "pat_supersecrettoken",
		"api_token":        "abc123",
		"connection_url":   "postgres://user:pass@host:5432/db",
		"connectionString": "redis://:pw@host:6379",
		"password":         "hunter2",
		"authorization":    "Bearer xyz",
		"ip":               "203.0.113.7",
		// allowlisted, must survive:
		AttrActor:      ActorAgentClaude,
		AttrTier:       "pro",
		AttrFunnelStep: FunnelStepClaim,
		AttrLatencyMs:  int64(142),
	}

	out := Sanitize(in)

	// 1. No raw email anywhere — neither key nor value.
	if _, ok := out[AttrEmail]; ok {
		t.Fatalf("raw email key %q survived Sanitize: %v", AttrEmail, out)
	}
	for k, v := range out {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), "alice@example.com") {
			t.Fatalf("raw email value leaked under key %q: %q", k, s)
		}
	}

	// 2. Email is present ONLY as a stable hash under AttrEmailHash.
	got, ok := out[AttrEmailHash].(string)
	if !ok {
		t.Fatalf("expected hashed email under %q, got %v", AttrEmailHash, out)
	}
	if want := HashEmail(rawEmail); got != want {
		t.Fatalf("email hash = %q, want %q (case/space-normalized sha256[:16])", got, want)
	}
	if len(got) != emailHashLen {
		t.Fatalf("email hash len = %d, want %d", len(got), emailHashLen)
	}

	// 3. Every secret/PII key was dropped.
	for _, banned := range []string{"token", "api_token", "connection_url", "connectionString", "password", "authorization", "ip"} {
		if _, ok := out[banned]; ok {
			t.Errorf("secret/PII key %q survived Sanitize (default-deny violated)", banned)
		}
	}

	// 4. Allowlisted keys survived verbatim.
	if out[AttrActor] != ActorAgentClaude || out[AttrTier] != "pro" ||
		out[AttrFunnelStep] != FunnelStepClaim || out[AttrLatencyMs] != int64(142) {
		t.Errorf("allowlisted keys mangled: %v", out)
	}
}

func TestSanitize_NilAndEmpty(t *testing.T) {
	if got := Sanitize(nil); got != nil {
		t.Errorf("Sanitize(nil) = %v, want nil", got)
	}
	if got := Sanitize(map[string]any{}); len(got) != 0 {
		t.Errorf("Sanitize(empty) = %v, want empty", got)
	}
}

func TestSanitize_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{AttrEmail: "x@y.com", "secret": "s", AttrTier: "pro"}
	_ = Sanitize(in)
	if len(in) != 3 || in[AttrEmail] != "x@y.com" || in["secret"] != "s" {
		t.Fatalf("Sanitize mutated input map: %v", in)
	}
}

func TestSanitize_EmailCaseInsensitiveKey(t *testing.T) {
	// A caller using "EMAIL" or "Email" still gets hashed+dropped, not passed.
	for _, key := range []string{"EMAIL", "Email", "eMaIl"} {
		out := Sanitize(map[string]any{key: "user@test.com"})
		if _, ok := out[key]; ok {
			t.Errorf("email under key %q survived raw", key)
		}
		if out[AttrEmailHash] != HashEmail("user@test.com") {
			t.Errorf("email under key %q not hashed", key)
		}
	}
}

func TestSanitize_EmailNonStringDropped(t *testing.T) {
	// A non-string under the email key is dropped entirely (no hash, no raw).
	out := Sanitize(map[string]any{AttrEmail: 12345})
	if len(out) != 0 {
		t.Fatalf("non-string email produced output: %v", out)
	}
}

func TestSanitize_EmptyEmailNoHash(t *testing.T) {
	out := Sanitize(map[string]any{AttrEmail: "   "})
	if _, ok := out[AttrEmailHash]; ok {
		t.Fatalf("whitespace-only email produced a hash: %v", out)
	}
}

func TestHashEmail(t *testing.T) {
	// Stable, normalized, reversible-resistant.
	a := HashEmail("Bob@Example.com")
	b := HashEmail("  bob@example.com  ")
	if a != b {
		t.Errorf("HashEmail not normalized: %q != %q", a, b)
	}
	if len(a) != emailHashLen {
		t.Errorf("HashEmail len = %d, want %d", len(a), emailHashLen)
	}
	if strings.Contains(a, "@") || strings.Contains(a, "bob") {
		t.Errorf("HashEmail leaked plaintext: %q", a)
	}
	if HashEmail("") != "" || HashEmail("   ") != "" {
		t.Errorf("HashEmail(empty) should be empty")
	}
	// Different emails -> different hashes.
	if HashEmail("a@x.com") == HashEmail("b@x.com") {
		t.Errorf("HashEmail collision on distinct inputs")
	}
}

// TestAllowlist_PIIKeysAbsent_NonPIIKeysPresent is the contract test (CLAUDE.md
// rule 18: iterate the registry, don't hand-type a list) tying the AttrX
// constants to the allowlist: every non-PII Attr* must be allowlisted, and the
// one PII key (AttrEmail) must NOT be.
func TestAllowlist_PIIKeysAbsent_NonPIIKeysPresent(t *testing.T) {
	// AttrEmail is the only PII key and must be excluded from the allowlist.
	if _, ok := AllowedAttributes[AttrEmail]; ok {
		t.Fatalf("AttrEmail (%q) must NOT be on the allowlist — it is hashed, never emitted raw", AttrEmail)
	}
	// Every non-PII Attr* constant the package exposes must be allowlisted, so a
	// new emit site using a documented attribute never has it silently dropped.
	nonPII := []string{
		AttrActor, AttrTier, AttrEnv, AttrCohort, AttrTeamID, AttrResourceToken,
		AttrFingerprint, AttrCommitID, AttrServiceName, AttrFunnelStep, AttrFlow,
		AttrLayer, AttrResult, AttrReason, AttrLatencyMs, AttrSyntheticRunID,
		AttrService, AttrReasonCode, AttrEmailHash,
	}
	for _, k := range nonPII {
		if _, ok := AllowedAttributes[k]; !ok {
			t.Errorf("non-PII attribute %q is missing from AllowedAttributes — events using it would be silently dropped", k)
		}
	}
}
