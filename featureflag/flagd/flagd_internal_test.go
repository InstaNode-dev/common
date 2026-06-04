package flagd

import (
	"context"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
)

// White-box test exercising the clean-hit resolution paths of the flagd adapter
// by injecting OpenFeature's in-memory provider through the bindProvider test
// seam. This covers the `return v, nil` branches that an unreachable flagd can
// never reach in a unit test.
func TestFlagdAdapter_CleanHitResolves(t *testing.T) {
	mem := memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{
		"bool_flag": {
			State:          memprovider.Enabled,
			DefaultVariant: "on",
			Variants:       map[string]any{"on": true, "off": false},
		},
		"string_flag": {
			State:          memprovider.Enabled,
			DefaultVariant: "v",
			Variants:       map[string]any{"v": "resolved"},
		},
		"int_flag": {
			State:          memprovider.Enabled,
			DefaultVariant: "n",
			Variants:       map[string]any{"n": 77}, // memprovider IntEvaluation expects an int variant
		},
	})

	closed := false
	p, err := bindProvider(mem, func() { closed = true })
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, featureflag.BackendFlagd, p.Name())

	ctx := context.Background()

	gotB, errB := p.BoolVariant(ctx, "bool_flag", false, featureflag.EvalContext{TargetingKey: "t"})
	require.NoError(t, errB)
	assert.True(t, gotB, "clean hit must return the resolved bool value, not the default")

	gotS, errS := p.StringVariant(ctx, "string_flag", "DEF", featureflag.EvalContext{})
	require.NoError(t, errS)
	assert.Equal(t, "resolved", gotS)

	gotI, errI := p.IntVariant(ctx, "int_flag", -1, featureflag.EvalContext{})
	require.NoError(t, errI)
	assert.Equal(t, int64(77), gotI)

	// missing flag still falls back to default (and surfaces an error)
	gotMiss, errMiss := p.BoolVariant(ctx, "absent", false, featureflag.EvalContext{})
	assert.False(t, gotMiss)
	assert.Error(t, errMiss)

	// Close delegates to closeFn.
	require.NoError(t, p.Close())
	assert.True(t, closed, "Close must invoke the injected closeFn")
}

// TestBindProvider_NilCloseFn covers the Close branch when closeFn is nil.
func TestBindProvider_NilCloseFn(t *testing.T) {
	mem := memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{})
	p, err := bindProvider(mem, nil)
	require.NoError(t, err)
	assert.NoError(t, p.Close(), "Close with nil closeFn must be a safe no-op")
}

// TestNextDomain_Unique verifies each call yields a distinct domain so two
// builders never clash on the OpenFeature singleton.
func TestNextDomain_Unique(t *testing.T) {
	a := nextDomain()
	b := nextDomain()
	assert.NotEqual(t, a, b)
}

// TestEvalContext_RendersTargetingKey covers the evalContext renderer.
func TestEvalContext_RendersTargetingKey(t *testing.T) {
	ec := evalContext(featureflag.EvalContext{TargetingKey: "team-1", Tier: "pro"})
	assert.Equal(t, "team-1", ec.TargetingKey())
	_ = openfeature.EvaluationContext{} // ensure import used in white-box pkg
}
