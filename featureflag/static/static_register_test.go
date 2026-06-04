package static_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
	_ "instant.dev/common/featureflag/static"
)

// TestStaticSubpackage_RegistersBackend verifies the side-effect import wires
// the static backend into the parent registry via init().
func TestStaticSubpackage_RegistersBackend(t *testing.T) {
	assert.Contains(t, featureflag.ListRegistered(), featureflag.BackendStatic,
		"static backend must self-register via init()")

	p, err := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			// allowlist with an empty-string entry exercises the skip branch
			"f": {Off: false, On: true, AllowTeamIDs: []string{"", "good"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	got, _ := p.BoolVariant(context.Background(), "f", false, featureflag.EvalContext{TeamID: "good"})
	assert.True(t, got, "non-empty allowlist entry must still match")
	gotEmpty, _ := p.BoolVariant(context.Background(), "f", false, featureflag.EvalContext{TeamID: ""})
	assert.False(t, gotEmpty, "empty TeamID must not match the empty allowlist entry")
}
