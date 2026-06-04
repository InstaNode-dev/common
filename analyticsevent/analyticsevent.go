package analyticsevent

import "context"

// Wrap decorates a concrete [Emitter] with the package's two non-negotiable
// guarantees:
//
//  1. FAIL OPEN — a panic inside the backend's Record (or Close) is recovered
//     and swallowed so an analytics hiccup can NEVER crash or error a caller's
//     request path. Record has no error return, so swallowing is the contract.
//  2. PII — every attribute map is run through [Sanitize] (allowlist + email
//     hashing) BEFORE the concrete backend sees it, so no backend can emit a
//     raw email / token / connection string even if an emit site passed one.
//
// This is the single chokepoint that makes the package contract true. Factory
// always returns a wrapped emitter, so no call site holds a raw backend.
//
// Wrapping is idempotent: Wrap(Wrap(e)) behaves identically to Wrap(e). A nil
// backend wraps to the no-op emitter (the most fail-open emitter possible).
func Wrap(e Emitter) Emitter {
	if e == nil {
		return wrapped{inner: NewNoop()}
	}
	if w, already := e.(wrapped); already {
		return w
	}
	return wrapped{inner: e}
}

// wrapped is the fail-open + PII-sanitizing decorator. Unexported; construct via
// [Wrap]. Value receiver (it holds only an interface) so it is cheap to copy and
// the idempotency type-assertion in Wrap is straightforward.
type wrapped struct {
	inner Emitter
}

// Record sanitizes attrs (allowlist + email-hash) then forwards to the backend,
// recovering and swallowing any panic so the caller's path is never affected.
func (w wrapped) Record(ctx context.Context, eventType string, attrs map[string]any) {
	defer func() { _ = recover() }() // fail open: analytics must never panic upward
	if eventType == "" {
		return // a typeless event is meaningless and un-queryable; drop it
	}
	w.inner.Record(ctx, eventType, Sanitize(attrs))
}

// Name reports the wrapped backend's name (the wrapper is transparent).
func (w wrapped) Name() string { return w.inner.Name() }

// Close delegates to the backend, swallowing any panic so teardown of one
// service never crashes on a misbehaving emitter.
func (w wrapped) Close() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = nil // fail open: teardown is best-effort, never panic upward
		}
	}()
	return w.inner.Close()
}
