// Package plans loads and provides typed access to the plans.yaml configuration.
// All plan limits and pricing live in that file — no hard-coded values here.
//
// Usage:
//
//	registry, err := plans.Load("plans.yaml")
//	price := registry.Get("pro").PriceMonthly // e.g. 4900 (cents)
package plans

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Limits defines the quantitative constraints for a plan tier.
// A value of -1 means unlimited.
type Limits struct {
	// ProvisionsPerDay is the maximum new resources a fingerprint/team may create per day.
	ProvisionsPerDay int `yaml:"provisions_per_day"`

	// Phase 2+: per-resource storage and throughput limits.
	// All values of -1 mean unlimited.

	// PostgresStorageMB is the maximum storage per Postgres database in megabytes.
	PostgresStorageMB int `yaml:"postgres_storage_mb"`
	// PostgresConnections is the maximum concurrent connections per Postgres database.
	PostgresConnections int `yaml:"postgres_connections"`

	// RedisMemoryMB is the maximum memory per Redis namespace in megabytes.
	RedisMemoryMB int `yaml:"redis_memory_mb"`
	// RedisCommandsPerDay is the maximum Redis commands per token per day.
	RedisCommandsPerDay int `yaml:"redis_commands_per_day"`

	// MongoStorageMB is the maximum storage per MongoDB database in megabytes.
	MongoStorageMB int `yaml:"mongodb_storage_mb"`
	// MongoConnections is the maximum concurrent connections per MongoDB database.
	MongoConnections int `yaml:"mongodb_connections"`
	// MongoOpsPerMinute is the maximum MongoDB operations per token per minute.
	MongoOpsPerMinute int `yaml:"mongodb_ops_per_minute"`

	// QueueStorageMB is the maximum JetStream storage per NATS resource in megabytes.
	QueueStorageMB int `yaml:"queue_storage_mb"`

	// StorageStorageMB is the maximum object storage per R2 prefix in megabytes.
	StorageStorageMB int `yaml:"storage_storage_mb"`

	// WebhookRequestsStored is the maximum number of received webhook payloads retained.
	WebhookRequestsStored int `yaml:"webhook_requests_stored"`

	// TeamMembers is the maximum users per team (including the owner). -1 means unlimited.
	// When unset (0) in older YAML, TeamMemberLimit applies built-in defaults per tier.
	TeamMembers int `yaml:"team_members"`

	// VaultMaxEntries is the maximum number of vault entries per team. -1 means unlimited.
	// 0 means the vault feature is not available on this tier.
	VaultMaxEntries int `yaml:"vault_max_entries"`

	// VaultEnvsAllowed is the list of environment names permitted for vault entries.
	// An empty slice means any env name is allowed (i.e. unlimited custom envs).
	VaultEnvsAllowed []string `yaml:"vault_envs_allowed"`

	// DeploymentsApps is the maximum number of deployable applications per team.
	// -1 means unlimited; 0 means deployments are not available on this tier.
	DeploymentsApps int `yaml:"deployments_apps"`
}

// Features describes the boolean capabilities unlocked by a plan tier.
type Features struct {
	// Alerts enables email and webhook notifications for billing and usage alerts.
	Alerts bool `yaml:"alerts"`
	// CustomDomains allows isolated instances with custom connection hostnames.
	CustomDomains bool `yaml:"custom_domains"`
	// SLA enables the service-level agreement commitments for the team tier.
	SLA bool `yaml:"sla"`
	// Dedicated is true when the tier provisions single-tenant isolated backends (see growth in plans.yaml).
	Dedicated bool `yaml:"dedicated"`
}

