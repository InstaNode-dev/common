package analyticsevent

import "context"

// noop is the default [Emitter]: it drops every event. Zero dependencies, zero
// network, and — critically — it can NEVER error or panic, which makes it the
// safe default when the analytics sink is not configured (local dev, CI, a pod
// before the insert-key secret is mounted). Construct via [NewNoop].
type noop struct{}

// NewNoop returns the no-op [Emitter]. Unwrapped is fine — it already satisfies
// every fail-open guarantee — but Factory wraps it for uniformity (so even noop
// goes through Sanitize, keeping behavior identical to a real backend).
func NewNoop() Emitter { return noop{} }

// Record drops the event. No-op.
func (noop) Record(context.Context, string, map[string]any) {}

// Name returns the stable backend identifier.
func (noop) Name() string { return BackendNoop }

// Close is a no-op (nothing to flush or release).
func (noop) Close() error { return nil }
