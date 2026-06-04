package featureflag_test

// contract_test.go — registry-iterating contract test for the feature-flag
// abstraction (CLAUDE.md rule 18).
//
// Every backend registers itself with the global registry at package-init via
// featureflag.Register(name, builder). This test iterates the LIVE registry
// rather than a hand-typed slice, so a third backend added later is
// automatically held to the same fail-closed contract.
//
// THE central invariant (the inverse of the repo's "fail open on Redis" rule):
// every backend MUST return the caller-supplied DEFAULT on every failure mode —
//   - the flag is missing
//   - the underlying provider errors
//   - the eval context is nil/empty (no targeting info)
//   - the context is cancelled / nil
// Failing closed means an unbuilt feature is OFF for everyone unless explicitly
// targeted on. This file is the load-bearing proof of that guarantee.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"

	// side-effect imports register each backend with the registry.
	// NOTE: the flagd backend is deferred to a follow-up PR (it ships with the
	// flagd deployment and pulls the OpenFeature+flagd dep tree, which carries
	// GO-2026-4279 until flagd/core >= v0.13.1). Config "flagd" currently
	// degrades to the static backend (fail-closed) via the factory. Until then
	// the registry holds only `static`, and the contract below still iterates it.
	_ "instant.dev/common/featureflag/static"
)

// configForBackend returns the minimum Config to construct each backend such
// that it has NO usable flag source — so every evaluation MUST fall back to the
// caller default (static: supply no flags). Must fail closed.
func configForBackend(name string) featureflag.Config {
	return featureflag.Config{Backend: name}
}

// TestRegistry_AllBackendsFailClosed is the rule-18 contract test. It iterates
// every registered backend and asserts the fail-closed guarantee across all the
// failure surfaces, for all three typed accessors.
func TestRegistry_AllBackendsFailClosed(t *testing.T) {
	registered := featureflag.ListRegistered()
	require.GreaterOrEqual(t, len(registered), 1,
		"expected at least 1 backend registered (static; flagd deferred to a follow-up PR); got %v", registered)

	for _, name := range registered {
		name := name
		t.Run(name, func(t *testing.T) {
			p, err := featureflag.Factory(configForBackend(name))
			// Factory NEVER hard-errors (it degrades to static); err is advisory.
			require.NotNil(t, p, "Factory(%q) returned nil provider", name)
			t.Cleanup(func() { _ = p.Close() })
			_ = err

			ctx := context.Background()

			// --- missing flag: every accessor returns its caller default ---
			t.Run("missing_flag", func(t *testing.T) {
				gotBool, _ := p.BoolVariant(ctx, "no_such_flag", true, featureflag.EvalContext{})
				assert.True(t, gotBool, "%s: missing bool flag must return caller default (true)", name)
				gotBool2, _ := p.BoolVariant(ctx, "no_such_flag", false, featureflag.EvalContext{})
				assert.False(t, gotBool2, "%s: missing bool flag must return caller default (false)", name)

				gotStr, _ := p.StringVariant(ctx, "no_such_flag", "DEFAULT", featureflag.EvalContext{})
				assert.Equal(t, "DEFAULT", gotStr, "%s: missing string flag must return caller default", name)

				gotInt, _ := p.IntVariant(ctx, "no_such_flag", 42, featureflag.EvalContext{})
				assert.Equal(t, int64(42), gotInt, "%s: missing int flag must return caller default", name)
			})

			// --- nil / empty eval context: still returns default ---
			t.Run("empty_eval_context", func(t *testing.T) {
				got, _ := p.BoolVariant(ctx, "feature_team_billing", false, featureflag.EvalContext{})
				assert.False(t, got, "%s: empty eval context must not enable a gated feature", name)

				gotNilAttrs, _ := p.BoolVariant(ctx, "feature_team_billing", false, featureflag.EvalContext{
					Attributes: nil,
				})
				assert.False(t, gotNilAttrs, "%s: nil attributes must not enable a gated feature", name)
			})

			// --- nil context: must fail closed, never panic ---
			t.Run("nil_context", func(t *testing.T) {
				//nolint:staticcheck // SA1012: deliberately passing nil ctx to prove fail-closed
				got, err := p.BoolVariant(nil, "anything", false, featureflag.EvalContext{})
				assert.False(t, got, "%s: nil context must return caller default", name)
				assert.Error(t, err, "%s: nil context should surface an advisory error", name)
			})

			// --- cancelled context: must fail closed ---
			t.Run("cancelled_context", func(t *testing.T) {
				cctx, cancel := context.WithCancel(context.Background())
				cancel()
				got, err := p.BoolVariant(cctx, "anything", false, featureflag.EvalContext{})
				assert.False(t, got, "%s: cancelled context must return caller default", name)
				assert.Error(t, err, "%s: cancelled context should surface an advisory error", name)
			})

			// --- Name + Close contract ---
			t.Run("name_and_close", func(t *testing.T) {
				assert.NotEmpty(t, p.Name(), "%s: Name() must be non-empty", name)
				assert.NoError(t, p.Close(), "%s: Close() must be a clean no-op", name)
				assert.NoError(t, p.Close(), "%s: Close() must be idempotent", name)
			})
		})
	}
}

// TestFactory_UnknownBackendDegradesToStatic verifies the DELIBERATE difference
// from storageprovider/queueprovider: an unknown backend does NOT hard-fail the
// service. It degrades to the static (fail-closed) backend and returns an
// advisory error — because a flag system that refuses to boot is strictly worse
// than one serving OFF defaults.
func TestFactory_UnknownBackendDegradesToStatic(t *testing.T) {
	p, err := featureflag.Factory(featureflag.Config{Backend: "made-up-backend"})
	require.NotNil(t, p, "unknown backend must still yield a usable provider")
	assert.ErrorIs(t, err, featureflag.ErrUnknownBackend, "should surface advisory ErrUnknownBackend")
	// The degraded provider must serve fail-closed defaults.
	got, _ := p.BoolVariant(context.Background(), "anything", false, featureflag.EvalContext{})
	assert.False(t, got, "degraded provider must fail closed")
	_ = p.Close()
}

// TestNormalizeBackend covers the alias table — the SUT is the table itself, so
// this is hand-typed (rule 18's carve-out: the table IS the registry here).
func TestNormalizeBackend(t *testing.T) {
	cases := map[string]string{
		"":            featureflag.BackendStatic, // empty defaults to static
		"static":      featureflag.BackendStatic,
		"STATIC":      featureflag.BackendStatic,
		"memory":      featureflag.BackendStatic,
		"in-memory":   featureflag.BackendStatic,
		"inmem":       featureflag.BackendStatic,
		"file":        featureflag.BackendStatic,
		"flagd":       featureflag.BackendFlagd,
		"openfeature": featureflag.BackendFlagd,
		"open-feature": featureflag.BackendFlagd,
		"grpc":        featureflag.BackendFlagd,
		"nonsense":    "",
	}
	for in, want := range cases {
		assert.Equal(t, want, featureflag.NormalizeBackend(in), "NormalizeBackend(%q)", in)
	}
}
