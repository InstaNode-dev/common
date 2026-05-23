// plans_coverage_test.go — white-box tests that cover branches unreachable
// through the public API (defensive nil-checks behind Registry.Get fallback,
// the parse() empty-plans guard, ValidatePromotion expiry/continue branches,
// and TeamMemberLimit per-tier defaults). Keeps the `plans` package over 95%
// line coverage without weakening the public contract.

package plans

import (
	"strings"
	"testing"
	"time"
)

// emptyRegistry builds a Registry with no plans. Used to exercise the
// defensive `if p == nil` branches on every accessor; in production these
// are unreachable because parse() rejects YAML without an anonymous plan.
func emptyRegistry() *Registry {
	return &Registry{plans: map[string]*Plan{}, promotions: nil}
}

// TestParse_EmptyPlansMap covers the `len(raw.Plans) == 0` guard at
// plans.go:217. A YAML document that defines no plans must be rejected
// before the anonymous-plan check fires.
func TestParse_EmptyPlansMap(t *testing.T) {
	cases := []string{
		// Completely empty document.
		"",
		// Explicit empty plans map.
		"plans: {}\n",
		// Only promotions, no plans.
		"promotions: []\n",
	}
	for _, in := range cases {
		_, err := parse([]byte(in))
		if err == nil {
			t.Errorf("parse(%q) = nil error, want non-nil for empty plans", in)
			continue
		}
		if !strings.Contains(err.Error(), "no plans") {
			t.Errorf("parse(%q) err = %v, want 'no plans' message", in, err)
		}
	}
}

// TestValidatePromotion_SkipsNonMatchingCode covers the `continue` at
// plans.go:266 — when the first promotion's code doesn't match the request,
// the loop must continue to the next entry rather than fall through.
func TestValidatePromotion_SkipsNonMatchingCode(t *testing.T) {
	r := &Registry{
		plans: map[string]*Plan{"anonymous": {Name: "anonymous"}, "pro": {Name: "pro"}},
		promotions: []Promotion{
			{Code: "FIRST", DiscountPercent: 10, AppliesTo: []string{"pro"}, MaxUses: -1},
			{Code: "SECOND", DiscountPercent: 25, AppliesTo: []string{"pro"}, MaxUses: -1},
		},
	}
	promo, err := r.ValidatePromotion("SECOND", "pro")
	if err != nil {
		t.Fatalf("ValidatePromotion(SECOND, pro) err = %v, want nil", err)
	}
	if promo.DiscountPercent != 25 {
		t.Errorf("got discount %d, want 25 — loop must have skipped FIRST", promo.DiscountPercent)
	}
}

// TestValidatePromotion_ExpiredCode covers the expiry-parse branch at
// plans.go:270-274. An expired promotion must return an error mentioning
// expiration.
func TestValidatePromotion_ExpiredCode(t *testing.T) {
	// Yesterday in UTC, formatted as YYYY-MM-DD.
	yesterday := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	r := &Registry{
		plans: map[string]*Plan{"anonymous": {Name: "anonymous"}, "pro": {Name: "pro"}},
		promotions: []Promotion{
			{Code: "EXPIRED", DiscountPercent: 50, AppliesTo: []string{"pro"}, ExpiresAt: yesterday, MaxUses: -1},
		},
	}
	_, err := r.ValidatePromotion("EXPIRED", "pro")
	if err == nil {
		t.Fatal("ValidatePromotion(EXPIRED, pro) err = nil, want expired error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v, want substring 'expired'", err)
	}
}

// TestValidatePromotion_FutureExpiry covers the "expiry parsed but not yet
// reached" path — the time.Parse succeeds and time.Now().After returns false,
// so the promotion is returned. Distinct from the expired path above.
func TestValidatePromotion_FutureExpiry(t *testing.T) {
	tomorrow := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	r := &Registry{
		plans: map[string]*Plan{"anonymous": {Name: "anonymous"}, "pro": {Name: "pro"}},
		promotions: []Promotion{
			{Code: "LATER", DiscountPercent: 30, AppliesTo: []string{"pro"}, ExpiresAt: tomorrow, MaxUses: -1},
		},
	}
	promo, err := r.ValidatePromotion("LATER", "pro")
	if err != nil {
		t.Fatalf("ValidatePromotion(LATER, pro) err = %v, want nil", err)
	}
	if promo.DiscountPercent != 30 {
		t.Errorf("got %d, want 30", promo.DiscountPercent)
	}
}

// TestValidatePromotion_UnparseableExpiry covers the silent-skip behaviour
// when ExpiresAt fails to parse — the entry is treated as never-expiring.
func TestValidatePromotion_UnparseableExpiry(t *testing.T) {
	r := &Registry{
		plans: map[string]*Plan{"anonymous": {Name: "anonymous"}, "pro": {Name: "pro"}},
		promotions: []Promotion{
			{Code: "GARBAGE", DiscountPercent: 5, AppliesTo: []string{"pro"}, ExpiresAt: "not-a-date", MaxUses: -1},
		},
	}
	_, err := r.ValidatePromotion("GARBAGE", "pro")
	if err != nil {
		t.Errorf("unparseable expiry should be treated as never-expires; got err = %v", err)
	}
}

