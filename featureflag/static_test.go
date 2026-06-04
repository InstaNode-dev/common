package featureflag_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
	_ "instant.dev/common/featureflag/static"
)

const teamBillingFlag = "feature_team_billing"

// TestStatic_AllowlistTargeting is the targeting proof: an allowlisted teamID
// resolves to the On variant; a non-listed teamID gets the Off (default)
// variant. This is the P1 integration-cohort model ("allowlist now").
func TestStatic_AllowlistTargeting(t *testing.T) {
	const integrationTeam = "team-integration-001"
	const realCustomer = "team-real-customer-999"

	p, err := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			teamBillingFlag: {
				Off:          false,
				On:           true,
				AllowTeamIDs: []string{integrationTeam},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()

	// Allowlisted team via TargetingKey -> On variant.
	gotAllow, gerr := p.BoolVariant(ctx, teamBillingFlag, false, featureflag.EvalContext{
		TargetingKey: integrationTeam,
		TeamID:       integrationTeam,
		Tier:         "team",
		Env:          "production",
	})
	require.NoError(t, gerr)
	assert.True(t, gotAllow, "allowlisted integration team must get the On variant")

	// Allowlisted via TeamID even when TargetingKey differs (keyed on user_id).
	gotByTeamID, _ := p.BoolVariant(ctx, teamBillingFlag, false, featureflag.EvalContext{
		TargetingKey: "user-abc",
		TeamID:       integrationTeam,
	})
	assert.True(t, gotByTeamID, "match on TeamID must also enable the flag")

	// Non-listed real customer -> Off (default), even on the team tier.
	gotDeny, _ := p.BoolVariant(ctx, teamBillingFlag, false, featureflag.EvalContext{
		TargetingKey: realCustomer,
		TeamID:       realCustomer,
		Tier:         "team",
		Env:          "production",
	})
	assert.False(t, gotDeny, "non-allowlisted team must get the Off/default variant")

	// Empty eval context -> Off.
	gotEmpty, _ := p.BoolVariant(ctx, teamBillingFlag, false, featureflag.EvalContext{})
	assert.False(t, gotEmpty, "empty context must get the Off variant")
}

// TestStatic_EmptyAllowlistAlwaysOff: a flag with no allowlist returns Off for
// everyone (fully fail-closed gate).
func TestStatic_EmptyAllowlistAlwaysOff(t *testing.T) {
	p, _ := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			teamBillingFlag: {Off: false, On: true}, // no AllowTeamIDs
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	got, _ := p.BoolVariant(context.Background(), teamBillingFlag, false, featureflag.EvalContext{
		TargetingKey: "anyone",
		TeamID:       "anyone",
	})
	assert.False(t, got, "empty allowlist => Off for everyone")
}

// TestStatic_StringAndIntVariants covers the typed accessors + their On/Off and
// type-mismatch (fail-closed) paths.
func TestStatic_StringAndIntVariants(t *testing.T) {
	p, _ := featureflag.Factory(featureflag.Config{
		Backend: featureflag.BackendStatic,
		StaticFlags: map[string]featureflag.StaticFlag{
			"banner_text": {Off: "default-banner", On: "vip-banner", AllowTeamIDs: []string{"vip"}},
			"max_apps":    {Off: int64(1), On: int64(50), AllowTeamIDs: []string{"vip"}},
			"max_apps_f":  {Off: float64(3), On: float64(99), AllowTeamIDs: []string{"vip"}},
			"max_apps_i":  {Off: 7, On: 8, AllowTeamIDs: []string{"vip"}},
			"wrong_type":  {Off: "not-a-bool", On: "still-not"},
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()
	vip := featureflag.EvalContext{TargetingKey: "vip", TeamID: "vip"}
	other := featureflag.EvalContext{TargetingKey: "other"}

	gotS, _ := p.StringVariant(ctx, "banner_text", "fallback", vip)
	assert.Equal(t, "vip-banner", gotS)
	gotSOff, _ := p.StringVariant(ctx, "banner_text", "fallback", other)
	assert.Equal(t, "default-banner", gotSOff)
	gotSMissing, _ := p.StringVariant(ctx, "nope", "fallback", other)
	assert.Equal(t, "fallback", gotSMissing)

	gotI, _ := p.IntVariant(ctx, "max_apps", -1, vip)
	assert.Equal(t, int64(50), gotI)
	gotIf, _ := p.IntVariant(ctx, "max_apps_f", -1, vip) // float64 path (JSON numbers)
	assert.Equal(t, int64(99), gotIf)
	gotIi, _ := p.IntVariant(ctx, "max_apps_i", -1, vip) // int path
	assert.Equal(t, int64(8), gotIi)
	gotIMissing, _ := p.IntVariant(ctx, "nope", -1, other)
	assert.Equal(t, int64(-1), gotIMissing)

	// type mismatch -> caller default (fail closed)
	gotBadBool, _ := p.BoolVariant(ctx, "wrong_type", true, other)
	assert.True(t, gotBadBool, "bool accessor on a string flag must return default")
	gotBadInt, _ := p.IntVariant(ctx, "wrong_type", 123, other)
	assert.Equal(t, int64(123), gotBadInt, "int accessor on a string flag must return default")
}

// TestStatic_FileSource loads a flagd-style JSON file and verifies the in-memory
// map wins on collision; a missing file is non-fatal (fail-closed empty set).
func TestStatic_FileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flags.json")
	const body = `{
	  "from_file":   {"off": false, "on": true, "allowTeamIDs": ["t-file"]},
	  "overridden":  {"off": false, "on": true, "allowTeamIDs": ["t-file"]}
	}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	p, err := featureflag.Factory(featureflag.Config{
		Backend:        featureflag.BackendStatic,
		StaticFilePath: path,
		StaticFlags: map[string]featureflag.StaticFlag{
			// in-memory override: a DIFFERENT allowlist wins over the file's
			"overridden": {Off: false, On: true, AllowTeamIDs: []string{"t-mem"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()

	// file-only flag honours the file's allowlist
	gotFile, _ := p.BoolVariant(ctx, "from_file", false, featureflag.EvalContext{TeamID: "t-file"})
	assert.True(t, gotFile, "file-sourced flag must target its file allowlist")

	// overridden flag honours the IN-MEMORY allowlist, not the file's
	gotMem, _ := p.BoolVariant(ctx, "overridden", false, featureflag.EvalContext{TeamID: "t-mem"})
	assert.True(t, gotMem, "in-memory map must win over file on collision")
	gotFileLoser, _ := p.BoolVariant(ctx, "overridden", false, featureflag.EvalContext{TeamID: "t-file"})
	assert.False(t, gotFileLoser, "file allowlist must be overridden by in-memory map")
}

// TestStatic_MissingAndBadFileNonFatal: a non-existent or malformed file must
// NOT fail Factory — it degrades to an empty (fail-closed) flag set.
func TestStatic_MissingAndBadFileNonFatal(t *testing.T) {
	// missing file
	p1, err := featureflag.Factory(featureflag.Config{
		Backend:        featureflag.BackendStatic,
		StaticFilePath: filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p1.Close() })
	got, _ := p1.BoolVariant(context.Background(), "anything", false, featureflag.EvalContext{})
	assert.False(t, got)

	// malformed file
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	p2, err := featureflag.Factory(featureflag.Config{
		Backend:        featureflag.BackendStatic,
		StaticFilePath: bad,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p2.Close() })
	got2, _ := p2.BoolVariant(context.Background(), "anything", true, featureflag.EvalContext{})
	assert.True(t, got2, "malformed file => empty flag set => caller default")
}

// TestAttributeMap covers the EvalContext -> attribute rendering, incl. the
// typed-field-wins-over-Attributes precedence and empty-field omission.
func TestAttributeMap(t *testing.T) {
	ec := featureflag.EvalContext{
		TeamID: "team-1",
		Tier:   "pro",
		Env:    "production",
		Attributes: map[string]any{
			"region":            "nyc3",
			featureflag.AttrTier: "SHOULD-BE-OVERRIDDEN",
		},
	}
	m := ec.AttributeMap()
	assert.Equal(t, "team-1", m[featureflag.AttrTeamID])
	assert.Equal(t, "pro", m[featureflag.AttrTier], "typed Tier must win over Attributes")
	assert.Equal(t, "production", m[featureflag.AttrEnv])
	assert.Equal(t, "nyc3", m["region"])

	// empty typed fields are omitted entirely (read as "missing" in a rule)
	empty := featureflag.EvalContext{}.AttributeMap()
	_, hasTeam := empty[featureflag.AttrTeamID]
	_, hasTier := empty[featureflag.AttrTier]
	_, hasEnv := empty[featureflag.AttrEnv]
	assert.False(t, hasTeam)
	assert.False(t, hasTier)
	assert.False(t, hasEnv)
}
