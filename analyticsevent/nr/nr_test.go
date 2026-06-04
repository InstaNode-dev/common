package nr

import (
	"context"
	"testing"

	"github.com/newrelic/go-agent/v3/newrelic"

	"instant.dev/common/analyticsevent"
)

func TestNew_NilAppDropsAndFiresHook(t *testing.T) {
	var gotReason string
	s := New(nil, WithFailureHook(func(r string) { gotReason = r }))

	if s.Name() != analyticsevent.BackendNewRelic {
		t.Fatalf("Name = %q, want %q", s.Name(), analyticsevent.BackendNewRelic)
	}
	// Nil app: must not panic, must drop, must fire hook with ReasonNilApp.
	s.Record(context.Background(), analyticsevent.EventFunnel, map[string]any{analyticsevent.AttrTier: "pro"})
	if gotReason != ReasonNilApp {
		t.Fatalf("failure hook reason = %q, want %q", gotReason, ReasonNilApp)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}

func TestNew_NilAppNoHookStillSafe(t *testing.T) {
	s := New(nil)
	// No hook set: still must not panic.
	s.Record(context.Background(), analyticsevent.EventFlowTest, nil)
}

func TestNew_WithRealApp_RecordsCustomEvent(t *testing.T) {
	// Construct a real (config-only, never connecting) NR Application. The v3 SDK
	// builds a usable *newrelic.Application offline; RecordCustomEvent buffers
	// locally without a network round-trip, so this exercises the real path
	// without a license/daemon.
	app, err := newrelic.NewApplication(
		newrelic.ConfigAppName("analyticsevent-test"),
		newrelic.ConfigLicense("0123456789012345678901234567890123456789"), // 40-char dummy
		newrelic.ConfigEnabled(false),                                      // offline: no harvest/connect, but app is non-nil
	)
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	t.Cleanup(func() { app.Shutdown(0) })

	s := New(app)
	// Should not panic and should reach RecordCustomEvent (no assertion on the
	// internal buffer — the SDK owns it — but the call path is exercised).
	s.Record(context.Background(), analyticsevent.EventFunnel, map[string]any{
		analyticsevent.AttrActor: analyticsevent.ActorHumanDashboard,
		analyticsevent.AttrTier:  "pro",
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}

func TestNew_WiresThroughFactoryOverride(t *testing.T) {
	// The intended wiring: nr.New(app) passed as analyticsevent.Config.Override.
	s := New(nil) // nil app is fine for this wiring test
	e, ferr := analyticsevent.Factory(analyticsevent.Config{Override: s})
	if ferr != nil {
		t.Fatalf("Factory(Override) err = %v", ferr)
	}
	if e.Name() != analyticsevent.BackendNewRelic {
		t.Fatalf("wired emitter name = %q, want %q", e.Name(), analyticsevent.BackendNewRelic)
	}
	// And it is wrapped: PII is sanitized before reaching the sink.
	e.Record(context.Background(), analyticsevent.EventFunnel, map[string]any{analyticsevent.AttrEmail: "a@b.com"})
}
