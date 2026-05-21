package plans_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/plans"
)

func TestDefault_LoadsWithoutError(t *testing.T) {
	r := plans.Default()
	require.NotNil(t, r)
}

func TestDefault_AllStandardTiersPresent(t *testing.T) {
	r := plans.Default()
	for _, tier := range []string{"anonymous", "free", "hobby", "pro", "team", "growth"} {
		p := r.Get(tier)
		assert.Equal(t, tier, p.Name, "tier %q must be in default registry", tier)
	}
}

func TestGet_UnknownTier_FallsBackToAnonymous(t *testing.T) {
	r := plans.Default()
	p := r.Get("enterprise-ultra")
	assert.Equal(t, "anonymous", p.Name, "unknown tier must fall back to anonymous plan")
}

func TestProvisionLimit_AnonymousIs5(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 5, r.ProvisionLimit("anonymous"))
}

func TestProvisionLimit_PaidTiersUnlimited(t *testing.T) {
	r := plans.Default()
	for _, tier := range []string{"hobby", "pro", "team", "growth"} {
		assert.Equal(t, -1, r.ProvisionLimit(tier),
			"ProvisionLimit(%q) must be -1 (unlimited)", tier)
	}
}

func TestLoad_ValidFile_ReturnsRegistry(t *testing.T) {
	yaml := `
plans:
  anonymous:
    display_name: "Anon"
    price_monthly_cents: 0
    trial_days: 0
    limits:
      provisions_per_day: 3
      postgres_storage_mb: 10
      redis_memory_mb: 5
    features:
      alerts: false
      custom_domains: false
      sla: false
promotions: []
`
	path := writeTempYAML(t, yaml)
	r, err := plans.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 3, r.ProvisionLimit("anonymous"))
}

func TestLoad_MissingFile_ReturnsError(t *testing.T) {
	_, err := plans.Load("/nonexistent/plans.yaml")
	assert.Error(t, err)
}

func TestLoad_MissingAnonymousPlan_ReturnsError(t *testing.T) {
	yaml := `
plans:
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 5120
      redis_memory_mb: 256
    features:
      alerts: true
      custom_domains: false
      sla: false
promotions: []
`
	path := writeTempYAML(t, yaml)
	_, err := plans.Load(path)
	assert.ErrorContains(t, err, "anonymous", "missing anonymous plan must return descriptive error")
}

func TestLoad_InvalidYAML_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "plans: [this is: not: valid: yaml")
	_, err := plans.Load(path)
	assert.Error(t, err)
}

func TestAll_ReturnsAllPlans(t *testing.T) {
	r := plans.Default()
	all := r.All()
	// 7 base tiers + 4 yearly variants (hobby_yearly, hobby_plus_yearly,
	// pro_yearly, team_yearly) = 11. W11 added hobby_plus (+yearly).
	assert.Len(t, all, 11, "default registry must have 11 plans (7 base + 4 yearly variants)")
	for _, name := range []string{
		"anonymous", "free", "hobby", "hobby_plus", "pro", "team", "growth",
		"hobby_yearly", "hobby_plus_yearly", "pro_yearly", "team_yearly",
	} {
		assert.Contains(t, all, name)
	}
}

// TestYearlyVariants_MirrorMonthlyLimits guards the invariant that each
// {tier}_yearly plan has the same limits + features as its monthly
// counterpart — only `price_monthly_cents` and `billing_period` may
// differ. Drifting these is silently wrong: a yearly Pro subscriber
// would get different headroom than a monthly Pro subscriber.
func TestYearlyVariants_MirrorMonthlyLimits(t *testing.T) {
	r := plans.Default()
	for _, base := range []string{"hobby", "hobby_plus", "pro", "team"} {
		yearly := r.Get(base + "_yearly")
		monthly := r.Get(base)
		assert.Equal(t, monthly.Limits, yearly.Limits,
			"%s_yearly limits must mirror %s exactly", base, base)
		assert.Equal(t, monthly.Features, yearly.Features,
			"%s_yearly features must mirror %s exactly", base, base)
		assert.Equal(t, "yearly", yearly.BillingPeriod,
			"%s_yearly must declare billing_period: yearly", base)
	}
}

