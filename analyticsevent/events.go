package analyticsevent

import (
	"context"
	"time"
)

// Well-known custom event types. Every emit site MUST use one of these
// constants, never an inline string literal (repo convention: no scattered
// strings; CLAUDE.md rule 16: a single source for each contract token). New
// Relic indexes events by eventType, so a typo creates a silent second event
// table that no dashboard queries.
const (
	// EventFunnel is the conversion-funnel event emitted at each step of the
	// acquisition journey (landing -> provision -> claim -> paid). Faceted by
	// AttrFunnelStep + AttrActor to compute anon->claimed and claimed->paid.
	EventFunnel = "InstantFunnel"

	// EventFlowTest is one synthetic flow-test result per UI/API flow per tick,
	// pushed by the worker's synthetic prober. Carries cohort="synthetic" so
	// every other dashboard can exclude this traffic from real KPIs.
	EventFlowTest = "InstantFlowTest"

	// EventPaymentProbe is one Layer-3 payment-prober leg result per tick,
	// pushed by the worker's payment_probe synthetic. Carries cohort="synthetic"
	// so business/billing dashboards exclude it from real revenue KPIs. It is
	// the money-path heartbeat companion to EventFlowTest (one event per probe
	// leg: checkout_reachable / billing_state / invoices_reachable /
	// webhook_security / upgrade_webhook_e2e).
	EventPaymentProbe = "InstantPaymentProbe"

	// EventChurnSignal flags a behavioral churn indicator on a paid team (e.g.
	// no activity in N days). Feeds the churn-trend tile (WS4-P5).
	EventChurnSignal = "InstantChurnSignal"

	// EventAbuseSignal flags an abuse indicator (fingerprint dedup-cap hit,
	// quota burn, recycle-seen spike). Feeds the abuse behavioral tile (WS4-P6).
	EventAbuseSignal = "InstantAbuseSignal"
)

// Canonical funnel-step values for the AttrFunnelStep attribute on
// [EventFunnel]. Low-cardinality and stable so NRQL FACETs stay clean.
const (
	FunnelStepLanding   = "landing"   // onboarding URL first hit
	FunnelStepProvision = "provision" // a resource was provisioned
	FunnelStepClaim     = "claim"     // anonymous -> claimed (account created)
	FunnelStepPaid      = "paid"      // claimed -> paid (subscription active)
)

// Canonical actor classes for the AttrActor attribute (mirrors plan F2's
// actor-classification middleware). Low cardinality (safe NR attribute).
const (
	ActorAgentClaude    = "agent_claude"
	ActorAgentMCP       = "agent_mcp"
	ActorAgentCurl      = "agent_curl"
	ActorAgentOther     = "agent_other"
	ActorHumanDashboard = "human_dashboard"
	ActorProber         = "prober"
	ActorUnknown        = "unknown"
)

// Canonical flow-test result values for the AttrResult attribute on
// [EventFlowTest].
const (
	ResultPass = "pass"
	ResultFail = "fail"
	ResultSkip = "skip"
)

// CohortSynthetic is the AttrCohort value every synthetic flow-test event
// carries so real-traffic dashboards can exclude it (WHERE cohort != 'synthetic').
const CohortSynthetic = "synthetic"

// Canonical attribute keys. Emit sites and dashboards (NRQL FACET / WHERE)
// MUST use these exact strings. Every key here that is non-PII is also in
// [AllowedAttributes]; AttrEmail is the ONE PII key and is hashed (never
// emitted raw) by [Sanitize].
const (
	// Identity / segmentation (non-PII; allowlisted).
	AttrActor         = "actor"         // agent_* / human_* / prober / unknown
	AttrTier          = "tier"          // anonymous, free, hobby, pro, ...
	AttrEnv           = "env"           // development, production, ...
	AttrCohort        = "cohort"        // "synthetic" or "" for real traffic
	AttrTeamID        = "teamId"        // team UUID (an opaque id, not PII)
	AttrResourceToken = "resourceToken" // resource token UUID (opaque id)
	AttrFingerprint   = "fingerprint"   // SHA256(/24+ASN) bucket hash (already hashed)
	AttrCommitID      = "commitId"      // deploy SHA, ties failures to a deploy
	AttrServiceName   = "serviceName"   // emitting service: api / worker / provisioner

	// Funnel (non-PII; allowlisted).
	AttrFunnelStep = "funnelStep" // landing / provision / claim / paid

	// Flow-test (non-PII; allowlisted).
	AttrFlow           = "flow"           // db_new, cache_new, deploy_new, ...
	AttrLayer          = "layer"          // api / ui / e2e
	AttrResult         = "result"         // pass / fail / skip
	AttrReason         = "reason"         // short failure reason (free text, no PII)
	AttrLatencyMs      = "latencyMs"      // observed latency in milliseconds
	AttrSyntheticRunID = "syntheticRunId" // groups all flows from one prober tick
	AttrHTTPStatus     = "httpStatus"     // HTTP status code observed on the probed endpoint (0 = no response)

	// Generic event metadata (non-PII; allowlisted).
	AttrService    = "service"    // free-form sub-service / handler name
	AttrReasonCode = "reasonCode" // enum-ish machine reason for churn/abuse signals

	// PII — NOT allowlisted as-is. Email under this key is HASHED by Sanitize
	// into AttrEmailHash; the raw value is dropped.
	AttrEmail = "email"

	// AttrEmailHash carries sha256(lower(trim(email)))[:16]. This IS allowlisted
	// — it is the only form an email may take in an event.
	AttrEmailHash = "emailHash"
)

