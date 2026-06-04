// Package featureflag defines the backend-agnostic interface for evaluating
// feature flags across instanode.dev's services (api, worker, provisioner).
//
// # Why this package exists
//
// instanode.dev ships in-progress features to prod (the Team tier is the live
// example: code-complete but delivery-unproven) and needs to (a) keep every
// half-built feature OFF for real customers without a redeploy-per-toggle, and
// (b) turn a feature ON in prod for a cohort of internal "integration" teams so
// the real prod path can be exercised before GA. A flag system with targeting
// on a stable eval-context solves both: problem (a) is "flag default OFF",
// problem (b) is "flag ON for team_id in the integration cohort".
//
// This package abstracts flag evaluation the same way `common/storageprovider`
// abstracts object-storage credential issuance and `common/queueprovider`
// abstracts message-broker credential issuance: one interface, multiple
// implementations, a factory selected by an env var (FEATURE_FLAG_BACKEND).
// Today the wire backends are:
//
//   - "static" (default) — a pure in-memory provider that reads a flag
//     definition map / file. NO network. Tests and CI need no running flagd.
//   - "flagd"            — OpenFeature SDK + flagd provider (gRPC streaming),
//     so a flag flip propagates sub-second to every connected service.
//
// OpenFeature is the CNCF vendor-neutral standard; flagd is a tiny stateless
// Go container. Wrapping both behind this interface keeps the backend swappable
// — if we later need progressive rollout / experimentation, go-feature-flag is
// ALSO an OpenFeature provider and drops in without touching any call site.
//
// # FAIL CLOSED — the deliberate inverse of the repo's "fail open on Redis"
//
// CLAUDE.md rule 1 says rate-limit / quota checks fail OPEN so a Redis outage
// never blocks provisioning. Feature flags are the EXACT INVERSE: an unbuilt
// feature must default OFF. Every evaluation in this package returns the
// caller-supplied default on ANY error (provider down, flag missing, sync
// failure, type mismatch, nil/cancelled context). That guarantee is enforced
// at the [Provider] boundary by the [Wrap] wrapper so no concrete backend can
// leak a non-default value on error — see featureflag.go.
//
// Lives in `common` so api + worker + provisioner share the same interface and
// the same fail-closed guarantee.
package featureflag

import (
	"context"
	"errors"
)

// Provider evaluates feature flags against a backend. Implementations exist for
// the in-memory "static" backend (real, default) and "flagd" (OpenFeature +
// flagd gRPC streaming). The factory selects one at boot via Factory(cfg).
//
// IMPORTANT: callers should NOT use a raw concrete Provider directly. The
// factory returns every provider already wrapped by [Wrap], which enforces the
// fail-closed guarantee (return the caller default on any error or panic). A
// concrete provider's *Variant methods MAY return an error or an off-spec
// value; the wrapper is what makes the package-level contract "default on any
// failure" true.
//
// All methods are safe for concurrent use across goroutines.
type Provider interface {
	// BoolVariant resolves a boolean flag. It returns the resolved value and a
	// nil error on a clean hit, or (def, err) on any failure. The wrapper
	// collapses the (value, err) pair to a single bool, always preferring def
	// when err != nil. def is the caller-supplied fail-closed default.
	BoolVariant(ctx context.Context, key string, def bool, evalCtx EvalContext) (bool, error)

	// StringVariant resolves a string flag. Same (value, err) contract as
	// BoolVariant.
	StringVariant(ctx context.Context, key string, def string, evalCtx EvalContext) (string, error)

	// IntVariant resolves an integer flag. Same (value, err) contract as
	// BoolVariant.
	IntVariant(ctx context.Context, key string, def int64, evalCtx EvalContext) (int64, error)

	// Name returns a stable identifier ("static", "flagd"). Used in logs,
	// metrics labels, and audit events.
	Name() string

	// Close releases any backend resources (flagd holds a gRPC stream).
	// Idempotent and safe to call on a never-connected provider. The static
	// provider's Close is a no-op.
	Close() error
}

