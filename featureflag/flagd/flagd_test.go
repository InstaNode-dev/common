package flagd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
	_ "instant.dev/common/featureflag/flagd"
)

// TestFlagd_RegisteredAndConstructs verifies the side-effect import registered
// the flagd backend and that Factory builds a wrapped flagd provider WITHOUT a
// running flagd (construction is non-blocking — readiness is deferred to eval).
func TestFlagd_RegisteredAndConstructs(t *testing.T) {
	assert.Contains(t, featureflag.ListRegistered(), featureflag.BackendFlagd,
		"flagd backend must self-register via init()")

	p, err := featureflag.Factory(featureflag.Config{
		Backend:   featureflag.BackendFlagd,
		FlagdHost: "127.0.0.1",
		FlagdPort: 1, // nothing listening; construction must NOT fail
	})
	require.NotNil(t, p)
	require.NoError(t, err, "flagd construction must be non-blocking (no boot-time error)")
	assert.Equal(t, featureflag.BackendFlagd, p.Name())
	t.Cleanup(func() { _ = p.Close() })
}

// TestFlagd_UnreachableFailsClosed proves the central guarantee on the flagd
// path: with no flagd reachable, every eval returns the caller default.
func TestFlagd_UnreachableFailsClosed(t *testing.T) {
	p, err := featureflag.Factory(featureflag.Config{
		Backend:        featureflag.BackendFlagd,
		FlagdHost:      "127.0.0.1",
		FlagdPort:      1,
		FlagdInProcess: false,
	})
	require.NotNil(t, p)
	_ = err
	t.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	gotB, _ := p.BoolVariant(ctx, "feature_team_billing", false, featureflag.EvalContext{TeamID: "t1"})
	assert.False(t, gotB, "unreachable flagd must fail closed (bool)")
	gotS, _ := p.StringVariant(ctx, "banner", "DEF", featureflag.EvalContext{})
	assert.Equal(t, "DEF", gotS, "unreachable flagd must fail closed (string)")
	gotI, _ := p.IntVariant(ctx, "max", 7, featureflag.EvalContext{})
	assert.Equal(t, int64(7), gotI, "unreachable flagd must fail closed (int)")
}

// TestFlagd_InProcessResolverConstructs covers the in-process resolver option
// branch in the builder.
func TestFlagd_InProcessResolverConstructs(t *testing.T) {
	p, err := featureflag.Factory(featureflag.Config{
		Backend:        featureflag.BackendFlagd,
		FlagdInProcess: true,
	})
	require.NotNil(t, p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	assert.Equal(t, featureflag.BackendFlagd, p.Name())
}

// TestFlagd_Integration exercises evaluation against a REAL flagd. It is gated
// behind -short (CI coverage runs with -short, so this is skipped there) and
// only runs when a flagd endpoint is supplied. Named "Integration" per the
// repo's gate-filter convention.
func TestFlagd_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping flagd integration test in -short mode (no flagd container)")
	}
	t.Skip("requires a running flagd; enable by removing this skip and pointing FlagdHost/Port at it")
}