// Plan is the fully resolved configuration for one pricing tier.
type Plan struct {
	// Name is the internal tier key (e.g. "pro"). Set by the loader, not the YAML.
	Name string `yaml:"-"`
	// DisplayName is the human-readable label shown to users.
	DisplayName string `yaml:"display_name"`
	// PriceMonthly is the recurring price in USD cents (0 = free). For
	// yearly variants this stores the *annual* price in cents — the
	// effective per-month figure is derived in the UI.
	PriceMonthly int `yaml:"price_monthly_cents"`
	// BillingPeriod is "monthly" (default) or "yearly". The {tier}_yearly
	// plans set this to "yearly" so callers can distinguish them from the
	// monthly counterpart at billing-cycle time. Empty == "monthly".
	BillingPeriod string `yaml:"billing_period"`
	// TrialDays is the length of the free trial in days (0 = no trial).
	TrialDays int `yaml:"trial_days"`
	// Limits holds all quantitative constraints for this tier.
	Limits Limits `yaml:"limits"`
	// Features holds the boolean feature flags for this tier.
	Features Features `yaml:"features"`
}

// Promotion describes a discount code redeemable at checkout.
// An empty ExpiresAt string means the code never expires. MaxUses of -1 means unlimited.
type Promotion struct {
	// Code is the case-insensitive coupon code (e.g. "LAUNCH50").
	Code string `yaml:"code"`
	// DiscountPercent is the whole-number percentage off (e.g. 50 = 50% off).
	DiscountPercent int `yaml:"discount_percent"`
	// AppliesTo is the list of plan tiers this code may be applied to.
	AppliesTo []string `yaml:"applies_to"`
	// ExpiresAt is the last date the code is valid in "YYYY-MM-DD" format.
	// An empty string means the code never expires.
	ExpiresAt string `yaml:"expires_at"`
	// MaxUses is the maximum number of redemptions allowed (-1 = unlimited).
	MaxUses int `yaml:"max_uses"`
	// Description is a human-readable note for the operations team.
	Description string `yaml:"description"`
}

// Registry is an in-memory index of all plan and promotion definitions.
// Load it once at startup and share it across handlers.
type Registry struct {
	plans      map[string]*Plan
	promotions []Promotion
}

// rawConfig is the top-level YAML structure.
type rawConfig struct {
	Plans      map[string]*Plan `yaml:"plans"`
	Promotions []Promotion      `yaml:"promotions"`
}

// Load reads and parses a plans YAML file and returns a validated Registry.
// Returns an error if the file cannot be read, is invalid YAML, or is missing
// the "anonymous" plan definition (which is the fallback for unknown tiers).
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plans.Load: read %q: %w", path, err)
	}
	return parse(data)
}

// parse decodes raw YAML bytes into a validated Registry.
func parse(data []byte) (*Registry, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("plans.parse: invalid YAML: %w", err)
	}
	if len(raw.Plans) == 0 {
		return nil, fmt.Errorf("plans.parse: no plans defined in config")
	}
	if _, ok := raw.Plans["anonymous"]; !ok {
		return nil, fmt.Errorf("plans.parse: missing required plan 'anonymous' (used as fallback)")
	}

	// Stamp each plan with its key name so callers don't have to track it separately.
	for name, p := range raw.Plans {
		p.Name = name
	}

	return &Registry{
		plans:      raw.Plans,
		promotions: raw.Promotions,
	}, nil
}

// Get returns the Plan for the given tier name.
// If the tier is not recognised, the "anonymous" plan is returned (safe fallback).
func (r *Registry) Get(tier string) *Plan {
	if p, ok := r.plans[tier]; ok {
		return p
	}
	return r.plans["anonymous"]
}

// ProvisionLimit returns the daily provisioning limit for the given tier.
// Returns -1 for unlimited.
func (r *Registry) ProvisionLimit(tier string) int {
	return r.Get(tier).Limits.ProvisionsPerDay
}

// All returns all plans keyed by tier name. The map is read-only; do not mutate it.
func (r *Registry) All() map[string]*Plan {
	return r.plans
}

// Promotions returns all configured promotion codes.
func (r *Registry) Promotions() []Promotion {
	return r.promotions
}

