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
	// 6 base tiers + 3 yearly variants (hobby_yearly, pro_yearly, team_yearly) = 9.
	assert.Len(t, all, 9, "default registry must have 9 plans (6 base + 3 yearly variants)")
	for _, name := range []string{
		"anonymous", "free", "hobby", "pro", "team", "growth",
		"hobby_yearly", "pro_yearly", "team_yearly",
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
	for _, base := range []string{"hobby", "pro", "team"} {
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
	for _, t1 := range []string{"hobby", "pro", "team", "growth", "anonymous", "free"} {
		assert.Equal(t, "monthly", r.BillingPeriod(t1),
			"tier %q must default to monthly when billing_period is unset", t1)
	}
	for _, t1 := range []string{"hobby_yearly", "pro_yearly", "team_yearly"} {
		assert.Equal(t, "yearly", r.BillingPeriod(t1),
			"tier %q must report yearly", t1)
	}
}

// TestCanonicalTier strips _yearly and leaves bare tiers alone.
func TestCanonicalTier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hobby_yearly", "hobby"},
		{"pro_yearly", "pro"},
		{"team_yearly", "team"},
		{"hobby", "hobby"},
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
	for _, base := range []string{"hobby", "pro", "team"} {
		monthly := r.Get(base).PriceMonthly
		yearly := r.Get(base + "_yearly").PriceMonthly
		assert.Less(t, yearly, monthly*12,
			"%s_yearly (%d) must be cheaper than %s x 12 (%d)", base, yearly, base, monthly*12)
	}
}

// TestYearlyIsTwoMonthsFree locks the yearly-pricing contract:
// (yearly / 12) / monthly must equal 10/12 ≈ 0.8333 within a small tolerance
// for each of hobby/pro/team. This is the mathematical expression of "2
// months free" — pay 10 months, get 12. The framing beats percentage-off by
// ~3.4x in conversion per PRICING-BEST-PRACTICES-2026-05-13.md (Athenic).
// Future price changes that accidentally drift the discount (e.g. forgetting
// to re-derive the yearly cents from the new monthly) will fail this test.
func TestYearlyIsTwoMonthsFree(t *testing.T) {
	r := plans.Default()
	const tolerance = 0.01
	const twoMonthsFreeRatio = 10.0 / 12.0 // ≈ 0.8333
	for _, base := range []string{"hobby", "pro", "team"} {
		monthly := float64(r.Get(base).PriceMonthly)
		yearly := float64(r.Get(base + "_yearly").PriceMonthly)
		require.Greater(t, monthly, 0.0, "%s monthly price must be > 0", base)
		ratio := (yearly / 12.0) / monthly
		assert.InDelta(t, twoMonthsFreeRatio, ratio, tolerance,
			"%s_yearly effective monthly / %s monthly must be 10/12 ≈ 0.8333 (2 months free); got %.4f (yearly=%d, monthly=%d)",
			base, base, ratio, int(yearly), int(monthly))
	}
}

// TestYearlyIsExactlyMonthlyTimesTen is the strict integer-cents lock for
// the "2 months free" pricing model: yearly_price_cents == monthly_price_cents * 10
// exactly. This makes the "2 months free" claim provable to the cent and
// keeps the Razorpay plan_id <-> dashboard display values in lockstep.
func TestYearlyIsExactlyMonthlyTimesTen(t *testing.T) {
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

func TestValidatePromotion_ValidCode_ReturnsPromotion(t *testing.T) {
	yaml := `
plans:
  anonymous:
    display_name: "Anon"
    price_monthly_cents: 0
    trial_days: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    trial_days: 0
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
    trial_days: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    trial_days: 0
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
    trial_days: 0
    limits: {provisions_per_day: 5, postgres_storage_mb: 10, redis_memory_mb: 5}
    features: {alerts: false, custom_domains: false, sla: false}
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    trial_days: 0
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
	assert.Equal(t, 14, r.TrialDays("hobby"))
	assert.False(t, r.IsDedicatedTier("pro"))
	assert.True(t, r.IsDedicatedTier("growth"))
}

func TestVaultMaxEntries_Tiers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.VaultMaxEntries("anonymous"))
	assert.Equal(t, 20, r.VaultMaxEntries("hobby"))
	assert.Equal(t, 200, r.VaultMaxEntries("pro"))
	assert.Equal(t, -1, r.VaultMaxEntries("team"))
}

func TestVaultEnvsAllowed_HobbyIsProductionOnly(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, []string{"production"}, r.VaultEnvsAllowed("hobby"))
	assert.Empty(t, r.VaultEnvsAllowed("pro"))
}

func TestDeploymentsAppsLimit_Tiers(t *testing.T) {
	r := plans.Default()
	assert.Equal(t, 0, r.DeploymentsAppsLimit("anonymous"))
	assert.Equal(t, 1, r.DeploymentsAppsLimit("hobby"))
	assert.Equal(t, 10, r.DeploymentsAppsLimit("pro"))
	assert.Equal(t, -1, r.DeploymentsAppsLimit("team"))
	assert.Equal(t, 5, r.DeploymentsAppsLimit("growth"))
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