// TestBillingPeriod_MonthlyDefault verifies that base tiers report
// "monthly" (the YAML omits billing_period for them) and yearly tiers
// report "yearly".
func TestBillingPeriod_MonthlyDefault(t *testing.T) {
	r := plans.Default()
	for _, t1 := range []string{"hobby", "hobby_plus", "pro", "team", "growth", "anonymous", "free"} {
		assert.Equal(t, "monthly", r.BillingPeriod(t1),
			"tier %q must default to monthly when billing_period is unset", t1)
	}
	for _, t1 := range []string{"hobby_yearly", "hobby_plus_yearly", "pro_yearly", "team_yearly"} {
		assert.Equal(t, "yearly", r.BillingPeriod(t1),
			"tier %q must report yearly", t1)
	}
}

// TestCanonicalTier strips _yearly and leaves bare tiers alone.
func TestCanonicalTier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hobby_yearly", "hobby"},
		{"hobby_plus_yearly", "hobby_plus"},
		{"pro_yearly", "pro"},
		{"team_yearly", "team"},
		{"hobby", "hobby"},
		{"hobby_plus", "hobby_plus"},
		{"pro", "pro"},
		{"team", "team"},
		{"anonymous", "anonymous"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, plans.CanonicalTier(c.in),
			"CanonicalTier(%q)", c.in)
	}
}

// TestYearlyPrices_DiscountedVsMonthlyTimesTwelve is a regression guard:
// each yearly price must be strictly less than (monthly_price * 12) so the
// "Save $X" badge is honest.
func TestYearlyPrices_DiscountedVsMonthlyTimesTwelve(t *testing.T) {
	r := plans.Default()
	for _, base := range []string{"hobby", "hobby_plus", "pro", "team"} {
		monthly := r.Get(base).PriceMonthly
		yearly := r.Get(base + "_yearly").PriceMonthly
		assert.Less(t, yearly, monthly*12,
			"%s_yearly (%d) must be cheaper than %s x 12 (%d)", base, yearly, base, monthly*12)
	}
}

// TestProAnnualIsTwoMonthsFree locks the Pro/Team yearly-pricing contract:
// (yearly / 12) / monthly must equal 10/12 ≈ 0.8333 within a small tolerance.
// This is the mathematical expression of "2 months free" — pay 10 months,
// get 12. The framing beats percentage-off by ~3.4x in conversion per
// PRICING-BEST-PRACTICES-2026-05-13.md (Athenic). As of the 2026-05-17
// contract-drift fix Hobby Annual shares this same 10/12 discount (see
// TestHobbyAnnualIsTwoMonthsFree).
func TestProAnnualIsTwoMonthsFree(t *testing.T) {
	r := plans.Default()
	const tolerance = 0.01
	const twoMonthsFreeRatio = 10.0 / 12.0 // ≈ 0.8333
	for _, base := range []string{"pro", "team"} {
		monthly := float64(r.Get(base).PriceMonthly)
		yearly := float64(r.Get(base + "_yearly").PriceMonthly)
		require.Greater(t, monthly, 0.0, "%s monthly price must be > 0", base)
		ratio := (yearly / 12.0) / monthly
		assert.InDelta(t, twoMonthsFreeRatio, ratio, tolerance,
			"%s_yearly effective monthly / %s monthly must be 10/12 ≈ 0.8333 (2 months free); got %.4f (yearly=%d, monthly=%d)",
			base, base, ratio, int(yearly), int(monthly))
	}
}

