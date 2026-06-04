package analyticsevent

import (
	"context"
	"sync"
	"testing"
)

// recorder is a test Emitter that captures everything it sees AFTER the wrapper
// sanitized it, so tests can assert the wrapper applied Sanitize.
type recorder struct {
	mu       sync.Mutex
	events   []capturedEvent
	panicOn  string // eventType to panic on (to exercise fail-open)
	closed   bool
	closeErr error
}

type capturedEvent struct {
	eventType string
	attrs     map[string]any
}

func (r *recorder) Record(_ context.Context, eventType string, attrs map[string]any) {
	if eventType == r.panicOn {
		panic("backend blew up")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, capturedEvent{eventType, attrs})
}
func (r *recorder) Name() string { return "recorder" }
func (r *recorder) Close() error {
	r.closed = true
	return r.closeErr
}
func (r *recorder) last() capturedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return capturedEvent{}
	}
	return r.events[len(r.events)-1]
}
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func TestWrap_SanitizesBeforeBackend(t *testing.T) {
	rec := &recorder{}
	e := Wrap(rec)
	e.Record(context.Background(), EventFunnel, map[string]any{
		AttrEmail:  "secret@user.com",
		"password": "nope",
		AttrTier:   "pro",
	})
	got := rec.last()
	if got.eventType != EventFunnel {
		t.Fatalf("eventType = %q, want %q", got.eventType, EventFunnel)
	}
	if _, ok := got.attrs[AttrEmail]; ok {
		t.Error("wrapper did not strip raw email before backend")
	}
	if _, ok := got.attrs["password"]; ok {
		t.Error("wrapper did not strip non-allowlisted key before backend")
	}
	if got.attrs[AttrEmailHash] != HashEmail("secret@user.com") {
		t.Error("wrapper did not hash email before backend")
	}
	if got.attrs[AttrTier] != "pro" {
		t.Error("wrapper dropped an allowlisted key")
	}
}

func TestWrap_FailOpenOnPanic(t *testing.T) {
	rec := &recorder{panicOn: EventFunnel}
	e := Wrap(rec)
	// Must NOT panic into the caller.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wrapper let backend panic escape: %v", r)
		}
	}()
	e.Record(context.Background(), EventFunnel, nil)
	// A subsequent non-panicking event still works (wrapper isn't poisoned).
	e.Record(context.Background(), EventChurnSignal, nil)
	if rec.count() != 1 {
		t.Fatalf("expected 1 captured (the non-panicking) event, got %d", rec.count())
	}
}

func TestWrap_DropsEmptyEventType(t *testing.T) {
	rec := &recorder{}
	Wrap(rec).Record(context.Background(), "", map[string]any{AttrTier: "pro"})
	if rec.count() != 0 {
		t.Fatalf("empty eventType should be dropped, got %d events", rec.count())
	}
}

func TestWrap_NilBackendIsNoop(t *testing.T) {
	e := Wrap(nil)
	if e.Name() != BackendNoop {
		t.Fatalf("Wrap(nil).Name() = %q, want %q", e.Name(), BackendNoop)
	}
	// Must not panic.
	e.Record(context.Background(), EventFunnel, map[string]any{AttrTier: "pro"})
	if err := e.Close(); err != nil {
		t.Fatalf("Wrap(nil).Close() = %v, want nil", err)
	}
}

func TestWrap_Idempotent(t *testing.T) {
	rec := &recorder{}
	once := Wrap(rec)
	twice := Wrap(once)
	if once != twice {
		t.Fatal("Wrap is not idempotent: Wrap(Wrap(e)) != Wrap(e)")
	}
}

func TestWrap_CloseDelegatesAndRecoversPanic(t *testing.T) {
	rec := &recorder{}
	if err := Wrap(rec).Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	if !rec.closed {
		t.Fatal("Close did not delegate to backend")
	}

	// A panicking Close is swallowed (returns nil).
	pc := &panicCloser{}
	if err := Wrap(pc).Close(); err != nil {
		t.Fatalf("panicking Close should be swallowed to nil, got %v", err)
	}
}

type panicCloser struct{ recorder }

func (*panicCloser) Close() error { panic("close blew up") }

func TestWrap_NameTransparent(t *testing.T) {
	if Wrap(&recorder{}).Name() != "recorder" {
		t.Fatal("wrapper should be transparent for Name()")
	}
}
