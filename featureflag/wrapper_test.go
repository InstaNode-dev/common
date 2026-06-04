package featureflag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/featureflag"
)

// fakeProvider is a configurable backend used to drive the wrapper through its
// every failure mode. It deliberately can return errors, off-spec values, or
// panic — proving the wrapper, not the backend, owns the fail-closed contract.
type fakeProvider struct {
	boolVal   bool
	strVal    string
	intVal    int64
	err       error
	panicWith any
	closeErr  error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Close() error {
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.closeErr
}
func (f *fakeProvider) BoolVariant(_ context.Context, _ string, _ bool, _ featureflag.EvalContext) (bool, error) {
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.boolVal, f.err
}
func (f *fakeProvider) StringVariant(_ context.Context, _ string, _ string, _ featureflag.EvalContext) (string, error) {
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.strVal, f.err
}
func (f *fakeProvider) IntVariant(_ context.Context, _ string, _ int64, _ featureflag.EvalContext) (int64, error) {
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.intVal, f.err
}

func TestWrap_NilBackendFailsClosed(t *testing.T) {
	p := featureflag.Wrap(nil)
	require.NotNil(t, p)
	assert.Equal(t, "fail-closed", p.Name())
	got, err := p.BoolVariant(context.Background(), "x", false, featureflag.EvalContext{})
	assert.False(t, got)
	assert.Error(t, err)
	gotT, _ := p.BoolVariant(context.Background(), "x", true, featureflag.EvalContext{})
	assert.True(t, gotT, "nil backend returns caller default, whatever it is")
	gotS, _ := p.StringVariant(context.Background(), "x", "def", featureflag.EvalContext{})
	assert.Equal(t, "def", gotS)
	gotI, _ := p.IntVariant(context.Background(), "x", 9, featureflag.EvalContext{})
	assert.Equal(t, int64(9), gotI)
	assert.NoError(t, p.Close())
}

func TestWrap_Idempotent(t *testing.T) {
	inner := &fakeProvider{boolVal: true}
	once := featureflag.Wrap(inner)
	twice := featureflag.Wrap(once)
	assert.Same(t, once, twice, "Wrap(Wrap(p)) must not double-wrap")
}

func TestWrap_ErrorReturnsDefault(t *testing.T) {
	inner := &fakeProvider{boolVal: true, strVal: "ON", intVal: 99, err: errors.New("backend down")}
	p := featureflag.Wrap(inner)
	ctx := context.Background()

	gotB, errB := p.BoolVariant(ctx, "x", false, featureflag.EvalContext{})
	assert.False(t, gotB, "value must be the caller default on backend error")
	assert.Error(t, errB, "advisory error preserved")

	gotS, errS := p.StringVariant(ctx, "x", "DEF", featureflag.EvalContext{})
	assert.Equal(t, "DEF", gotS)
	assert.Error(t, errS)

	gotI, errI := p.IntVariant(ctx, "x", -7, featureflag.EvalContext{})
	assert.Equal(t, int64(-7), gotI)
	assert.Error(t, errI)
}

func TestWrap_CleanHitReturnsValue(t *testing.T) {
	inner := &fakeProvider{boolVal: true, strVal: "ON", intVal: 99}
	p := featureflag.Wrap(inner)
	ctx := context.Background()

	gotB, errB := p.BoolVariant(ctx, "x", false, featureflag.EvalContext{})
	assert.True(t, gotB)
	assert.NoError(t, errB)
	gotS, _ := p.StringVariant(ctx, "x", "def", featureflag.EvalContext{})
	assert.Equal(t, "ON", gotS)
	gotI, _ := p.IntVariant(ctx, "x", 0, featureflag.EvalContext{})
	assert.Equal(t, int64(99), gotI)
	assert.Equal(t, "fake", p.Name())
}

func TestWrap_NilContextFailsClosed(t *testing.T) {
	p := featureflag.Wrap(&fakeProvider{boolVal: true, strVal: "ON", intVal: 99})
	//nolint:staticcheck // SA1012: deliberately nil
	gotB, errB := p.BoolVariant(nil, "x", false, featureflag.EvalContext{})
	assert.False(t, gotB)
	assert.ErrorIs(t, errB, context.Canceled)
	//nolint:staticcheck // SA1012
	gotS, _ := p.StringVariant(nil, "x", "def", featureflag.EvalContext{})
	assert.Equal(t, "def", gotS)
	//nolint:staticcheck // SA1012
	gotI, _ := p.IntVariant(nil, "x", 5, featureflag.EvalContext{})
	assert.Equal(t, int64(5), gotI)
}

func TestWrap_CancelledContextFailsClosed(t *testing.T) {
	p := featureflag.Wrap(&fakeProvider{boolVal: true, strVal: "ON", intVal: 99})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gotB, errB := p.BoolVariant(ctx, "x", false, featureflag.EvalContext{})
	assert.False(t, gotB)
	assert.ErrorIs(t, errB, context.Canceled)
	gotS, _ := p.StringVariant(ctx, "x", "def", featureflag.EvalContext{})
	assert.Equal(t, "def", gotS)
	gotI, _ := p.IntVariant(ctx, "x", 5, featureflag.EvalContext{})
	assert.Equal(t, int64(5), gotI)
}

func TestWrap_PanicFailsClosed(t *testing.T) {
	ctx := context.Background()

	// panic with an error value
	pErr := featureflag.Wrap(&fakeProvider{panicWith: errors.New("boom")})
	gotB, errB := pErr.BoolVariant(ctx, "x", false, featureflag.EvalContext{})
	assert.False(t, gotB)
	assert.Error(t, errB)

	// panic with a non-error value
	pStr := featureflag.Wrap(&fakeProvider{panicWith: "kaboom"})
	gotS, errS := pStr.StringVariant(ctx, "x", "def", featureflag.EvalContext{})
	assert.Equal(t, "def", gotS)
	assert.Error(t, errS)
	gotI, errI := pStr.IntVariant(ctx, "x", 3, featureflag.EvalContext{})
	assert.Equal(t, int64(3), gotI)
	assert.Error(t, errI)

	// Close must swallow a panic and never crash teardown
	assert.NoError(t, pErr.Close())
}

func TestWrap_CloseDelegates(t *testing.T) {
	cerr := errors.New("close failed")
	p := featureflag.Wrap(&fakeProvider{closeErr: cerr})
	assert.ErrorIs(t, p.Close(), cerr)
}

func TestBoolEnabled_Helper(t *testing.T) {
	ctx := context.Background()
	// on a clean hit returns the value
	assert.True(t, featureflag.BoolEnabled(ctx, &fakeProvider{boolVal: true}, "x", false, featureflag.EvalContext{}))
	// on error returns default
	assert.False(t, featureflag.BoolEnabled(ctx, &fakeProvider{err: errors.New("x")}, "x", false, featureflag.EvalContext{}))
	// nil provider -> default
	assert.True(t, featureflag.BoolEnabled(ctx, nil, "x", true, featureflag.EvalContext{}))
}