// TestHobbyAnnualIsTwoMonthsFree locks the Hobby yearly-pricing contract:
// (yearly / 12) / monthly must equal 10/12 ≈ 0.8333. Hobby Annual is
// $90/yr = $7.50/mo ("save 2 months" vs $9 x 12), matching the Pro/Team
// "2 months free" discount. Source of truth is api/plans.yaml
// (hobby_yearly price_monthly_cents: 9000); see the instanode-web
// PricingPage FIX-K note "$90/yr = $7.50/mo".
func TestHobbyAnnualIsTwoMonthsFree(t *testing.T) {
	r := plans.Default()
	const tolerance = 0.01
	const twoMonthsFreeRatio = 10.0 / 12.0 // ≈ 0.8333
	monthly := float64(r.Get("hobby").PriceMonthly)
	yearly := float64(r.Get("hobby_yearly").PriceMonthly)
	require.Greater(t, monthly, 0.0, "hobby monthly price must be > 0")
	ratio := (yearly / 12.0) / monthly
	assert.InDelta(t, twoMonthsFreeRatio, ratio, tolerance,
		"hobby_yearly effective monthly / hobby monthly must be 10/12 ≈ 0.8333 (save 2 months); got %.4f (yearly=%d, monthly=%d)",
		ratio, int(yearly), int(monthly))
}

// TestYearlyIsMonthlyTimesTen is the strict integer-cents lock for the
// "2 months free" pricing model: yearly_price_cents ==
// monthly_price_cents * 10 exactly. This makes the "2 months free" claim
// provable to the cent and keeps Razorpay plan_id <-> dashboard display
// values in lockstep. As of the 2026-05-17 contract-drift fix,
// hobby_yearly is also x10 ($90/yr = $7.50/mo) — it no longer uses the
// old x11 "save 1 month" model. hobby_plus is deliberately excluded: its
// annual variant uses a distinct "~1.5 months free" mid-discount
// ($199/yr vs $19 x 12) — see TestHobbyPlusYearlyDiscount.
func TestYearlyIsMonthlyTimesTen(t *testing.T) {
	r := plans.Default()
	for _, base := range []string{"hobby", "pro", "team"} {
		monthly := r.Get(base).PriceMonthly
		yearly := r.Get(base + "_yearly").PriceMonthly
		require.Greater(t, monthly, 0, "%s monthly price must be > 0", base)
		assert.Equal(t, monthly*10, yearly,
			"%s_yearly (%d cents) must equal %s monthly (%d cents) * 10 = %d cents",
			base, yearly, base, monthly, monthly*10)
	}
}

// TestHobbyPlusYearlyDiscount locks the mid-tier's intentionally distinct
// annual discount: hobby_plus_yearly = $199/yr against $19/mo x 12 = $228,
// i.e. "~1.5 months free" — between Hobby's "2 months free" and the
// step-up incentive. Mirrors the plans.yaml comment.
func TestHobbyPlusYearlyDiscount(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 1900, r.PriceMonthly("hobby_plus"),
		"hobby_plus monthly must be 1900 cents")
	assert.Equal(t, 19900, r.PriceMonthly("hobby_plus_yearly"),
		"hobby_plus_yearly must be 19900 cents ($199/yr) — distinct ~1.5-month discount")
	yearly := r.PriceMonthly("hobby_plus_yearly")
	monthlyTimes12 := r.PriceMonthly("hobby_plus") * 12
	assert.Less(t, yearly, monthlyTimes12,
		"hobby_plus_yearly (%d) must be cheaper than $19 x 12 (%d)", yearly, monthlyTimes12)
}

// TestHobbyYearlyPriceIsPinned is the value-pinning regression guard for
// the 2026-05-17 contract-drift fix: common/plans.go defaultYAML's
// hobby_yearly price had drifted to 9900 cents while api/plans.yaml (the
// source of truth) holds 9000 cents ($90/yr = $7.50/mo, "save 2 months",
// matching the instanode-web PricingPage FIX-K note). This test fails if
// defaultYAML's hobby_yearly price drifts off 9000 again.
func TestHobbyYearlyPriceIsPinned(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 9000, r.PriceMonthly("hobby_yearly"),
		"hobby_yearly must be 9000 cents ($90/yr) — matches api/plans.yaml source of truth")
	assert.Equal(t, 9000, r.Get("hobby_yearly").PriceMonthly,
		"hobby_yearly Plan.PriceMonthly must be 9000 cents")
}