// EvalContext carries the targeting inputs for a single flag evaluation.
//
// TargetingKey is the stable identity the backend hashes for percentage
// rollouts and matches against allowlists — for instanode.dev this is the
// team_id (or, for anonymous flows, the resource token / fingerprint). The
// remaining fields are the attributes flagd targeting rules can match on
// (`tier`, `env`, ...). Attributes carries any additional ad-hoc keys.
//
// The zero value (empty TargetingKey, nil Attributes) is valid and represents
// "no targeting information" — every backend MUST treat it as a non-matching
// context and therefore return the flag's default variant (which, for an
// unbuilt feature, is OFF). The contract test asserts this explicitly.
type EvalContext struct {
	// TargetingKey is the stable per-subject key (team_id for authenticated
	// flows; resource token / fingerprint for anonymous). Empty = anonymous /
	// no stable identity.
	TargetingKey string

	// TeamID is the team UUID, surfaced as the "team_id" targeting attribute.
	// Usually equal to TargetingKey but kept separate so callers can target by
	// team while keying rollout buckets on something else (e.g. user_id).
	TeamID string

	// Tier is the resource/team plan tier (anonymous, free, hobby, pro, ...),
	// surfaced as the "tier" targeting attribute. Lets a flag target a whole
	// tier (e.g. "feature_team_billing" -> tier == team).
	Tier string

	// Env is the resolved environment (development, production, ...), surfaced
	// as the "env" targeting attribute. Lets a flag be ON in development and
	// OFF in production for the same team.
	Env string

	// Attributes carries any extra ad-hoc targeting keys not covered by the
	// typed fields above. Merged into the OpenFeature evaluation context; the
	// typed fields win on key collision.
	Attributes map[string]any
}

// AttributeMap renders the EvalContext into the flat attribute map the
// underlying backends consume (OpenFeature evaluation context attributes /
// static targeting-rule lookups). Typed fields are only emitted when non-empty
// so an absent attribute reads as "missing" (not "empty string") in a
// targeting rule. Typed fields take precedence over Attributes on collision.
func (e EvalContext) AttributeMap() map[string]any {
	out := make(map[string]any, len(e.Attributes)+3)
	for k, v := range e.Attributes {
		out[k] = v
	}
	if e.TeamID != "" {
		out[AttrTeamID] = e.TeamID
	}
	if e.Tier != "" {
		out[AttrTier] = e.Tier
	}
	if e.Env != "" {
		out[AttrEnv] = e.Env
	}
	return out
}

// Canonical targeting-attribute keys. Flag definitions (flags.json targeting
// rules, static allowlists) MUST use these exact strings. Extracted as named
// constants per the repo convention (no scattered string literals).
const (
	// AttrTeamID is the targeting attribute carrying EvalContext.TeamID.
	AttrTeamID = "team_id"
	// AttrTier is the targeting attribute carrying EvalContext.Tier.
	AttrTier = "tier"
	// AttrEnv is the targeting attribute carrying EvalContext.Env.
	AttrEnv = "env"
)

// ErrUnknownBackend is returned by Factory when FEATURE_FLAG_BACKEND is set to
// a value that does not match any registered backend.
var ErrUnknownBackend = errors.New("featureflag: unknown backend (valid: static, flagd)")

// ErrFlagNotFound is returned by a backend when the requested flag key is not
// present in the flag source. Callers never see this directly — the wrapper
// collapses it to the caller-supplied default (fail closed) — but it is
// exported so backends share one sentinel and tests can assert on it.
var ErrFlagNotFound = errors.New("featureflag: flag not found")

// ErrTypeMismatch is returned by a backend when a flag exists but its variant
// value is not the type the caller requested (e.g. BoolVariant on a string
// flag). Collapsed to the default by the wrapper (fail closed).
var ErrTypeMismatch = errors.New("featureflag: flag type mismatch")
