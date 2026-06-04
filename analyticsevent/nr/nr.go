// Package nr is the New Relic concrete sink for common/analyticsevent: it wraps
// an already-constructed *newrelic.Application and emits custom events via
// Application.RecordCustomEvent (the Insights custom-event path NRQL FACETs over).
//
// It lives in a SUBPACKAGE so the New Relic Go-agent dependency stays out of the
// import graph of analyticsevent consumers that only use the noop backend — the
// same quarantine pattern as featureflag/static and queueprovider's backends.
//
// A service wires it WITHOUT the root analyticsevent package importing the NR
// agent: construct the app via the service's own internal/obs.InitNewRelic,
// build the sink with nr.New(app), and pass it as analyticsevent.Config.Override
// to analyticsevent.Factory. Factory wraps it so the fail-open + PII guarantees
// hold.
//
// Fail-open: RecordCustomEvent is nil-safe in the v3 SDK (a nil app is a silent
// no-op), and the root package's Wrap recovers any panic, so this sink can never
// block or error a request path.
package nr

import (
	"context"

	"github.com/newrelic/go-agent/v3/newrelic"

	"instant.dev/common/analyticsevent"
)

// FailureHook is invoked (best-effort) when an emit is dropped because the
// underlying *newrelic.Application is nil — i.e. NR was not configured. Services
// wire it to increment instant_analytics_emit_failed_total{reason} (the counter
// lives in each service's metrics package per CLAUDE.md rule 25; this is the seam
// that lets common stay metrics-library-agnostic). nil = no hook.
type FailureHook func(reason string)

// reason label values passed to a FailureHook.
const (
	// ReasonNilApp = the sink had no *newrelic.Application (NR not configured),
	// so the event was dropped. Distinct from a sink-side reject so the alert can
	// tell "misconfigured" from "NR rejected the payload".
	ReasonNilApp = "nil_app"
)

// sink is the concrete New Relic [analyticsevent.Emitter]. Unexported; construct
// via [New].
type sink struct {
	app    *newrelic.Application
	onFail FailureHook
}

// New returns a New Relic-backed [analyticsevent.Emitter] over app. A nil app is
// permitted (fail-open): every Record then drops the event and fires onFail with
// [ReasonNilApp] instead of erroring — the same contract as obs.InitNewRelic
// returning a nil application when the license key is unset.
//
// Pass the result as analyticsevent.Config.Override to analyticsevent.Factory so
// it is wrapped with the package's fail-open + PII-sanitizing guarantees.
func New(app *newrelic.Application, opts ...Option) analyticsevent.Emitter {
	s := &sink{app: app}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures the New Relic sink.
type Option func(*sink)

// WithFailureHook sets the [FailureHook] invoked when an event is dropped
// because NR is not configured. Wire it to your emit-failure counter.
func WithFailureHook(h FailureHook) Option {
	return func(s *sink) { s.onFail = h }
}

// Record emits one custom event via Application.RecordCustomEvent. attrs has
// already been PII-sanitized by analyticsevent.Wrap before reaching here. When
// the app is nil (NR not configured) the event is dropped and the failure hook
// (if any) fires — never an error, never a block.
func (s *sink) Record(_ context.Context, eventType string, attrs map[string]any) {
	if s.app == nil {
		if s.onFail != nil {
			s.onFail(ReasonNilApp)
		}
		return
	}
	// RecordCustomEvent wants map[string]interface{}; map[string]any is the same
	// underlying type in Go, so a direct conversion is allocation-free.
	s.app.RecordCustomEvent(eventType, map[string]interface{}(attrs))
}

// Name returns the stable backend identifier.
func (s *sink) Name() string { return analyticsevent.BackendNewRelic }

// Close is a no-op: the *newrelic.Application's lifecycle is owned by the
// service that constructed it (it calls app.Shutdown on its own teardown path),
// not by this sink.
func (s *sink) Close() error { return nil }