// TestTierDiscountUniformity locks the strategic intent: the hobby/pro/team
// yearly variants all offer the same "2 months free" (10/12) discount, so
// the annual-billing pitch is uniform across the core tier ladder. As of
// the 2026-05-17 fix Hobby Annual is no longer a deliberately weaker
// discount — api/plans.yaml puts it at the same 10/12 ratio as Pro/Team.
// hobby_plus is excluded: it uses a distinct mid-discount (see
// TestHobbyPlusYearlyDiscount).
func TestTierDiscountUniformity(t *testing.T) {
	r := plans.Default()
	const tolerance = 0.01
	const twoMonthsFreeRatio = 10.0 / 12.0 // ≈ 0.8333
	ratio := func(base string) float64 {
		monthly := float64(r.Get(base).PriceMonthly)
		yearly := float64(r.Get(base + "_yearly").PriceMonthly)
		return (yearly / 12.0) / monthly
	}
	for _, base := range []string{"hobby", "pro", "team"} {
		assert.InDelta(t, twoMonthsFreeRatio, ratio(base), tolerance,
			"%s_yearly discount ratio must be 10/12 ≈ 0.8333 (2 months free); got %.4f",
			base, ratio(base))
	}
}

func TestValidatePromotion_ValidCode_ReturnsPromotion(t *testing.T) {
	yaml := `
plans:
  anonymous:
    display_name: "Anon"
    price_monthly_cents: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    limits: {provisions_per_day: -1, postgres_storage_mb: 5120, redis_memory_mb: 256}
    features: {alerts: true, custom_domains: false, sla: false}
promotions:
  - code: "SAVE20"
    discount_percent: 20
    applies_to: ["pro"]
    expires_at: ""
    max_uses: -1
    description: "20% off Pro"
`
	path := writeTempYAML(t, yaml)
	r, err := plans.Load(path)
	require.NoError(t, err)

	promo, err := r.ValidatePromotion("SAVE20", "pro")
	require.NoError(t, err)
	assert.Equal(t, 20, promo.DiscountPercent)
}

func TestValidatePromotion_CaseInsensitive(t *testing.T) {
	yaml := `
plans:
  anonymous:
    display_name: "Anon"
    price_monthly_cents: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    limits: {provisions_per_day: -1, postgres_storage_mb: 5120, redis_memory_mb: 256}
    features: {alerts: true, custom_domains: false, sla: false}
promotions:
  - code: "LAUNCH"
    discount_percent: 50
    applies_to: ["pro"]
    expires_at: ""
    max_uses: -1
    description: "Launch discount"
`
	path := writeTempYAML(t, yaml)
	r, err := plans.Load(path)
	require.NoError(t, err)

	_, err = r.ValidatePromotion("launch", "pro") // lowercase
	assert.NoError(t, err, "promotion codes must be case-insensitive")
}

func TestValidatePromotion_UnknownCode_ReturnsError(t *testing.T) {
	r := plans.Default()
	_, err := r.ValidatePromotion("DOESNOTEXIST", "pro")
	assert.Error(t, err)
}

func TestValidatePromotion_WrongPlan_ReturnsError(t *testing.T) {
	yaml := `
plans:
  anonymous:
    display_name: "Anon"
    price_monthly_cents: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    limits: {provisions_per_day: -1, postgres_storage_mb: 5120, redis_memory_mb: 256}
    features: {alerts: true, custom_domains: false, sla: false}
promotions:
  - code: "PROONLY"
    discount_percent: 10
    applies_to: ["pro"]
    expires_at: ""
    max_uses: -1
    description: "Pro only"
`
	path := writeTempYAML(t, yaml)
	r, err := plans.Load(path)
	require.NoError(t, err)

	_, err = r.ValidatePromotion("PROONLY", "anonymous")
	assert.Error(t, err, "promotion for 'pro' must not apply to 'anonymous'")
}