// ValidatePromotion checks whether the given code is valid for the target plan tier.
// Returns the matching Promotion or an error describing why it is not applicable.
func (r *Registry) ValidatePromotion(code, targetTier string) (*Promotion, error) {
	upperCode := strings.ToUpper(code)
	for i := range r.promotions {
		p := &r.promotions[i]
		if strings.ToUpper(p.Code) != upperCode {
			continue
		}
		// Check expiry (empty string = never expires).
		if p.ExpiresAt != "" {
			expiry, parseErr := time.Parse("2006-01-02", p.ExpiresAt)
			if parseErr == nil && time.Now().UTC().After(expiry.AddDate(0, 0, 1)) {
				return nil, fmt.Errorf("promotion %q has expired", code)
			}
		}
		// Check plan applicability.
		applies := false
		for _, t := range p.AppliesTo {
			if t == targetTier {
				applies = true
				break
			}
		}
		if !applies {
			return nil, fmt.Errorf("promotion %q does not apply to plan %q", code, targetTier)
		}
		return p, nil
	}
	return nil, fmt.Errorf("promotion code %q not found", code)
}

// StorageLimitMB returns the storage limit in MB for the given tier and service type.
// service must be one of "postgres", "redis", "mongodb". Returns -1 for unlimited.
func (r *Registry) StorageLimitMB(tier, service string) int {
	p := r.Get(tier)
	switch service {
	case "postgres":
		return p.Limits.PostgresStorageMB
	case "redis":
		return p.Limits.RedisMemoryMB
	case "mongodb":
		return p.Limits.MongoStorageMB
	case "queue":
		return p.Limits.QueueStorageMB
	case "storage":
		return p.Limits.StorageStorageMB
	case "webhook":
		return p.Limits.WebhookRequestsStored
	}
	return -1
}

// ConnectionsLimit returns the max concurrent connections for the given tier and service.
// Returns -1 for unlimited.
func (r *Registry) ConnectionsLimit(tier, service string) int {
	p := r.Get(tier)
	switch service {
	case "postgres":
		return p.Limits.PostgresConnections
	case "mongodb":
		return p.Limits.MongoConnections
	}
	return -1
}

// TeamMemberLimit returns the maximum team size for the tier (including owner).
// Returns -1 for unlimited. Missing YAML (0) maps to tier-specific defaults.
func (r *Registry) TeamMemberLimit(tier string) int {
	n := r.Get(tier).Limits.TeamMembers
	if n != 0 {
		return n
	}
	switch tier {
	case "team":
		return -1
	case "pro":
		return 5
	case "growth":
		return 10
	default:
		return 1
	}
}

// ThroughputLimit returns the daily throughput limit (commands/ops/requests) for
// the given tier and service. Returns -1 for unlimited.
func (r *Registry) ThroughputLimit(tier, service string) int {
	p := r.Get(tier)
	switch service {
	case "redis":
		return p.Limits.RedisCommandsPerDay
	case "mongodb":
		return p.Limits.MongoOpsPerMinute // per-minute; callers scale accordingly
	}
	return -1
}

// PriceMonthly returns the recurring price in USD cents for the tier (0 = free).
func (r *Registry) PriceMonthly(tier string) int {
	return r.Get(tier).PriceMonthly
}

// DisplayName returns the human-readable plan label for the tier.
func (r *Registry) DisplayName(tier string) string {
	return r.Get(tier).DisplayName
}

// TrialDays returns the free-trial length in days for the tier (0 = no trial).
func (r *Registry) TrialDays(tier string) int {
	return r.Get(tier).TrialDays
}

// IsDedicatedTier reports whether the tier provisions dedicated backends.
func (r *Registry) IsDedicatedTier(tier string) bool {
	return r.Get(tier).Features.Dedicated
}

