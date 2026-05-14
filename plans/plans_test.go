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
// PRICING-BEST-PRACTICES-2026-05-13.md (Athenic). Hobby is *intentionally*
// excluded: it gets a smaller "save 1 month" discount (see
// TestHobbyAnnualIsOneMonthFree) so the savings differential nudges
// hobbyists to tier-skip into Pro Annual rather than just upgrade frequency.
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

// TestHobbyAnnualIsOneMonthFree locks the Hobby-specific "save 1 month"
// contract: (yearly / 12) / monthly must equal 11/12 ≈ 0.9167. Hobby
// Annual is deliberately a weaker discount than Pro/Team Annual so the
// savings differential nudges hobbyists to tier-skip into Pro Annual
// (which saves "2 months free / $98") rather than just upgrade frequency.
func TestHobbyAnnualIsOneMonthFree(t *testing.T) {
	r := plans.Default()
	const tolerance = 0.01
	const oneMonthFreeRatio = 11.0 / 12.0 // ≈ 0.9167
	monthly := float64(r.Get("hobby").PriceMonthly)
	yearly := float64(r.Get("hobby_yearly").PriceMonthly)
	require.Greater(t, monthly, 0.0, "hobby monthly price must be > 0")
	ratio := (yearly / 12.0) / monthly
	assert.InDelta(t, oneMonthFreeRatio, ratio, tolerance,
		"hobby_yearly effective monthly / hobby monthly must be 11/12 ≈ 0.9167 (save 1 month); got %.4f (yearly=%d, monthly=%d)",
		ratio, int(yearly), int(monthly))
}

// TestProTeamYearlyIsMonthlyTimesTen is the strict integer-cents lock for
// the Pro/Team "2 months free" pricing model: yearly_price_cents ==
// monthly_price_cents * 10 exactly. This makes the "2 months free" claim
// provable to the cent and keeps Razorpay plan_id <-> dashboard display
// values in lockstep. Hobby has its own x11 lock (see
// TestHobbyYearlyIsMonthlyTimesEleven).
func TestProTeamYearlyIsMonthlyTimesTen(t *testing.T) {
	r := plans.Default()
	for _, base := range []string{"pro", "team"} {
		monthly := r.Get(base).PriceMonthly
		yearly := r.Get(base + "_yearly").PriceMonthly
		require.Greater(t, monthly, 0, "%s monthly price must be > 0", base)
		assert.Equal(t, monthly*10, yearly,
			"%s_yearly (%d cents) must equal %s monthly (%d cents) * 10 = %d cents",
			base, yearly, base, monthly, monthly*10)
	}
}

// TestHobbyYearlyIsMonthlyTimesEleven is the strict integer-cents lock for
// the Hobby "save 1 month" pricing model: hobby_yearly == hobby monthly * 11
// exactly. Differentiated from Pro/Team (which use x10) so Hobby Annual
// looks deliberately weaker, nudging tier-skip to Pro Annual.
func TestHobbyYearlyIsMonthlyTimesEleven(t *testing.T) {
	r := plans.Default()
	monthly := r.Get("hobby").PriceMonthly
	yearly := r.Get("hobby_yearly").PriceMonthly
	require.Greater(t, monthly, 0, "hobby monthly price must be > 0")
	assert.Equal(t, monthly*11, yearly,
		"hobby_yearly (%d cents) must equal hobby monthly (%d cents) * 11 = %d cents",
		yearly, monthly, monthly*11)
}

// TestTierDiscountDifferentiation locks the strategic intent: Pro Annual
// must be a *strictly better* discount than Hobby Annual so the savings
// differential nudges hobbyists to tier-skip rather than just upgrade
// frequency. Expressed as: pro_yearly_ratio < hobby_yearly_ratio where
// ratio = (yearly / 12) / monthly. Lower ratio = better discount. If
// someone "fixes" Hobby to also be 10/12, this test fails — the
// differentiation is the product directive, not an accident.
func TestTierDiscountDifferentiation(t *testing.T) {
	r := plans.Default()
	ratio := func(base string) float64 {
		monthly := float64(r.Get(base).PriceMonthly)
		yearly := float64(r.Get(base + "_yearly").PriceMonthly)
		return (yearly / 12.0) / monthly
	}
	hobbyRatio := ratio("hobby")
	proRatio := ratio("pro")
	teamRatio := ratio("team")
	assert.Less(t, proRatio, hobbyRatio,
		"pro_yearly ratio (%.4f) must be strictly < hobby_yearly ratio (%.4f) so Pro Annual is the obviously-best value",
		proRatio, hobbyRatio)
	assert.Less(t, teamRatio, hobbyRatio,
		"team_yearly ratio (%.4f) must be strictly < hobby_yearly ratio (%.4f) so Team Annual is the obviously-best value",
		teamRatio, hobbyRatio)
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
	// hobby_plus is the first paid tier with multi-env support (dev/staging/prod).
	assert.Equal(t, []string{"development", "staging", "production"},
		r.VaultEnvsAllowed("hobby_plus"),
		"hobby_plus must allow dev/staging/prod envs (multi-env is the upgrade lever)")
}

func TestDeploymentsAppsLimit_Tiers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.DeploymentsAppsLimit("anonymous"))
	assert.Equal(t, 1, r.DeploymentsAppsLimit("hobby"))
	assert.Equal(t, 2, r.DeploymentsAppsLimit("hobby_plus"),
		"hobby_plus must allow 2 deployment apps (doubles hobby's 1, vs pro's 10)")
	assert.Equal(t, 10, r.DeploymentsAppsLimit("pro"))
	assert.Equal(t, -1, r.DeploymentsAppsLimit("team"))
	assert.Equal(t, 5, r.DeploymentsAppsLimit("growth"))
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
	assert.Equal(t, []string{"development", "staging", "production"}, p.Limits.VaultEnvsAllowed,
		"hobby_plus is the cheapest tier with multi-env vault support")
	// Features — custom_domains is the marquee feature that justifies
	// the $10 step up from hobby ($9 → $19).
	assert.True(t, p.Features.CustomDomains,
		"hobby_plus must enable custom_domains (the W11 headline feature)")
	assert.True(t, p.Features.Alerts)
	assert.False(t, p.Features.SLA)
	assert.False(t, p.Features.Dedicated)
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
