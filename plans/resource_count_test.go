package plans_test

// resource_count_test.go — coverage for the per-service resource-count cap
// accessors added in Task #55 (strict-≥80%-margin redesign, 2026-06-05).
//
// Before this, only queue_count was capped, so a tenant could create MANY
// postgres/redis/mongo resources each at the per-resource size cap and blow the
// saturated-COGS bound. These tests pin the accessor semantics (mirrors
// QueueCountLimit exactly) and assert — via a registry iteration, not a
// hand-typed list (rule 18) — that no tier's count×size×unit-COGS exceeds its
// 20%-of-price budget for the binding resources.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/plans"
)

// TestResourceCountLimit_Dispatch asserts the registry-driven dispatcher routes
// each service string to the right per-service accessor and fails open (-1) for
// an unknown service name.
func TestResourceCountLimit_Dispatch(t *testing.T) {
	r := plans.Default()

	cases := []struct {
		service string
		direct  func(string) int
	}{
		{"postgres", r.PostgresCountLimit},
		{"vector", r.VectorCountLimit},
		{"redis", r.RedisCountLimit},
		{"mongodb", r.MongoCountLimit},
		{"storage", r.StorageCountLimit},
		{"queue", r.QueueCountLimit},
	}
	for _, tier := range []string{"anonymous", "hobby", "pro", "growth", "team"} {
		for _, c := range cases {
			assert.Equal(t, c.direct(tier), r.ResourceCountLimit(tier, c.service),
				"ResourceCountLimit(%q,%q) must match the direct accessor", tier, c.service)
		}
	}

	// Unknown service → fail open.
	assert.Equal(t, -1, r.ResourceCountLimit("pro", "kafka"),
		"unknown service must fail open (-1) so a caller typo never blocks a provision")
}

// TestResourceCountLimit_UnknownTierFallsBackToAnonymous asserts an unknown
// tier resolves to the anonymous plan (Registry.Get's established fallback) for
// every count service — so a bad tier string lands on the most-restrictive cap,
// never an unbounded one.
func TestResourceCountLimit_UnknownTierFallsBackToAnonymous(t *testing.T) {
	r := plans.Default()
	for _, svc := range []string{"postgres", "vector", "redis", "mongodb", "storage", "queue"} {
		assert.Equal(t, r.ResourceCountLimit("anonymous", svc), r.ResourceCountLimit("does-not-exist", svc),
			"unknown tier must resolve to anonymous fallback for %s", svc)
	}
}

// TestResourceCountLimit_ZeroFallbackIsUnlimited asserts that a tier whose
// *_count field is absent (struct zero value 0) is treated as unlimited (-1),
// mirroring QueueCountLimit so a stale plans.yaml never blocks a customer.
func TestResourceCountLimit_ZeroFallbackIsUnlimited(t *testing.T) {
	// Build a registry from a minimal YAML that omits every *_count field.
	const minimalYAML = `
plans:
  anonymous:
    display_name: "Anonymous"
    price_monthly_cents: 0
    limits:
      provisions_per_day: 5
`
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.yaml")
	require.NoError(t, os.WriteFile(path, []byte(minimalYAML), 0o600))
	r, err := plans.Load(path)
	require.NoError(t, err)
	for _, svc := range []string{"postgres", "vector", "redis", "mongodb", "storage", "queue"} {
		assert.Equal(t, -1, r.ResourceCountLimit("anonymous", svc),
			"absent %s_count field must fall back to unlimited (-1)", svc)
	}
}

// TestResourceCountLimit_NilPlanFailsOpen exercises the defensive nil-plan
// branch in every count accessor: a zero-value Registry (empty plan map) makes
// Get return a nil *Plan, and each accessor must then fail open (-1) rather than
// panic. Mirrors QueueCountLimit's nil guard.
func TestResourceCountLimit_NilPlanFailsOpen(t *testing.T) {
	var r plans.Registry // zero value: nil plan map → Get returns nil
	for _, svc := range []string{"postgres", "vector", "redis", "mongodb", "storage", "queue"} {
		assert.Equal(t, -1, r.ResourceCountLimit("anything", svc),
			"nil plan must fail open (-1) for %s", svc)
	}
}