// BillingPeriod returns the billing cycle for the tier — "yearly" for
// the *_yearly variants, "monthly" for everything else. The webhook + DB
// store only the canonical tier (CanonicalTier strips the suffix), so this
// helper exists so callers that care about the cycle (UI, audit logs) can
// recover it from the plan name.
func (r *Registry) BillingPeriod(tier string) string {
	p := r.Get(tier)
	if p == nil {
		return "monthly"
	}
	if p.BillingPeriod == "yearly" {
		return "yearly"
	}
	return "monthly"
}

// CanonicalTier strips the "_yearly" suffix and returns the base tier name
// (e.g. "pro_yearly" -> "pro"). Used by the webhook + dashboard mapping so
// the team's plan_tier column stores the canonical name and limits resolve
// the same way regardless of billing cycle.
func CanonicalTier(tier string) string {
	if strings.HasSuffix(tier, "_yearly") {
		return strings.TrimSuffix(tier, "_yearly")
	}
	return tier
}

// CustomDomainsAllowed reports whether the given tier may bind custom
// hostnames to its stacks. Mirrors the `features.custom_domains` flag in
// plans.yaml — currently true only for "pro", "team", and "growth".
func (r *Registry) CustomDomainsAllowed(tier string) bool {
	return r.Get(tier).Features.CustomDomains
}

// VaultMaxEntries returns the per-team vault entry cap for the given tier.
// -1 means unlimited; 0 means vault is not available on this tier.
func (r *Registry) VaultMaxEntries(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.VaultMaxEntries
}

// VaultEnvsAllowed returns the list of allowed env names for vault on the
// given tier. An empty slice means any env name is allowed (Pro/Team).
// Returns an empty slice when the plan or limit is missing.
func (r *Registry) VaultEnvsAllowed(tier string) []string {
	p := r.Get(tier)
	if p == nil {
		return []string{}
	}
	if p.Limits.VaultEnvsAllowed == nil {
		return []string{}
	}
	return p.Limits.VaultEnvsAllowed
}

// DeploymentsAppsLimit returns the max number of deployable apps for the tier.
// -1 means unlimited; 0 means deployments are not available on this tier.
func (r *Registry) DeploymentsAppsLimit(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return -1
	}
	return p.Limits.DeploymentsApps
}

// Default returns a Registry built from hardcoded defaults.
// Used in tests and when plans.yaml is not present (development convenience).
func Default() *Registry {
	r, err := parse([]byte(defaultYAML))
	if err != nil {
		// defaultYAML is tested — panic here is a programming error.
		panic("plans.Default: invalid built-in YAML: " + err.Error())
	}
	return r
}

