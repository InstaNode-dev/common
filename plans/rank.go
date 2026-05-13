// rank.go — totally-ordered rank of plan tiers, shared across api/, worker/,
// and any future module that needs to classify a tier transition as an
// upgrade vs a downgrade.
//
// Two package-private rank functions used to live in the api repo
// (internal/handlers/billing.go::tierRank and
// internal/handlers/admin_customers.go::adminTierRank). They had subtly
// different orderings — billing.go covered 6 tiers (anonymous .. team),
// admin_customers.go covered 4 (free .. team) and was off-by-one against
// billing for the same names. The discrepancy never bit production because
// the admin surface never sees anonymous/growth, but it's a footgun waiting
// to happen the moment the admin surface is widened.
//
// This file promotes a single canonical ordering. Callers compare ranks
// (a.rank < b.rank ⇒ a is "lower tier") and MUST guard against the -1
// sentinel returned for unknown tiers.

package plans

import "strings"

// Rank returns a totally-ordered integer rank for the given plan tier name.
// Higher rank = more capacity. The canonical ordering is:
//
//	anonymous = 0
//	free      = 1
//	hobby     = 2
//	growth    = 3
//	pro       = 4
//	team      = 5
//
// Unknown tiers return -1. Callers that compare ranks to classify a
// transition (upgrade vs downgrade vs renewal) MUST treat -1 as the
// "no transition direction" verdict — i.e. emit no audit row rather than
// guess which way an unknown tier sits relative to a known one.
//
// The function is intentionally case- and whitespace-insensitive so callers
// don't need to pre-normalise. The "*_yearly" billing variants are NOT
// special-cased here — pass them through CanonicalTier first if you want
// "pro_yearly" to rank the same as "pro" (billing.go does exactly this).
func Rank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "anonymous":
		return 0
	case "free":
		return 1
	case "hobby":
		return 2
	case "growth":
		return 3
	case "pro":
		return 4
	case "team":
		return 5
	}
	return -1
}
