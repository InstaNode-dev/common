package featureflag

import "context"

// Wrap decorates a concrete [Provider] with the package's non-negotiable
// fail-closed guarantee: every evaluation returns the caller-supplied default
// on ANY failure mode — backend down, flag missing, sync failure, type
// mismatch, a nil/cancelled context, a nil EvalContext, or even a panic inside
// the underlying provider.
//
// This is the single chokepoint that makes the package contract true. Concrete
// backends are free to return (value, err); the wrapper is what collapses that
// pair to a single fail-closed value. The factory always returns a wrapped
// provider, so no call site ever holds a raw backend.
//
// Wrapping is idempotent: Wrap(Wrap(p)) behaves identically to Wrap(p).
func Wrap(p Provider) Provider {
	if p == nil {
		// A nil backend is itself a failure mode: hand back a provider that
		// fails closed on every call rather than panicking at the call site.
		return failClosed{}
	}
	if _, already := p.(*wrapped); already {
		return p
	}
	return &wrapped{inner: p}
}

// wrapped is the fail-closed decorator. It is unexported; construct via [Wrap].
type wrapped struct {
	inner Provider
}

// Name reports the wrapped backend's name (the wrapper is transparent).
func (w *wrapped) Name() string { return w.inner.Name() }

// Close delegates to the wrapped backend, swallowing any panic so teardown of
// one service never crashes on a misbehaving provider.
func (w *wrapped) Close() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = nil // fail closed: teardown best-effort, never panic upward
		}
	}()
	return w.inner.Close()
}

// BoolVariant returns the resolved value only on a clean (nil-error,
// no-panic) evaluation; otherwise it returns def. The returned error is
// preserved so callers who DO want to observe/metricise failures can — but the
// VALUE is always fail-closed (def) whenever err != nil.
func (w *wrapped) BoolVariant(ctx context.Context, key string, def bool, evalCtx EvalContext) (val bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			val, err = def, errPanic(r)
		}
	}()
	if ctx == nil {
		return def, context.Canceled
	}
	if err = ctx.Err(); err != nil {
		return def, err
	}
	got, gErr := w.inner.BoolVariant(ctx, key, def, evalCtx)
	if gErr != nil {
		return def, gErr
	}
	return got, nil
}

// StringVariant mirrors BoolVariant's fail-closed contract for string flags.
func (w *wrapped) StringVariant(ctx context.Context, key, def string, evalCtx EvalContext) (val string, err error) {
	defer func() {
		if r := recover(); r != nil {
			val, err = def, errPanic(r)
		}
	}()
	if ctx == nil {
		return def, context.Canceled
	}
	if err = ctx.Err(); err != nil {
		return def, err
	}
	got, gErr := w.inner.StringVariant(ctx, key, def, evalCtx)
	if gErr != nil {
		return def, gErr
	}
	return got, nil
}

// IntVariant mirrors BoolVariant's fail-closed contract for integer flags.
func (w *wrapped) IntVariant(ctx context.Context, key string, def int64, evalCtx EvalContext) (val int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			val, err = def, errPanic(r)
		}
	}()
	if ctx == nil {
		return def, context.Canceled
	}
	if err = ctx.Err(); err != nil {
		return def, err
	}
	got, gErr := w.inner.IntVariant(ctx, key, def, evalCtx)
	if gErr != nil {
		return def, gErr
	}
	return got, nil
}

// failClosed is the provider returned when Wrap is given a nil backend (the
// factory's last-resort path). Every evaluation returns the caller default; it
// is the most fail-closed provider possible.
type failClosed struct{}

func (failClosed) Name() string  { return "fail-closed" }
func (failClosed) Close() error  { return nil }
func (failClosed) BoolVariant(_ context.Context, _ string, def bool, _ EvalContext) (bool, error) {
	return def, ErrFlagNotFound
}
func (failClosed) StringVariant(_ context.Context, _ string, def string, _ EvalContext) (string, error) {
	return def, ErrFlagNotFound
}
func (failClosed) IntVariant(_ context.Context, _ string, def int64, _ EvalContext) (int64, error) {
	return def, ErrFlagNotFound
}

// BoolEnabled is the ergonomic entry point most call sites use: it discards the
// error and returns just the fail-closed bool. Equivalent to calling
// BoolVariant on a wrapped provider and ignoring err. Provided as a free
// function so a non-wrapped provider passed in still gets the fail-closed
// guarantee.
//
// Example:
//
//	if flags.Enabled(ctx, "feature_team_billing", false, ec) { ... }
func BoolEnabled(ctx context.Context, p Provider, key string, def bool, evalCtx EvalContext) bool {
	v, _ := Wrap(p).BoolVariant(ctx, key, def, evalCtx)
	return v
}