// FlowTest is the typed payload for an [EventFlowTest] custom event, matching
// the synthetic-prober contract in TEST-ACCOUNTS-AND-NR-SYNTHETICS-PLAN.md §3.3.
// Use [Emitter] with [FlowTest.Attrs] (or the [RecordFlowTest] helper) instead
// of hand-building the attribute map at each call site.
type FlowTest struct {
	// Flow is the flow under test ("db_new", "cache_new", "deploy_new", ...).
	Flow string
	// Actor is the simulated caller class (one of the Actor* constants).
	Actor string
	// Tier is the plan tier the synthetic run exercised ("anonymous", "pro", ...).
	Tier string
	// Layer is which layer was probed ("api", "ui", "e2e").
	Layer string
	// Result is the outcome (one of the Result* constants).
	Result string
	// LatencyMs is the observed end-to-end latency in milliseconds.
	LatencyMs int64
	// Reason is a short, PII-free failure reason (empty on pass).
	Reason string
	// CommitID is the api /healthz commit_id at run time (ties a failure to the
	// deploy that caused it).
	CommitID string
	// SyntheticRunID groups every flow from one prober tick (UUID).
	SyntheticRunID string
}

// Attrs renders a [FlowTest] into the flat attribute map an [Emitter] consumes.
// Cohort is always [CohortSynthetic] for flow tests. Empty fields are omitted so
// an absent value reads as "missing" (not "") in NRQL. The result is already
// allowlist-clean (no PII keys) but still passes through [Sanitize] in Record.
func (f FlowTest) Attrs() map[string]any {
	out := make(map[string]any, 9)
	putStr(out, AttrFlow, f.Flow)
	putStr(out, AttrActor, f.Actor)
	putStr(out, AttrTier, f.Tier)
	putStr(out, AttrLayer, f.Layer)
	putStr(out, AttrResult, f.Result)
	putStr(out, AttrReason, f.Reason)
	putStr(out, AttrCommitID, f.CommitID)
	putStr(out, AttrSyntheticRunID, f.SyntheticRunID)
	out[AttrLatencyMs] = f.LatencyMs
	out[AttrCohort] = CohortSynthetic
	return out
}

// RecordFlowTest is the ergonomic typed helper for the synthetic prober: it
// builds the [EventFlowTest] attribute map from a [FlowTest] and records it.
// Fire-and-forget, same fail-open contract as [Emitter.Record].
func RecordFlowTest(ctx context.Context, e Emitter, f FlowTest) {
	if e == nil {
		return
	}
	e.Record(ctx, EventFlowTest, f.Attrs())
}

// PaymentProbe is the typed payload for an [EventPaymentProbe] custom event,
// matching the Layer-3 payment-prober contract (docs/ci/FORUM-PAYMENT-E2E-TOOLING.md
// §4 Layer 3). Use [Emitter] with [PaymentProbe.Attrs] (or the
// [RecordPaymentProbe] helper) instead of hand-building the map at each call site.
// One event per probe leg per tick.
type PaymentProbe struct {
	// Leg is the payment-funnel leg under test ("checkout_reachable",
	// "billing_state", "invoices_reachable", "webhook_security",
	// "upgrade_webhook_e2e"). Emitted under [AttrFlow] so the matrix grid keys on
	// the same attribute as flow tests.
	Leg string
	// Result is the outcome (pass / fail / "degraded").
	Result string
	// LatencyMs is the observed leg latency in milliseconds.
	LatencyMs int64
	// Reason is a short, PII-free outcome reason (empty on a clean pass).
	Reason string
	// HTTPStatus is the status code observed on the probed endpoint (0 = no
	// response, e.g. a config-skipped leg or a transport error).
	HTTPStatus int
	// SyntheticRunID groups every leg from one prober tick (UUID).
	SyntheticRunID string
}

// Attrs renders a [PaymentProbe] into the flat attribute map an [Emitter]
// consumes. Cohort is always [CohortSynthetic] so billing/revenue dashboards
// exclude probe traffic. The leg is emitted under [AttrFlow] (the same matrix
// axis flow tests use). Empty/zero fields are omitted so an absent value reads
// as "missing" in NRQL. Already allowlist-clean (no PII keys).
func (p PaymentProbe) Attrs() map[string]any {
	out := make(map[string]any, 6)
	putStr(out, AttrFlow, p.Leg)
	putStr(out, AttrResult, p.Result)
	putStr(out, AttrReason, p.Reason)
	putStr(out, AttrSyntheticRunID, p.SyntheticRunID)
	out[AttrLatencyMs] = p.LatencyMs
	if p.HTTPStatus != 0 {
		out[AttrHTTPStatus] = p.HTTPStatus
	}
	out[AttrCohort] = CohortSynthetic
	return out
}

// RecordPaymentProbe is the ergonomic typed helper for the Layer-3 payment
// prober: it builds the [EventPaymentProbe] attribute map from a [PaymentProbe]
// and records it. Fire-and-forget, same fail-open contract as [Emitter.Record]
// (a nil emitter is a no-op).
func RecordPaymentProbe(ctx context.Context, e Emitter, p PaymentProbe) {
	if e == nil {
		return
	}
	e.Record(ctx, EventPaymentProbe, p.Attrs())
}

// putStr sets out[key]=val only when val is non-empty, so callers can build a
// map without "" placeholders polluting NRQL facets.
func putStr(out map[string]any, key, val string) {
	if val != "" {
		out[key] = val
	}
}

// nowUnixMilli is a package var so tests can pin the clock if a future event
// needs a server-stamped timestamp. Unused by the current event set (NR stamps
// its own timestamp), retained as the single time source if one is added.
var nowUnixMilli = func() int64 { return time.Now().UnixMilli() }