// defaultYAML is the same content as plans.yaml, embedded for use in tests
// and as a fallback when no file path is configured.
const defaultYAML = `
plans:
  anonymous:
    display_name: "Anonymous"
    price_monthly_cents: 0
    trial_days: 0
    limits:
      provisions_per_day: 5
      postgres_storage_mb: 10
      postgres_connections: 2
      redis_memory_mb: 5
      redis_commands_per_day: 1000
      mongodb_storage_mb: 5
      mongodb_connections: 2
      mongodb_ops_per_minute: 100
      queue_storage_mb: 1024
      storage_storage_mb: 10
      webhook_requests_stored: 100
      team_members: 1
      vault_max_entries: 0
      vault_envs_allowed: []
      deployments_apps: 0
    features:
      alerts: false
      custom_domains: false
      sla: false
  # free mirrors anonymous exactly. anonymous is pre-claim (no team_id);
  # free is claimed-but-unpaid (team_id set, no Razorpay subscription).
  # Limits + features must stay byte-for-byte identical to anonymous so an
  # anonymous->free flip at claim time can't widen or narrow quotas. The
  # 24h reaper still applies — pay-from-day-one policy holds for both.
  free:
    display_name: "Free"
    price_monthly_cents: 0
    trial_days: 0
    limits:
      provisions_per_day: 5
      postgres_storage_mb: 10
      postgres_connections: 2
      redis_memory_mb: 5
      redis_commands_per_day: 1000
      mongodb_storage_mb: 5
      mongodb_connections: 2
      mongodb_ops_per_minute: 100
      queue_storage_mb: 1024
      storage_storage_mb: 10
      webhook_requests_stored: 100
      team_members: 1
      vault_max_entries: 0
      vault_envs_allowed: []
      deployments_apps: 0
    features:
      alerts: false
      custom_domains: false
      sla: false
  hobby:
    display_name: "Hobby"
    price_monthly_cents: 900
    trial_days: 14
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 100
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      storage_storage_mb: 512
      webhook_requests_stored: 1000
      team_members: 1
      vault_max_entries: 20
      vault_envs_allowed: ["production"]
      deployments_apps: 1
    features:
      alerts: true
      custom_domains: false
      sla: false
  # hobby_yearly mirrors hobby exactly — same limits + features. Only the
  # billing period and price differ ($90/yr ≈ $7.50/mo, ~17% off vs $9 x 12).
  # The webhook upgrades teams to the "hobby" tier regardless of which
  # cycle the user paid on; this variant exists only so the checkout
  # handler can pick the right Razorpay plan_id at subscribe time.
  hobby_yearly:
    display_name: "Hobby (yearly)"
    price_monthly_cents: 9000
    billing_period: "yearly"
    trial_days: 14
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 100
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      storage_storage_mb: 512
      webhook_requests_stored: 1000
      team_members: 1
      vault_max_entries: 20
      vault_envs_allowed: ["production"]
      deployments_apps: 1
    features:
      alerts: true
      custom_domains: false
      sla: false
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 5120
      postgres_connections: 20
      redis_memory_mb: 256
      redis_commands_per_day: 500000
      mongodb_storage_mb: 2048
      mongodb_connections: 20
      mongodb_ops_per_minute: 10000
      queue_storage_mb: 10240
      storage_storage_mb: 10240
      webhook_requests_stored: 10000
      team_members: 5
      vault_max_entries: 200
      vault_envs_allowed: []
      deployments_apps: 10
    features:
      alerts: true
      custom_domains: true
      sla: false
  # pro_yearly mirrors pro exactly. $490/yr ≈ $40.83/mo (~17% off $49 x 12).
  pro_yearly:
    display_name: "Pro (yearly)"
    price_monthly_cents: 49000
    billing_period: "yearly"
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 5120
      postgres_connections: 20
      redis_memory_mb: 256
      redis_commands_per_day: 500000
      mongodb_storage_mb: 2048
      mongodb_connections: 20
      mongodb_ops_per_minute: 10000
      queue_storage_mb: 10240
      storage_storage_mb: 10240
      webhook_requests_stored: 10000
      team_members: 5
      vault_max_entries: 200
      vault_envs_allowed: []
      deployments_apps: 10
    features:
      alerts: true
      custom_domains: true
      sla: false
  team:
    display_name: "Team"
    price_monthly_cents: 19900
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: -1
      postgres_connections: -1
      redis_memory_mb: -1
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: -1
      vault_max_entries: -1
      vault_envs_allowed: []
      deployments_apps: -1
    features:
      alerts: true
      custom_domains: true
      sla: true
  # team_yearly mirrors team exactly. $1990/yr ≈ $165.83/mo (~17% off $199 x 12).
  team_yearly:
    display_name: "Team (yearly)"
    price_monthly_cents: 199000
    billing_period: "yearly"
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: -1
      postgres_connections: -1
      redis_memory_mb: -1
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: -1
      vault_max_entries: -1
      vault_envs_allowed: []
      deployments_apps: -1
    features:
      alerts: true
      custom_domains: true
      sla: true
  growth:
    display_name: "Growth"
    price_monthly_cents: 9900
    trial_days: 0
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 5120
      postgres_connections: 20
      redis_memory_mb: 256
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: 10
      vault_max_entries: 200
      vault_envs_allowed: []
      deployments_apps: 5
    features:
      alerts: true
      custom_domains: true
      sla: false
      dedicated: true
promotions: []
`
