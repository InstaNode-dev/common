package plans_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"instant.dev/common/plans"
)

// TestRank_AllStandardTiers asserts the canonical ordering documented in
// rank.go. Lock-in test — changing any of these values is an API break
// because callers compare ranks across modules (api/, worker/).
//
// W11 (2026-05-13): hobby_plus inserted between hobby and pro at rank 3.
//
// growth/pro fix (P1, BUGHUNT-REPORT-2026-05-17-round2.md): growth was
// incorrectly ranked BELOW pro (growth=4, pro=5), contradicting plans.yaml
// pricing ($99 growth > $49 pro) and the worker's billingTierRankMap. The
// canonical order is now anchored to price: pro=4, growth=5.
func TestRank_AllStandardTiers(t *testing.T) {
	cases := map[string]int{
		"anonymous":  0,
		"free":       1,
		"hobby":      2,
		"hobby_plus": 3,
		"pro":        4,
		"growth":     5,
		"team":       6,
	}
	for tier, want := range cases {
		t.Run(tier, func(t *testing.T) {
			assert.Equal(t, want, plans.Rank(tier),
				"Rank(%q) — canonical ordering must remain stable", tier)
		})
	}
}

// TestRank_ProRanksBelowGrowth is the dedicated pinning test for the P1
// growth/pro inversion fix. pro ($49/mo) MUST rank strictly below growth
// ($99/mo); if this fails the inversion has regressed and plan-transition
// classification (upgrade vs downgrade) will be wrong.
func TestRank_ProRanksBelowGrowth(t *testing.T) {
	pro := plans.Rank("pro")
	growth := plans.Rank("growth")
	assert.Less(t, pro, growth,
		"Rank(pro)=%d must be strictly less than Rank(growth)=%d — pro $49/mo < growth $99/mo",
		pro, growth)
	// And growth must still rank strictly below team.
	assert.Less(t, growth, plans.Rank("team"),
		"Rank(growth)=%d must be strictly less than Rank(team)=%d", growth, plans.Rank("team"))
}

// TestRank_UnknownReturnsMinusOne covers the sentinel contract: any tier
// name not in the canonical list returns -1 so callers can short-circuit
// rather than guess a direction.
func TestRank_UnknownReturnsMinusOne(t *testing.T) {
	cases := []string{
		"",
		"enterprise",
		"premium",
		"basic",
		"unknown",
		"pro_yearly", // Yearly variants must be normalised via CanonicalTier first.
		"hobby_yearly",
		"   ", // Empty-after-trim stays unknown.
		"pro-yearly",
		"freetier",
	}
	for _, tier := range cases {
		t.Run(tier, func(t *testing.T) {
			if tier == "pro_yearly" || tier == "hobby_yearly" {
				// Yearly variants intentionally return -1 — callers MUST
				// pass them through CanonicalTier first. This row asserts
				// the "don't auto-normalise" contract.
				assert.Equal(t, -1, plans.Rank(tier),
					"Rank(%q) — yearly variants must NOT auto-normalise (callers use CanonicalTier)", tier)
				return
			}
			assert.Equal(t, -1, plans.Rank(tier),
				"Rank(%q) — unknown tier must return -1 sentinel", tier)
		})
	}
}

// TestRank_MonotonicallyIncreasing asserts that the canonical chain
// anonymous < free < hobby < hobby_plus < pro < growth < team is strictly
// increasing. This is the property callers actually depend on
// (a.rank < b.rank ⇒ a is the lower tier); the absolute values in
// TestRank_AllStandardTiers could in principle be remapped, but the
// relative ordering can't.
func TestRank_MonotonicallyIncreasing(t *testing.T) {
	chain := []string{"anonymous", "free", "hobby", "hobby_plus", "pro", "growth", "team"}
	for i := 1; i < len(chain); i++ {
		prev := plans.Rank(chain[i-1])
		curr := plans.Rank(chain[i])
		assert.Less(t, prev, curr,
			"Rank(%q)=%d must be strictly less than Rank(%q)=%d",
			chain[i-1], prev, chain[i], curr)
	}
}

// TestRank_CaseInsensitive covers the documented case-insensitive
// behaviour — callers shouldn't need to normalise before calling.
func TestRank_CaseInsensitive(t *testing.T) {
	assert.Equal(t, 4, plans.Rank("PRO"))
	assert.Equal(t, 4, plans.Rank("Pro"))
	assert.Equal(t, 4, plans.Rank("pRo"))
	assert.Equal(t, 2, plans.Rank("  hobby  "))
	// hobby_plus (W11): inserted between hobby and pro at rank 3.
	assert.Equal(t, 3, plans.Rank("HOBBY_PLUS"))
	assert.Equal(t, 3, plans.Rank("  hobby_plus  "))
}
