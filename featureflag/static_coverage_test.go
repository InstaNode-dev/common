package featureflag_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
)

// TestStatic_Matches_SkipsEmptyAllowlistEntry covers the empty-string guard in
// StaticFlag.matches: an "" entry in AllowTeamIDs must be SKIPPED, never used to
// match a context (e.g. one whose TeamID/TargetingKey is also empty). Without
// the skip, an empty allowlist entry + an empty eval context would wrongly
// resolve On — a fail-OPEN hole. This proves it stays fail-closed.
func TestStatic_Matches_SkipsEmptyAllowlistEntry(t *testing.T) {
	p, err := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			"f": {Off: false, On: true, AllowTeamIDs: []string{"", "team-real"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()

	// Empty-targeting context must NOT be matched by the "" allowlist entry.
	got, _ := p.BoolVariant(ctx, "f", false, featureflag.EvalContext{})
	assert.False(t, got, "empty allowlist entry must not match an empty eval context (fail-closed)")

	// A non-listed team: "" skipped, "team-real" no match -> Off.
	gotOther, _ := p.BoolVariant(ctx, "f", false, featureflag.EvalContext{TeamID: "other"})
	assert.False(t, gotOther)

	// Sanity: the real allowlisted id still resolves On (the "" entry is merely skipped).
	gotReal, _ := p.BoolVariant(ctx, "f", false, featureflag.EvalContext{TeamID: "team-real"})
	assert.True(t, gotReal)
}

// TestStatic_StringVariant_TypeMismatchReturnsDefault covers the type-assertion
// guard in staticProvider.StringVariant: a flag whose resolved value is not a
// string must return the caller default + ErrTypeMismatch (fail-closed), never
// a zero value or a panic.
func TestStatic_StringVariant_TypeMismatchReturnsDefault(t *testing.T) {
	p, err := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			// bool-valued flag, deliberately queried as a string below.
			"b": {Off: false, On: true, AllowTeamIDs: []string{"t"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	got, err := p.StringVariant(context.Background(), "b", "DEF", featureflag.EvalContext{TeamID: "t"})
	assert.Equal(t, "DEF", got, "string accessor on a bool flag must return the caller default")
	require.ErrorIs(t, err, featureflag.ErrTypeMismatch)
}