// TestResourceCount_PinnedTierValues pins the exact per-tier counts so a future
// edit that loosens a cap (and breaks the COGS bound) trips a visible diff.
func TestResourceCount_PinnedTierValues(t *testing.T) {
	r := plans.Default()
	type want struct{ pg, vector, redis, mongo, storage int }
	cases := map[string]want{
		"anonymous":  {1, 1, 1, 1, 1},
		"free":       {1, 1, 1, 1, 1},
		"hobby":      {2, 2, 2, 2, 2},
		"hobby_plus": {3, 3, 3, 3, 3},
		"pro":        {5, 5, 3, 5, 5},
		"growth":     {6, 6, 3, 6, 6},
		"team":       {5, 8, 4, 6, 6},
	}
	for tier, w := range cases {
		assert.Equal(t, w.pg, r.PostgresCountLimit(tier), "%s postgres_count", tier)
		assert.Equal(t, w.vector, r.VectorCountLimit(tier), "%s vector_count", tier)
		assert.Equal(t, w.redis, r.RedisCountLimit(tier), "%s redis_count", tier)
		assert.Equal(t, w.mongo, r.MongoCountLimit(tier), "%s mongodb_count", tier)
		assert.Equal(t, w.storage, r.StorageCountLimit(tier), "%s storage_count", tier)
	}
}

// TestResourceCount_WithinBudget is the rule-18 registry-iterating COGS guard:
// for every paid tier, count × per-resource-size-cap × unit-COGS must stay
// within the tier's 20%-of-price budget for the binding cost resources. This is
// the invariant that closes the strict-margin hole — re-introducing a too-large
// count (or a new tier with an unbudgeted count) fails here.
func TestResourceCount_WithinBudget(t *testing.T) {
	r := plans.Default()

	// Strict marginal COGS in $/GB-mo (see PRICING-MARGIN-MODEL doc).
	const (
		usdPGPerGB      = 0.15 // Postgres / vector / Mongo storage (DO Volume + backups)
		usdRedisPerGB   = 6.50 // RAM — the binding constraint at every tier
		usdStoragePerGB = 0.02 // DO Spaces object storage
	)

	// service → (unit $/GB, storage-limit service key for StorageLimitMB)
	type svc struct {
		name   string
		unit   float64
		count  func(string) int
		sizeMB func(tier string) int
	}
	services := []svc{
		{"postgres", usdPGPerGB, r.PostgresCountLimit, func(t string) int { return r.StorageLimitMB(t, "postgres") }},
		{"vector", usdPGPerGB, r.VectorCountLimit, func(t string) int { return r.StorageLimitMB(t, "vector") }},
		{"mongodb", usdPGPerGB, r.MongoCountLimit, func(t string) int { return r.StorageLimitMB(t, "mongodb") }},
		{"redis", usdRedisPerGB, r.RedisCountLimit, func(t string) int { return r.StorageLimitMB(t, "redis") }},
		{"storage", usdStoragePerGB, r.StorageCountLimit, func(t string) int { return r.StorageLimitMB(t, "storage") }},
	}

	// Paid tiers only — anonymous/free are $0 (CAC) and fingerprint-dedup-gated.
	prices := map[string]int{ // dollars/mo
		"hobby": 9, "hobby_plus": 19, "pro": 49, "growth": 99, "team": 199,
	}

	for tier, price := range prices {
		budget := float64(price) * 0.20
		for _, s := range services {
			count := s.count(tier)
			if count < 0 {
				t.Errorf("tier %q service %q: count is unlimited (-1) — every count cap must be finite to bound COGS", tier, s.name)
				continue
			}
			sizeGB := float64(s.sizeMB(tier)) / 1024.0
			satCOGS := float64(count) * sizeGB * s.unit
			assert.LessOrEqualf(t, satCOGS, budget,
				"tier %q service %q: count(%d)×size(%.3fGB)×$%.2f/GB = $%.2f exceeds 20%%-of-price budget $%.2f",
				tier, s.name, count, sizeGB, s.unit, satCOGS, budget)
		}
	}
}