func TestLoad_PlansYAMLFile_MatchesDefaults(t *testing.T) {
	// Verify that the actual plans.yaml in api/ loads cleanly and that
	// its anonymous limits match the built-in defaults.
	repoRoot := filepath.Join("..", "..", "api", "plans.yaml")
	if _, err := os.Stat(repoRoot); os.IsNotExist(err) {
		t.Skip("plans.yaml not found — skipping file consistency check")
	}

	fromFile, err := plans.Load(repoRoot)
	require.NoError(t, err)

	fromDefault := plans.Default()
	assert.Equal(t, fromDefault.ProvisionLimit("anonymous"), fromFile.ProvisionLimit("anonymous"),
		"plans.yaml anonymous provision limit must match Default()")
}

func TestRegistry_TierHelpers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.PriceMonthly("anonymous"))
	assert.Equal(t, 4900, r.PriceMonthly("pro"))
	assert.Equal(t, "Pro", r.DisplayName("pro"))
	assert.False(t, r.IsDedicatedTier("pro"))
	assert.True(t, r.IsDedicatedTier("growth"))
	// W11: hobby_plus is the mid-tier — $19/mo, custom domains, 2 apps.
	assert.Equal(t, 1900, r.PriceMonthly("hobby_plus"),
		"hobby_plus monthly price must be $19/mo (1900 cents)")
	assert.Equal(t, "Hobby Plus", r.DisplayName("hobby_plus"))
}

func TestVaultMaxEntries_Tiers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.VaultMaxEntries("anonymous"))
	assert.Equal(t, 20, r.VaultMaxEntries("hobby"))
	assert.Equal(t, 50, r.VaultMaxEntries("hobby_plus"),
		"hobby_plus must allow 50 vault entries (mid-tier between hobby:20 and pro:200)")
	assert.Equal(t, 200, r.VaultMaxEntries("pro"))
	assert.Equal(t, -1, r.VaultMaxEntries("team"))
}

func TestVaultEnvsAllowed_HobbyIsProductionOnly(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, []string{"production"}, r.VaultEnvsAllowed("hobby"))
	assert.Empty(t, r.VaultEnvsAllowed("pro"))
	// 2026-05-15: hobby_plus rolled back to production-only.
	// Multi-env (dev/staging/prod) is now exclusively Pro+ — pro returns []
	// which the handler treats as "no restriction / all envs". hobby_plus
	// matches hobby's posture so the upgrade lever points cleanly at Pro.
	assert.Equal(t, []string{"production"},
		r.VaultEnvsAllowed("hobby_plus"),
		"hobby_plus is production-only (W12 rollback); Pro is the cheapest multi-env tier")
}

func TestDeploymentsAppsLimit_Tiers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.DeploymentsAppsLimit("anonymous"))
	assert.Equal(t, 1, r.DeploymentsAppsLimit("hobby"))
	assert.Equal(t, 2, r.DeploymentsAppsLimit("hobby_plus"),
		"hobby_plus must allow 2 deployment apps (doubles hobby's 1, vs pro's 10)")
	assert.Equal(t, 10, r.DeploymentsAppsLimit("pro"))
	assert.Equal(t, -1, r.DeploymentsAppsLimit("team"))
	assert.Equal(t, 50, r.DeploymentsAppsLimit("growth"),
		"growth allows 50 deployment apps (wave-3 BugBash bumped from 5 → 50, matching plans.yaml)")
}