// TestTeamMemberLimit_AllTierDefaults exercises every branch of the
// fallback-default switch at plans.go:340-349. Each case is hit by a Plan
// whose TeamMembers field is 0 (i.e. unset in YAML).
func TestTeamMemberLimit_AllTierDefaults(t *testing.T) {
	r := &Registry{plans: map[string]*Plan{
		"anonymous": {Name: "anonymous"},
		"team":      {Name: "team"},
		"pro":       {Name: "pro"},
		"growth":    {Name: "growth"},
		"hobby":     {Name: "hobby"},   // hits the default branch (= 1)
		"weird":     {Name: "weird"},   // unknown tier → Get falls back to anonymous → 1
	}}
	cases := map[string]int{
		"team":      -1, // unlimited
		"pro":       5,
		"growth":    10,
		"hobby":     1, // default branch
		"anonymous": 1, // default branch
	}
	for tier, want := range cases {
		if got := r.TeamMemberLimit(tier); got != want {
			t.Errorf("TeamMemberLimit(%q) = %d, want %d", tier, got, want)
		}
	}
}

// TestTeamMemberLimit_ExplicitYAMLOverride covers the early-return at
// plans.go:337 — a non-zero TeamMembers value short-circuits the default
// switch.
func TestTeamMemberLimit_ExplicitYAMLOverride(t *testing.T) {
	r := &Registry{plans: map[string]*Plan{
		"anonymous": {Name: "anonymous"},
		"pro":       {Name: "pro", Limits: Limits{TeamMembers: 7}},
	}}
	if got := r.TeamMemberLimit("pro"); got != 7 {
		t.Errorf("TeamMemberLimit(pro) = %d, want 7 (explicit YAML override)", got)
	}
}

// TestQueueCountLimit_ZeroFallback covers the `QueueCount == 0` branch at
// plans.go:489-491 — an older YAML with the field absent must return -1
// (unlimited) so existing customers don't get blocked.
func TestQueueCountLimit_ZeroFallback(t *testing.T) {
	r := &Registry{plans: map[string]*Plan{
		"anonymous": {Name: "anonymous"},
		"legacy":    {Name: "legacy"}, // QueueCount defaults to 0
	}}
	if got := r.QueueCountLimit("legacy"); got != -1 {
		t.Errorf("QueueCountLimit(legacy) = %d, want -1 (zero-fallback)", got)
	}
}

// TestAccessors_NilPlanBranches exercises the defensive `if p == nil`
// branches that are unreachable through the public API (because Get()
// returns the anonymous fallback). Constructed via an empty Registry so
// every accessor's p==nil path is hit.
func TestAccessors_NilPlanBranches(t *testing.T) {
	r := emptyRegistry()

	if got := r.BillingPeriod("anything"); got != "monthly" {
		t.Errorf("BillingPeriod(nil) = %q, want 'monthly'", got)
	}
	if got := r.CustomDomainsMaxLimit("anything"); got != 0 {
		t.Errorf("CustomDomainsMaxLimit(nil) = %d, want 0", got)
	}
	if got := r.VaultMaxEntries("anything"); got != 0 {
		t.Errorf("VaultMaxEntries(nil) = %d, want 0", got)
	}
	envs := r.VaultEnvsAllowed("anything")
	if envs == nil || len(envs) != 0 {
		t.Errorf("VaultEnvsAllowed(nil) = %v, want empty non-nil slice", envs)
	}
	if got := r.DeploymentsAppsLimit("anything"); got != -1 {
		t.Errorf("DeploymentsAppsLimit(nil) = %d, want -1", got)
	}
	if got := r.QueueCountLimit("anything"); got != -1 {
		t.Errorf("QueueCountLimit(nil) = %d, want -1", got)
	}
	if got := r.BackupRetentionDays("anything"); got != 0 {
		t.Errorf("BackupRetentionDays(nil) = %d, want 0", got)
	}
	if r.BackupRestoreEnabled("anything") {
		t.Error("BackupRestoreEnabled(nil) = true, want false")
	}
	if got := r.ManualBackupsPerDay("anything"); got != 0 {
		t.Errorf("ManualBackupsPerDay(nil) = %d, want 0", got)
	}
	if got := r.RPOMinutes("anything"); got != 0 {
		t.Errorf("RPOMinutes(nil) = %d, want 0", got)
	}
	if got := r.RTOMinutes("anything"); got != 0 {
		t.Errorf("RTOMinutes(nil) = %d, want 0", got)
	}
}

// TestVaultEnvsAllowed_NilSliceInPlan covers the `p.Limits.VaultEnvsAllowed
// == nil` branch at plans.go:452 — a plan with the field unset must surface
// an empty slice (not nil) so callers can range over it safely.
func TestVaultEnvsAllowed_NilSliceInPlan(t *testing.T) {
	r := &Registry{plans: map[string]*Plan{
		"anonymous": {Name: "anonymous"},
		"weird":     {Name: "weird"}, // VaultEnvsAllowed defaults to nil
	}}
	envs := r.VaultEnvsAllowed("weird")
	if envs == nil {
		t.Error("VaultEnvsAllowed returned nil, want empty slice")
	}
	if len(envs) != 0 {
		t.Errorf("VaultEnvsAllowed = %v, want empty slice", envs)
	}
}

// TestDefault_DoesNotPanic guards plans.go:551 — Default() must succeed on
// the embedded defaultYAML. If defaultYAML ever drifts to invalid syntax,
// this fails before the panic.
func TestDefault_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Default() panicked: %v", r)
		}
	}()
	r := Default()
	if r == nil {
		t.Fatal("Default() returned nil")
	}
	if len(r.plans) == 0 {
		t.Fatal("Default() returned empty registry")
	}
}
