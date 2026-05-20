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

	// QueueCount is the maximum number of queue (NATS JetStream) resources a team
	// may have active simultaneously. -1 means unlimited; 0 means the tier cannot
	// provision queues at all (anonymous/free are already gated by fingerprint dedup).
	// Added A6 (P1 Wave-3): each queue creates a dedicated k8s namespace+pod, so
	// unbounded queue creation is an operational risk against the cluster.
	QueueCount int `yaml:"queue_count"`

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

	// BackupRetentionDays is how long the worker keeps Postgres backups for
	// resources in this tier. 0 means backups are not taken at all (anonymous,
	// free). Hobby = 7, hobby_plus = 14, Pro/Growth = 30, Team = 90.
	BackupRetentionDays int `yaml:"backup_retention_days"`

	// BackupRestoreEnabled gates POST /api/v1/resources/:id/restore. When
	// false the handler returns 402 upgrade_required with a sales nudge.
	// Hobby = false (sales lever); hobby_plus / Pro / Team = true.
	BackupRestoreEnabled bool `yaml:"backup_restore_enabled"`

	// ManualBackupsPerDay caps the number of ad-hoc backups a team can
	// trigger via POST /api/v1/resources/:id/backup per UTC day. 0 means
	// manual backups are not allowed.
	ManualBackupsPerDay int `yaml:"manual_backups_per_day"`

	// RPOMinutes — Recovery Point Objective. The maximum window of
	// data loss a tier accepts between the last completed backup and
	// a restore event. Surfaced on GET /api/v1/capabilities so an
	// agent can reason about whether a tier meets a workload's
	// durability requirements before provisioning. 0 means "RPO not
	// promised" (no scheduled backups on this tier). FIX-H #Q50 (B36).
	RPOMinutes int `yaml:"rpo_minutes"`

	// RTOMinutes — Recovery Time Objective. The target wall-clock
	// duration between "operator presses restore" and "data is back
	// online" for a tier. Includes the worker tick + pg_restore +
	// post-restore verification. 0 means "RTO not promised" (no
	// self-serve restore available on this tier). FIX-H #Q50 (B36).
	RTOMinutes int `yaml:"rto_minutes"`

	// VectorStorageMB is the maximum storage per pgvector-enabled Postgres
	// database in megabytes. Mirrors PostgresStorageMB because pgvector
	// runs on the same underlying Postgres backend.
	VectorStorageMB int `yaml:"vector_storage_mb"`
	// VectorConnections is the maximum concurrent connections per pgvector
	// database. Mirrors PostgresConnections.
	VectorConnections int `yaml:"vector_connections"`

	// CustomDomainsMax is the maximum number of custom domains a team may
	// bind across all their stacks. -1 means unlimited; 0 means the feature
	// is not available on this tier (paired with Features.CustomDomains=false).
	//
	// Introduced 2026-05-14 (FIX-G) to close the per-count gap: previously
	// the only gate on /api/v1/stacks/:slug/domains was the boolean
	// Features.CustomDomains flag, which let any Hobby Plus+ team add an
	// unbounded number of hostnames. The cap is enforced in
	// api/internal/handlers/custom_domain.go before the create-row write.
	// Tier ladder (mirrors plans.yaml):
	//
	//   anonymous / free / hobby      = 0  (feature off — boolean gate trips first)
	//   hobby_plus                    = 1  (first tier with the feature)
	//   growth                        = 3
	//   pro                           = 5
	//   team                          = 50 (effectively unlimited for dashboards)
	//
	// Keeping it in Limits (not Features) lets ops change the cap per tier
	// in plans.yaml without redeploying the handler.
	CustomDomainsMax int `yaml:"custom_domains_max"`
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
// service must be one of "postgres", "vector", "redis", "mongodb", "queue",
// "storage", "webhook". Returns -1 for unlimited. "vector" mirrors "postgres"
// because pgvector runs on the same underlying Postgres backend.
func (r *Registry) StorageLimitMB(tier, service string) int {
	p := r.Get(tier)
	switch service {
	case "postgres":
		return p.Limits.PostgresStorageMB
	case "vector":
		return p.Limits.VectorStorageMB
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
// Returns -1 for unlimited. "vector" mirrors "postgres" because pgvector runs
// on the same underlying Postgres backend.
func (r *Registry) ConnectionsLimit(tier, service string) int {
	p := r.Get(tier)
	switch service {
	case "postgres":
		return p.Limits.PostgresConnections
	case "vector":
		return p.Limits.VectorConnections
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

// CustomDomainsMaxLimit returns the maximum number of custom domains a team
// on the given tier may bind across their stacks. -1 means unlimited; 0
// means the feature is not enabled (CustomDomainsAllowed will also be false
// for that tier — the boolean gate trips first in the handler).
//
// Introduced alongside the Limits.CustomDomainsMax field (FIX-G). Callers
// should pair this with the boolean check:
//
//	if !r.CustomDomainsAllowed(tier) { return 402 upgrade_required }
//	if max := r.CustomDomainsMaxLimit(tier); max >= 0 && count >= max {
//	    return 402 limit_reached
//	}
func (r *Registry) CustomDomainsMaxLimit(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.CustomDomainsMax
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

// QueueCountLimit returns the maximum number of simultaneous active queue
// resources for the given tier. -1 means unlimited; 0 means the tier may not
// provision queues (the caller is expected to reject with 402 when 0 is returned
// and the team already has >= 0 queues — i.e. any queue is over-cap).
//
// When the plans.yaml entry is missing (older YAML without queue_count field),
// the struct zero-value 0 is returned. Callers that need to distinguish
// "truly unlimited" from "not configured" should treat 0 as the default-permit
// fallback; this method returns -1 for unlimited so callers can use the same
// `limit >= 0 && existing >= limit` pattern used by DeploymentsAppsLimit.
//
// Introduced A6 (P1 Wave-3): each queue provisions a dedicated k8s namespace
// and NATS pod, making unbounded queue creation an operational risk.
func (r *Registry) QueueCountLimit(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return -1 // unknown tier — fail open
	}
	// A zero value means the YAML field was absent (pre-A6 plans.yaml) — treat as
	// unlimited to avoid blocking existing customers on old configs. Once plans.yaml
	// has queue_count for all tiers, this zero-fallback is inert.
	if p.Limits.QueueCount == 0 {
		return -1
	}
	return p.Limits.QueueCount
}

// BackupRetentionDays returns how long the worker keeps Postgres backups for
// the given tier. 0 means no backups are taken.
func (r *Registry) BackupRetentionDays(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.BackupRetentionDays
}

// BackupRestoreEnabled reports whether the tier may self-serve restore from a
// backup. Hobby/free/anonymous get false (sales lever — restore is an upgrade
// hook). hobby_plus / Pro / Team return true.
func (r *Registry) BackupRestoreEnabled(tier string) bool {
	p := r.Get(tier)
	if p == nil {
		return false
	}
	return p.Limits.BackupRestoreEnabled
}

// ManualBackupsPerDay returns the per-team daily cap on POST /backup calls.
// 0 means manual backups are not allowed at all. -1 means unlimited.
func (r *Registry) ManualBackupsPerDay(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.ManualBackupsPerDay
}

// RPOMinutes returns the per-tier Recovery Point Objective in minutes.
// 0 = "not promised" (the tier doesn't take scheduled backups, so no
// RPO is guaranteed). Surfaced on GET /api/v1/capabilities. FIX-H #Q50.
func (r *Registry) RPOMinutes(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.RPOMinutes
}

// RTOMinutes returns the per-tier Recovery Time Objective in minutes.
// 0 = "not promised" (the tier doesn't have self-serve restore, so
// the time-to-restore is operator-driven and unbounded). Surfaced on
// GET /api/v1/capabilities. FIX-H #Q50.
func (r *Registry) RTOMinutes(tier string) int {
	p := r.Get(tier)
	if p == nil {
		return 0
	}
	return p.Limits.RTOMinutes
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
    limits:
      provisions_per_day: 5
      postgres_storage_mb: 10
      postgres_connections: 2
      vector_storage_mb: 10
      vector_connections: 2
      redis_memory_mb: 5
      redis_commands_per_day: 1000
      mongodb_storage_mb: 5
      mongodb_connections: 2
      mongodb_ops_per_minute: 100
      queue_storage_mb: 1024
      queue_count: -1
      storage_storage_mb: 10
      webhook_requests_stored: 100
      team_members: 1
      vault_max_entries: 0
      vault_envs_allowed: []
      deployments_apps: 0
      backup_retention_days: 0
      backup_restore_enabled: false
      manual_backups_per_day: 0
      rpo_minutes: 0
      rto_minutes: 0
      custom_domains_max: 0
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
    limits:
      provisions_per_day: 5
      postgres_storage_mb: 10
      postgres_connections: 2
      vector_storage_mb: 10
      vector_connections: 2
      redis_memory_mb: 5
      redis_commands_per_day: 1000
      mongodb_storage_mb: 5
      mongodb_connections: 2
      mongodb_ops_per_minute: 100
      queue_storage_mb: 1024
      queue_count: -1
      storage_storage_mb: 10
      webhook_requests_stored: 100
      team_members: 1
      vault_max_entries: 0
      vault_envs_allowed: []
      deployments_apps: 0
      backup_retention_days: 0
      backup_restore_enabled: false
      manual_backups_per_day: 0
      rpo_minutes: 0
      rto_minutes: 0
      custom_domains_max: 0
    features:
      alerts: false
      custom_domains: false
      sla: false
  hobby:
    display_name: "Hobby"
    price_monthly_cents: 900
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      vector_storage_mb: 500
      vector_connections: 5
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 100
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      queue_count: 3
      storage_storage_mb: 512
      webhook_requests_stored: 1000
      team_members: 1
      vault_max_entries: 20
      vault_envs_allowed: ["production"]
      deployments_apps: 1
      backup_retention_days: 7
      backup_restore_enabled: false
      manual_backups_per_day: 1
      rpo_minutes: 1440
      rto_minutes: 30
      custom_domains_max: 0
    features:
      alerts: true
      custom_domains: false
      sla: false
  # hobby_plus — $19/mo mid-step between Hobby ($9) and Pro ($49).
  # The W11 mid-tier insertion (2026-05-13). Research-backed pricing
  # decoy: triple-tier $9/$19/$49 lifts conversion ~22% vs $9/$49 by
  # anchoring against the middle price. Same limits as hobby plus:
  #   - 2 deployment apps (vs hobby's 1)
  #   - custom_domains: true (the first paid tier with this feature)
  #   - 5 GB object storage (vs hobby's 512 MB) — small bump
  #   - 50 vault entries with multi-env support (vs hobby's 20 prod-only)
  hobby_plus:
    display_name: "Hobby Plus"
    price_monthly_cents: 1900
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      vector_storage_mb: 1024
      vector_connections: 8
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 1024
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      queue_count: 5
      storage_storage_mb: 5120
      webhook_requests_stored: 5000
      team_members: 1
      vault_max_entries: 50
      # 2026-05-15: hobby_plus rolled back to production-only vault envs.
      # Multi-env is Pro+ — see multiEnvTierAllowed in stack.go.
      vault_envs_allowed: ["production"]
      deployments_apps: 2
      backup_retention_days: 14
      backup_restore_enabled: true
      manual_backups_per_day: 5
      rpo_minutes: 1440
      rto_minutes: 30
      custom_domains_max: 1
    features:
      alerts: true
      custom_domains: true
      sla: false
  # hobby_plus_yearly — annual variant of hobby_plus.
  # $199/yr ≈ $16.58/mo (~13% off). Discount sits between hobby's
  # "save 1 month" (~8%) and pro/team's "2 months free" (~17%) — the
  # mid-tier gets a mid-discount so the savings ladder reads:
  #   Hobby $9 → save 1 month / Hobby Plus $19 → save ~1.5 months /
  #   Pro $49 → save 2 months.
  hobby_plus_yearly:
    display_name: "Hobby Plus (yearly)"
    price_monthly_cents: 19900
    billing_period: "yearly"
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      vector_storage_mb: 1024
      vector_connections: 8
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 1024
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      queue_count: 5
      storage_storage_mb: 5120
      webhook_requests_stored: 5000
      team_members: 1
      vault_max_entries: 50
      # 2026-05-15: hobby_plus rolled back to production-only vault envs.
      # Multi-env is Pro+ — see multiEnvTierAllowed in stack.go.
      vault_envs_allowed: ["production"]
      deployments_apps: 2
      backup_retention_days: 14
      backup_restore_enabled: true
      manual_backups_per_day: 5
      rpo_minutes: 1440
      rto_minutes: 30
      custom_domains_max: 1
    features:
      alerts: true
      custom_domains: true
      sla: false
  # hobby_yearly mirrors hobby exactly — same limits + features. Only the
  # billing period and price differ ($90/yr = $7.50/mo — "save 2 months"
  # vs $9 x 12). Hobby Annual gets the same ~17% discount as Pro/Team
  # Annual (all "2 months free" = $X x 10). Locked by
  # TestHobbyYearlyPriceIsPinned + TestYearlyIsMonthlyTimesTen in
  # plans_test.go.
  # The webhook upgrades teams to the "hobby" tier regardless of which
  # cycle the user paid on; this variant exists only so the checkout
  # handler can pick the right Razorpay plan_id at subscribe time.
  hobby_yearly:
    display_name: "Hobby (yearly)"
    price_monthly_cents: 9000
    billing_period: "yearly"
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 1024
      postgres_connections: 8
      vector_storage_mb: 500
      vector_connections: 5
      redis_memory_mb: 50
      redis_commands_per_day: 10000
      mongodb_storage_mb: 100
      mongodb_connections: 5
      mongodb_ops_per_minute: 1000
      queue_storage_mb: 5120
      queue_count: 3
      storage_storage_mb: 512
      webhook_requests_stored: 1000
      team_members: 1
      vault_max_entries: 20
      vault_envs_allowed: ["production"]
      deployments_apps: 1
      backup_retention_days: 7
      backup_restore_enabled: false
      manual_backups_per_day: 1
      rpo_minutes: 1440
      rto_minutes: 30
      custom_domains_max: 0
    features:
      alerts: true
      custom_domains: false
      sla: false
  pro:
    display_name: "Pro"
    price_monthly_cents: 4900
    limits:
      provisions_per_day: -1
      # 2026-05-15 storage bump — keep in sync with api/plans.yaml.
      postgres_storage_mb: 10240
      postgres_connections: 20
      vector_storage_mb: 10240
      vector_connections: 20
      redis_memory_mb: 512
      redis_commands_per_day: 500000
      mongodb_storage_mb: 5120
      mongodb_connections: 20
      mongodb_ops_per_minute: 10000
      queue_storage_mb: 10240
      queue_count: 20
      storage_storage_mb: 51200
      webhook_requests_stored: 10000
      team_members: 5
      vault_max_entries: 200
      vault_envs_allowed: []
      deployments_apps: 10
      backup_retention_days: 30
      backup_restore_enabled: true
      manual_backups_per_day: 100
      rpo_minutes: 60
      rto_minutes: 15
      custom_domains_max: 5
    features:
      alerts: true
      custom_domains: true
      sla: false
  # pro_yearly mirrors pro exactly. $490/yr = $49 x 10 ("2 months free" vs $49 x 12).
  pro_yearly:
    display_name: "Pro (yearly)"
    price_monthly_cents: 49000
    billing_period: "yearly"
    limits:
      provisions_per_day: -1
      postgres_storage_mb: 10240
      postgres_connections: 20
      vector_storage_mb: 10240
      vector_connections: 20
      redis_memory_mb: 512
      redis_commands_per_day: 500000
      mongodb_storage_mb: 5120
      mongodb_connections: 20
      mongodb_ops_per_minute: 10000
      queue_storage_mb: 10240
      queue_count: 20
      storage_storage_mb: 51200
      webhook_requests_stored: 10000
      team_members: 5
      vault_max_entries: 200
      vault_envs_allowed: []
      deployments_apps: 10
      backup_retention_days: 30
      backup_restore_enabled: true
      manual_backups_per_day: 100
      rpo_minutes: 60
      rto_minutes: 15
      custom_domains_max: 5
    features:
      alerts: true
      custom_domains: true
      sla: false
  team:
    display_name: "Team"
    price_monthly_cents: 19900
    limits:
      provisions_per_day: -1
      postgres_storage_mb: -1
      postgres_connections: -1
      vector_storage_mb: -1
      vector_connections: -1
      redis_memory_mb: -1
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      queue_count: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: -1
      vault_max_entries: -1
      vault_envs_allowed: []
      deployments_apps: -1
      backup_retention_days: 90
      backup_restore_enabled: true
      manual_backups_per_day: 1000
      rpo_minutes: 60
      rto_minutes: 15
      custom_domains_max: 50
    features:
      alerts: true
      custom_domains: true
      sla: true
  # team_yearly mirrors team exactly. $1990/yr = $199 x 10 ("2 months free" vs $199 x 12).
  team_yearly:
    display_name: "Team (yearly)"
    price_monthly_cents: 199000
    billing_period: "yearly"
    limits:
      provisions_per_day: -1
      postgres_storage_mb: -1
      postgres_connections: -1
      vector_storage_mb: -1
      vector_connections: -1
      redis_memory_mb: -1
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      queue_count: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: -1
      vault_max_entries: -1
      vault_envs_allowed: []
      deployments_apps: -1
      backup_retention_days: 90
      backup_restore_enabled: true
      manual_backups_per_day: 1000
      rpo_minutes: 60
      rto_minutes: 15
      custom_domains_max: 50
    features:
      alerts: true
      custom_domains: true
      sla: true
  growth:
    display_name: "Growth"
    price_monthly_cents: 9900
    limits:
      provisions_per_day: -1
      # 2026-05-15: bumped to stay above Pro after Pro storage bump.
      postgres_storage_mb: 20480
      postgres_connections: 20
      vector_storage_mb: 20480
      vector_connections: 20
      redis_memory_mb: 1024
      redis_commands_per_day: -1
      mongodb_storage_mb: -1
      mongodb_connections: -1
      mongodb_ops_per_minute: -1
      queue_storage_mb: -1
      queue_count: -1
      storage_storage_mb: -1
      webhook_requests_stored: -1
      team_members: 10
      vault_max_entries: 200
      vault_envs_allowed: []
      # B6-P3 (BugBash 2026-05-20, wave-3 consolidated): bumped from 5 → 50.
      # Pro's deployments_apps = 10; the previous Growth value of 5 was a
      # tier-ladder inversion (Growth $99/mo < Pro $49/mo on a customer-
      # facing dimension). Kept synchronised with api/plans.yaml.
      deployments_apps: 50
      backup_retention_days: 30
      backup_restore_enabled: true
      manual_backups_per_day: 100
      rpo_minutes: 60
      rto_minutes: 15
      custom_domains_max: 3
    features:
      alerts: true
      custom_domains: true
      sla: false
      dedicated: true
promotions: []
`