// TestHobbyPlus_TierMatrix is the W11 lock-in test for the hobby_plus tier.
// Asserts every documented field of the new $19/mo mid-tier exists and
// matches the documented values. If anyone changes a hobby_plus limit
// without updating the marketing copy + dashboard tier card, this test
// fails so the inconsistency is caught at unit-test time, not in prod.
func TestHobbyPlus_TierMatrix(t *testing.T) {
	r := plans.Default()
	p := r.Get("hobby_plus")
	require.NotNil(t, p, "hobby_plus tier must exist in default registry")
	assert.Equal(t, "hobby_plus", p.Name)
	assert.Equal(t, "Hobby Plus", p.DisplayName)
	assert.Equal(t, 1900, p.PriceMonthly, "$19/mo = 1900 cents")
	assert.Equal(t, "", p.BillingPeriod, "monthly tier omits billing_period (defaults to monthly)")
	// Storage / connection limits — hobby_plus matches hobby on the cheap
	// services (postgres / redis), bumps mongodb + storage to mid-tier
	// values, and doubles webhooks to 5000.
	assert.Equal(t, 1024, p.Limits.PostgresStorageMB)
	assert.Equal(t, 8, p.Limits.PostgresConnections)
	assert.Equal(t, 50, p.Limits.RedisMemoryMB)
	assert.Equal(t, 1024, p.Limits.MongoStorageMB,
		"hobby_plus mongodb = 1 GB (vs hobby's 100 MB, pro's 2 GB)")
	assert.Equal(t, 5, p.Limits.MongoConnections)
	assert.Equal(t, 5120, p.Limits.StorageStorageMB,
		"hobby_plus object storage = 5 GB (vs hobby's 512 MB, pro's 10 GB)")
	assert.Equal(t, 5000, p.Limits.WebhookRequestsStored,
		"hobby_plus webhook stored = 5000 (5x hobby's 1000, half of pro's 10k)")
	assert.Equal(t, 2, p.Limits.DeploymentsApps,
		"hobby_plus = 2 deployment apps (the headline differentiator vs hobby)")
	assert.Equal(t, 50, p.Limits.VaultMaxEntries)
	// 2026-05-15 pricing pass: hobby_plus rolled back to production-only.
	// Multi-env is now Pro+ only (see multiEnvTierAllowed in stack.go).
	assert.Equal(t, []string{"production"}, p.Limits.VaultEnvsAllowed,
		"hobby_plus rolled back to production-only on 2026-05-15; multi-env is Pro+")
	// Features — custom_domains is the marquee feature that justifies
	// the $10 step up from hobby ($9 → $19).
	assert.True(t, p.Features.CustomDomains,
		"hobby_plus must enable custom_domains (the W11 headline feature)")
	assert.True(t, p.Features.Alerts)
	assert.False(t, p.Features.SLA)
	assert.False(t, p.Features.Dedicated)
}

// TestCustomDomainsMaxLimit — FIX-G (2026-05-14) locks the per-tier
// custom-domain cap so the limit can't silently drift. The cap is paired
// with the boolean Features.CustomDomains gate: tiers where the boolean
// is false MUST also have CustomDomainsMax == 0 (the handler trips the
// boolean first, so a non-zero number on a false-feature tier would be
// dead code at best and a confusing API contract at worst).
func TestCustomDomainsMaxLimit(t *testing.T) {
	r := plans.Default()
	cases := []struct {
		tier   string
		want   int
		reason string
	}{
		{"anonymous", 0, "anonymous has no custom-domain feature"},
		{"free", 0, "free mirrors anonymous"},
		{"hobby", 0, "hobby is below the custom-domain unlock"},
		{"hobby_yearly", 0, "hobby_yearly mirrors hobby"},
		{"hobby_plus", 1, "hobby_plus is the first tier with custom domains — single hostname"},
		{"hobby_plus_yearly", 1, "hobby_plus_yearly mirrors hobby_plus"},
		{"growth", 3, "growth allows 3 hostnames — sits between hobby_plus and pro"},
		{"pro", 5, "pro allows 5 hostnames"},
		{"pro_yearly", 5, "pro_yearly mirrors pro"},
		{"team", 50, "team allows 50 hostnames (effectively unlimited for dashboards)"},
		{"team_yearly", 50, "team_yearly mirrors team"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, r.CustomDomainsMaxLimit(c.tier),
			"CustomDomainsMaxLimit(%q) — %s", c.tier, c.reason)
	}
}

