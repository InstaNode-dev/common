// Package analyticsevent defines the backend-agnostic interface for emitting
// product / behavioral custom events from instanode.dev's backend services
// (api, worker, provisioner) to an analytics sink.
//
// # Why this package exists
//
// As of the WS4 observability work, `grep RecordCustomEvent api/ worker/`
// returned ZERO hits: every business signal lived only as Prometheus counters,
// which cannot be keyed on a stable entity (team_id / anon_fingerprint / cohort)
// and so cannot answer the core funnel KPIs anon->claimed (target >2%) and
// claimed->paid (target >20%), nor record the synthetic flow-test matrix
// (the InstantFlowTest event). This package is the FOUNDATION (plan F1 /
// WS4-P1): the single backend->New Relic custom-event bridge every emit site
// will route through.
//
// It is shaped exactly like `common/storageprovider`, `common/queueprovider`,
// and `common/featureflag`: one interface ([Emitter]), multiple swappable
// backends selected by a factory keyed on an env var (ANALYTICS_BACKEND), with
// the concrete-backend dependency tree quarantined in a subpackage so consumers
// that never emit don't pull the New Relic SDK into their import graph.
//
//   - "noop"     (default) — drops every event. Zero deps, zero network. The
//     safe default when the New Relic insert path is not configured (local dev,
//     CI, a pod without the secret mounted). NEVER errors.
//   - "newrelic"           — wraps an already-constructed *newrelic.Application
//     and calls Application.RecordCustomEvent. Lives in the analyticsevent/nr
//     subpackage so the NR Go-agent dep stays out of noop-only builds. Wire it
//     by constructing nr.New(app) and passing it as Config.Override, OR by
//     registering its builder (see the nr subpackage's init()).
//
// # FAIL OPEN — the deliberate inverse of common/featureflag
//
// featureflag fails CLOSED: an unbuilt feature must default OFF, so any error
// collapses to the caller's default. Analytics is the EXACT OPPOSITE. An event
// emit is best-effort telemetry that MUST NEVER block, slow, or error a request
// path. Every [Emitter.Record] call:
//
//   - never returns an error to the caller (the method has no error return);
//   - swallows every sink error / panic and (in real backends) increments an
//     emit-failure counter rather than propagating;
//   - drops the event silently under backpressure rather than blocking.
//
// Losing an analytics event is acceptable; failing a customer's provision
// because the analytics sink hiccuped is not. The [Wrap] decorator enforces the
// no-panic half of this guarantee for every backend.
//
// # PII policy — enforced in this package
//
// Raw email addresses, auth tokens, and connection strings MUST NEVER leave the
// process as analytics attributes. [Sanitize] applies an explicit ALLOWLIST:
// only keys in [AllowedAttributes] survive, every other key is dropped. Email,
// when present under the well-known [AttrEmail] key, is hashed via [HashEmail]
// (sha256(lower(trim(email)))[:16]) and re-emitted under [AttrEmailHash] — the
// raw value never appears. Callers route every attribute map through Sanitize
// (the Wrap decorator does this automatically) so a new emit site cannot leak
// PII by passing the wrong key.
//
// Lives in `common` so api + worker + provisioner share one interface, one
// fail-open guarantee, and one PII allowlist.
package analyticsevent

import (
	"context"
	"errors"
)

// Emitter records product / behavioral custom events to an analytics backend.
//
// Record is fire-and-forget by contract: it has NO error return and MUST NOT
// block the caller's request path. Implementations buffer/drop on backpressure
// and swallow sink errors (incrementing a failure metric instead). The factory
// returns every backend already wrapped by [Wrap], which guarantees no emit can
// panic into the caller and that every attribute map is PII-sanitized first.
//
// All methods are safe for concurrent use across goroutines.
type Emitter interface {
	// Record emits one custom event of the given eventType with attrs. attrs is
	// PII-sanitized (allowlist + email-hash) by the wrapper before the concrete
	// backend sees it. A nil or empty attrs map is valid. Never blocks, never
	// errors, never panics into the caller.
	Record(ctx context.Context, eventType string, attrs map[string]any)

	// Name returns a stable identifier ("noop", "newrelic"). Used in logs and
	// metrics labels.
	Name() string

	// Close flushes any buffered events and releases backend resources.
	// Idempotent and safe on a never-used emitter. The noop backend's Close is a
	// no-op. Never panics.
	Close() error
}

// Config is the operator-facing configuration for the analytics backend. The
// api / worker / provisioner wire this from env vars (ANALYTICS_BACKEND) and
// pass it to Factory() at boot.
type Config struct {
	// Backend selects the implementation. One of "noop", "newrelic". Aliases
	// ("off"/"disabled"/"none"/"" -> "noop"; "nr"/"new-relic"/"newrelic" ->
	// "newrelic"). Empty defaults to "noop" — the dependency-free, never-erroring
	// backend that is safe when the analytics sink is not configured.
	Backend string

	// Override, when non-nil, is returned (wrapped) by Factory verbatim,
	// bypassing backend selection. This is how a service wires the New Relic sink
	// without the root package importing the NR Go agent: the service constructs
	// analyticsevent/nr.New(app) itself and passes it here. nil = use Backend.
	Override Emitter
}

// Canonical backend identifiers. These are the strings every layer (api,
// worker, provisioner, k8s ConfigMaps) compares against.
const (
	// BackendNoop drops every event. Default; zero deps; never errors.
	BackendNoop = "noop"

	// BackendNewRelic emits via Application.RecordCustomEvent. Its builder lives
	// in the analyticsevent/nr subpackage so the NR Go-agent dep stays out of
	// builds that only use noop.
	BackendNewRelic = "newrelic"
)

// ErrUnknownBackend is returned by Factory (as a non-blocking ADVISORY) when
// ANALYTICS_BACKEND is set to a value that matches no registered backend.
// Because analytics fails open, Factory still returns a usable (noop) Emitter
// alongside this error — it never leaves the caller without an emitter.
var ErrUnknownBackend = errors.New("analyticsevent: unknown backend (valid: noop, newrelic)")
