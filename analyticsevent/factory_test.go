package analyticsevent

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeBackend(t *testing.T) {
	cases := map[string]string{
		"":           BackendNoop,
		"noop":       BackendNoop,
		"OFF":        BackendNoop,
		" disabled ": BackendNoop,
		"none":       BackendNoop,
		"newrelic":   BackendNewRelic,
		"New-Relic":  BackendNewRelic,
		"NR":         BackendNewRelic,
		"insights":   BackendNewRelic,
		"bogus":      "",
		"clickhouse": "",
	}
	for in, want := range cases {
		if got := NormalizeBackend(in); got != want {
			t.Errorf("NormalizeBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFactory_DefaultIsNoop(t *testing.T) {
	e, err := Factory(Config{})
	if err != nil {
		t.Fatalf("default Factory err = %v, want nil", err)
	}
	if e.Name() != BackendNoop {
		t.Fatalf("default backend = %q, want %q", e.Name(), BackendNoop)
	}
}

func TestFactory_NoopExplicit(t *testing.T) {
	e, err := Factory(Config{Backend: "noop"})
	if err != nil || e.Name() != BackendNoop {
		t.Fatalf("noop: got (%q, %v)", e.Name(), err)
	}
}

func TestFactory_UnknownDegradesToNoopWithAdvisory(t *testing.T) {
	e, err := Factory(Config{Backend: "warehouse"})
	if !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("err = %v, want ErrUnknownBackend", err)
	}
	if e == nil || e.Name() != BackendNoop {
		t.Fatalf("must still return a usable noop emitter, got %v", e)
	}
	// Advisory only — the emitter is fully usable.
	e.Record(context.Background(), EventFunnel, nil)
}

func TestFactory_NewRelicWithoutAppDegradesToNoop(t *testing.T) {
	// ANALYTICS_BACKEND=newrelic but no Override and no registered builder:
	// degrade to noop with an advisory error (never fail the service).
	e, err := Factory(Config{Backend: "newrelic"})
	if err == nil {
		t.Fatal("expected advisory error for newrelic-without-app")
	}
	if e.Name() != BackendNoop {
		t.Fatalf("expected degrade to noop, got %q", e.Name())
	}
}

func TestFactory_OverrideWins(t *testing.T) {
	rec := &recorder{}
	e, err := Factory(Config{Backend: "noop", Override: rec})
	if err != nil {
		t.Fatalf("override err = %v", err)
	}
	// Override should be used (wrapped), so emitting reaches the recorder.
	e.Record(context.Background(), EventFunnel, map[string]any{AttrTier: "pro"})
	if rec.count() != 1 {
		t.Fatalf("override emitter not used: %d events", rec.count())
	}
	// And it is wrapped (sanitizing): a raw email would be hashed.
	e.Record(context.Background(), EventFunnel, map[string]any{AttrEmail: "a@b.com"})
	if _, ok := rec.last().attrs[AttrEmail]; ok {
		t.Fatal("override emitter was not wrapped (raw email leaked)")
	}
}

func TestFactory_RegisteredBuilder(t *testing.T) {
	rec := &recorder{}
	Register(BackendNewRelic, func(Config) (Emitter, error) { return rec, nil })
	t.Cleanup(func() { delete(builders, BackendNewRelic) })

	e, err := Factory(Config{Backend: "newrelic"})
	if err != nil {
		t.Fatalf("registered builder err = %v", err)
	}
	e.Record(context.Background(), EventFunnel, nil)
	if rec.count() != 1 {
		t.Fatalf("registered builder emitter not used")
	}
}

func TestFactory_BuilderErrorDegradesToNoop(t *testing.T) {
	Register(BackendNewRelic, func(Config) (Emitter, error) { return nil, errors.New("boom") })
	t.Cleanup(func() { delete(builders, BackendNewRelic) })

	e, err := Factory(Config{Backend: "newrelic"})
	if err == nil {
		t.Fatal("expected advisory error on builder failure")
	}
	if e.Name() != BackendNoop {
		t.Fatalf("expected degrade to noop, got %q", e.Name())
	}
}

func TestFactory_BuilderNilEmitterDegradesToNoop(t *testing.T) {
	Register(BackendNewRelic, func(Config) (Emitter, error) { return nil, nil })
	t.Cleanup(func() { delete(builders, BackendNewRelic) })

	e, err := Factory(Config{Backend: "newrelic"})
	if err == nil || e.Name() != BackendNoop {
		t.Fatalf("nil emitter from builder should degrade to noop with advisory, got (%q,%v)", e.Name(), err)
	}
}

func TestRegisterAndListRegistered(t *testing.T) {
	if len(ListRegistered()) != 0 {
		t.Fatalf("registry should start empty, got %v", ListRegistered())
	}
	Register("nr", func(Config) (Emitter, error) { return &recorder{}, nil })
	t.Cleanup(func() { delete(builders, BackendNewRelic) })
	got := ListRegistered()
	if len(got) != 1 || got[0] != BackendNewRelic {
		t.Fatalf("ListRegistered = %v, want [%q]", got, BackendNewRelic)
	}
}
