package featureflag

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// White-box tests for Factory degrade paths that the black-box contract test
// cannot reach because the real static/flagd backends are always registered in
// the contract test binary.

// TestFactory_RecognisedButUnregisteredDegradesToStatic covers the path where a
// backend name normalises to a valid canonical name but no impl package
// registered a builder (e.g. a slim build that imported neither subpackage).
func TestFactory_RecognisedButUnregisteredDegradesToStatic(t *testing.T) {
	saved := builders
	builders = map[string]Builder{} // empty registry
	t.Cleanup(func() { builders = saved })

	p, err := Factory(Config{Backend: BackendFlagd})
	require.NotNil(t, p)
	require.Error(t, err, "advisory error expected when backend not registered")
	assert.Contains(t, err.Error(), "not registered")
	// degraded provider must fail closed
	got, _ := p.BoolVariant(context.Background(), "x", false, EvalContext{})
	assert.False(t, got)
	assert.NoError(t, p.Close())
}

// TestFactory_ConstructionFailureDegradesToStatic covers the path where a
// registered builder errors (e.g. flagd misconfig at boot). Factory must NOT
// propagate the error to the caller — it degrades to static.
func TestFactory_ConstructionFailureDegradesToStatic(t *testing.T) {
	saved := builders
	builders = map[string]Builder{}
	Register(BackendFlagd, func(Config) (Provider, error) {
		return nil, errors.New("flagd unreachable at boot")
	})
	t.Cleanup(func() { builders = saved })

	p, err := Factory(Config{Backend: BackendFlagd})
	require.NotNil(t, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "degraded to static")
	got, _ := p.BoolVariant(context.Background(), "x", false, EvalContext{})
	assert.False(t, got, "must fail closed after construction failure")
	assert.Equal(t, BackendStatic, p.Name(), "degraded provider must be static")
}

// TestFactory_NilProviderFromBuilderDegrades covers a builder that returns
// (nil, nil) — a misbehaving impl must still yield a usable provider.
func TestFactory_NilProviderFromBuilderDegrades(t *testing.T) {
	saved := builders
	builders = map[string]Builder{}
	Register(BackendFlagd, func(Config) (Provider, error) { return nil, nil })
	t.Cleanup(func() { builders = saved })

	p, err := Factory(Config{Backend: BackendFlagd})
	require.NotNil(t, p)
	require.Error(t, err)
	got, _ := p.BoolVariant(context.Background(), "x", false, EvalContext{})
	assert.False(t, got)
}

// TestFactory_HappyPathReturnsRegisteredProvider verifies the non-degraded path
// returns the registered (wrapped) provider with a nil error.
func TestFactory_HappyPathReturnsRegisteredProvider(t *testing.T) {
	saved := builders
	builders = map[string]Builder{}
	Register(BackendStatic, NewStaticBuilder())
	t.Cleanup(func() { builders = saved })

	p, err := Factory(Config{Backend: BackendStatic})
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, BackendStatic, p.Name())
}

// TestListRegistered_ReflectsRegistry is a small unit covering ListRegistered.
func TestListRegistered_ReflectsRegistry(t *testing.T) {
	saved := builders
	builders = map[string]Builder{}
	Register("static", NewStaticBuilder())
	t.Cleanup(func() { builders = saved })
	assert.Equal(t, []string{BackendStatic}, ListRegistered())
}