// TestCustomDomainsMax_PairedWithBooleanFlag guards the invariant that
// any tier with custom_domains_max > 0 must also have features.custom_domains:true,
// and any tier with custom_domains_max == 0 must have features.custom_domains:false.
// Drift between the two is a code smell — the handler trips the boolean
// first, so an inconsistent pair means either a dead cap or an unreachable
// allowance.
func TestCustomDomainsMax_PairedWithBooleanFlag(t *testing.T) {
	r := plans.Default()
	for name, p := range r.All() {
		switch {
		case p.Features.CustomDomains && p.Limits.CustomDomainsMax == 0:
			t.Errorf("tier %q has features.custom_domains=true but custom_domains_max=0 — feature is unreachable", name)
		case !p.Features.CustomDomains && p.Limits.CustomDomainsMax > 0:
			t.Errorf("tier %q has features.custom_domains=false but custom_domains_max=%d — cap is unreachable (boolean gate trips first)",
				name, p.Limits.CustomDomainsMax)
		}
	}
}

// TestRPORTOMinutes_DefaultYAMLMatchesAPIPlansYAML pins the per-tier RPO/RTO
// values in common/plans.go's defaultYAML. BugBash 2026-05-18 P2-W2-41: the
// defaultYAML const set NO rpo_minutes/rto_minutes on any tier, so plans.Default()
// reported RPO=RTO=0 for Pro/Team (real 60/15). The accessors RPOMinutes/RTOMinutes
// exist and read these fields — without them, GET /api/v1/capabilities (which is
// served by a Default()-backed registry in any environment lacking plans.yaml)
// under-reports durability and an agent reasoning about a workload's RPO/RTO
// requirement gets a false "not promised" signal for Pro/Team.
//
// This test iterates every tier in the registry so a new tier added to
// defaultYAML without rpo/rto fails here rather than silently reporting 0.
func TestRPORTOMinutes_DefaultYAMLMatchesAPIPlansYAML(t *testing.T) {
	r := plans.Default()

	// Expected per-tier RPO/RTO, mirroring api/plans.yaml exactly.
	// 0/0 = "not promised" (no scheduled backups / no self-serve restore).
	want := map[string]struct{ rpo, rto int }{
		"anonymous":         {0, 0},
		"free":              {0, 0},
		"hobby":             {1440, 30},
		"hobby_yearly":      {1440, 30},
		"hobby_plus":        {1440, 30},
		"hobby_plus_yearly": {1440, 30},
		"pro":               {60, 15},
		"pro_yearly":        {60, 15},
		"team":              {60, 15},
		"team_yearly":       {60, 15},
		"growth":            {60, 15},
	}

	// Every tier in the registry must have a pinned expectation — guards
	// against a new tier being added with no RPO/RTO coverage.
	for name := range r.All() {
		if _, ok := want[name]; !ok {
			t.Errorf("tier %q has no RPO/RTO expectation — add it to TestRPORTOMinutes_DefaultYAMLMatchesAPIPlansYAML", name)
		}
	}

	for tier, exp := range want {
		assert.Equal(t, exp.rpo, r.RPOMinutes(tier),
			"RPOMinutes(%q) must match api/plans.yaml", tier)
		assert.Equal(t, exp.rto, r.RTOMinutes(tier),
			"RTOMinutes(%q) must match api/plans.yaml", tier)
	}

	// Spot-check the specific regression: Pro must NOT report 0/0.
	require.NotZero(t, r.RPOMinutes("pro"), "Pro RPO regressed to 0 — P2-W2-41 reappeared")
	require.NotZero(t, r.RTOMinutes("pro"), "Pro RTO regressed to 0 — P2-W2-41 reappeared")
}

// writeTempYAML writes content to a temp file and returns its path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "plans-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}
