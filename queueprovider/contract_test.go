package queueprovider_test

// contract_test.go — registry-iterating contract test for the queue provider
// abstraction (CLAUDE.md rule 18).
//
// Every backend implementation registers itself with the global registry at
// package-init via queueprovider.Register(name, builder). This test iterates
// the live registry rather than a hand-typed slice, so a fifth backend added
// later is automatically covered.
//
// What the contract verifies (independent of which backend is on the wire):
//   - Builder accepts a minimal Config and returns a non-nil provider
//   - provider.Name() is the canonical name we registered it under
//   - provider.Capabilities() is internally consistent
//   - provider.RevokeTenantCredentials("") is a safe no-op (the teardown
//     path relies on this)

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"instant.dev/common/queueprovider"

	// side-effect imports register each backend
	_ "instant.dev/common/queueprovider/kafka"
	_ "instant.dev/common/queueprovider/legacyopen"
	_ "instant.dev/common/queueprovider/nats"
	_ "instant.dev/common/queueprovider/rabbitmq"
)

// configForBackend returns the minimum Config needed to construct each
// provider. Kept centralised so the test stays small and any new field the
// providers start requiring shows up here.
func configForBackend(name string) queueprovider.Config {
	return queueprovider.Config{
		Backend:    name,
		Host:       "example.local",
		PublicHost: "example.dev",
		Port:       4222,
	}
}

// TestRegistry_AllProvidersSatisfyContract iterates every registered backend
// and checks the shared invariants. Required by CLAUDE.md rule 18: a hand-
// typed slice of backends would silently fail to cover a fifth backend
// added later.
func TestRegistry_AllProvidersSatisfyContract(t *testing.T) {
	registered := queueprovider.ListRegistered()
	assert.GreaterOrEqual(t, len(registered), 4,
		"expected at least 4 backends registered (nats, rabbitmq, kafka, legacy_open); got %v", registered)

	for _, name := range registered {
		name := name
		t.Run(name, func(t *testing.T) {
			cfg := configForBackend(name)
			p, err := queueprovider.Factory(cfg)
			if err != nil {
				t.Fatalf("Factory(%q): %v", name, err)
			}
			if p == nil {
				t.Fatalf("Factory(%q) returned nil provider", name)
			}
			assert.Equal(t, name, p.Name(), "Name() must match registered name")

			caps := p.Capabilities()
			// Internal consistency: PerTenantAccounts implies StreamIsolation.
			if caps.PerTenantAccounts {
				assert.True(t, caps.StreamIsolation,
					"%s: PerTenantAccounts=true should imply StreamIsolation=true", name)
			}

			// RevokeTenantCredentials("") must be a safe no-op so the teardown
			// path can call it unconditionally.
			assert.NoError(t, p.RevokeTenantCredentials(context.Background(), ""),
				"%s: RevokeTenantCredentials(\"\") must be a no-op", name)
		})
	}
}

// TestFactory_UnknownBackendReturnsError verifies the factory hard-fails on
// an unknown backend name. Silent fallback to a less-secure backend is the
// failure mode this abstraction exists to prevent.
func TestFactory_UnknownBackendReturnsError(t *testing.T) {
	_, err := queueprovider.Factory(queueprovider.Config{Backend: "made-up"})
	assert.Error(t, err)
	assert.ErrorIs(t, err, queueprovider.ErrUnknownBackend)
}

// TestNormalizeBackend covers the alias table — every operator-facing string
// that should map to a canonical name. Hand-typed because the table itself is
// the SUT.
func TestNormalizeBackend(t *testing.T) {
	cases := map[string]string{
		"":              "nats", // empty defaults to nats
		"unknown":       "",
		"nats":          "nats",
		"NATS":          "nats",
		"jetstream":     "nats",
		"nats-jetstream": "nats",
		"rabbitmq":      "rabbitmq",
		"rabbit":        "rabbitmq",
		"amqp":          "rabbitmq",
		"kafka":         "kafka",
		"redpanda":      "kafka",
		"legacy_open":   "legacy_open",
		"legacy-open":   "legacy_open",
		"noauth":        "legacy_open",
		"none":          "legacy_open",
	}
	for in, want := range cases {
		got := queueprovider.NormalizeBackend(in)
		assert.Equal(t, want, got, "NormalizeBackend(%q)", in)
	}
}

// TestNATSProvider_IssueWithoutOperatorReturnsLegacyOpen verifies the
// staged-cutover guard: when the operator seed is not configured, the nats
// provider returns auth_mode=legacy_open creds instead of failing. This lets
// us deploy the code BEFORE the operator runs `nsc generate` + applies the
// nats-operator Secret.
func TestNATSProvider_IssueWithoutOperatorReturnsLegacyOpen(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend: "nats",
		Host:    "nats.test.local",
	})
	assert.NoError(t, err)
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "test-token-abcdef",
		Subject:       "tenant_testtokenabcdef.",
	})
	assert.NoError(t, err)
	assert.Equal(t, queueprovider.AuthModeLegacyOpen, creds.AuthMode,
		"with no operator seed, nats provider must yield legacy_open creds")
	assert.Empty(t, creds.JWT, "legacy_open creds carry no JWT")
	assert.Empty(t, creds.NKey, "legacy_open creds carry no NKey")
}

// TestRabbitMQ_SkeletonReturnsNotImplemented verifies the skeleton fails loud
// rather than silently passing through unauthenticated traffic.
func TestRabbitMQ_SkeletonReturnsNotImplemented(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "rabbitmq"})
	assert.NoError(t, err)
	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok",
	})
	assert.ErrorIs(t, err, queueprovider.ErrNotImplemented)
}

// TestKafka_SkeletonReturnsNotImplemented mirrors the RabbitMQ check.
func TestKafka_SkeletonReturnsNotImplemented(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "kafka"})
	assert.NoError(t, err)
	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok",
	})
	assert.ErrorIs(t, err, queueprovider.ErrNotImplemented)
}
