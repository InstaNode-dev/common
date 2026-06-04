package analyticsevent

import (
	"context"
	"testing"
)

func TestNoop(t *testing.T) {
	e := NewNoop()
	if e.Name() != BackendNoop {
		t.Fatalf("Name = %q, want %q", e.Name(), BackendNoop)
	}
	// Records nothing, never errors, never panics — even with PII present.
	e.Record(context.Background(), EventFunnel, map[string]any{AttrEmail: "x@y.com"})
	e.Record(context.Background(), "", nil)
	if err := e.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}
