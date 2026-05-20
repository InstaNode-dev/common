package storageprovider_test

// contract_test.go — registry-iterating contract test for the storage provider
// abstraction (CLAUDE.md rule 18).
//
// Every backend implementation registers itself with the global registry at
// package-init via storageprovider.Register(name, builder). This test iterates
// the live registry rather than a hand-typed slice, so a fifth backend added
// later is automatically covered.
//
// What the contract verifies (independent of which backend is on the wire):
//   - Builder accepts a minimal Config and returns a non-nil provider
//   - provider.Name() is the canonical name we registered it under
//   - provider.Capabilities() is internally consistent
//     (PrefixScopedKeys=true implies BucketScopedKeys=true, for example)
//   - provider.RevokeTenantCredentials("") is a safe no-op (the broker-mode
//     teardown path relies on this)

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"instant.dev/common/storageprovider"

	// side-effect imports register each backend
	_ "instant.dev/common/storageprovider/dospaces"
	_ "instant.dev/common/storageprovider/r2"
	_ "instant.dev/common/storageprovider/s3"
)

// configForBackend returns the minimum Config needed to construct each
// provider. Kept centralised so the test stays small and so any new field
// the providers start requiring shows up here, not buried in three
// per-backend tests.
func configForBackend(name string) storageprovider.Config {
	base := storageprovider.Config{
		Backend:      name,
		Endpoint:     "example.local:9000",
		PublicURL:    "https://example.dev",
		Region:       "us-east-1",
		Bucket:       "instant-shared",
		MasterKey:    "MASTER",
		MasterSecret: "SECRET",
		UseTLS:       true,
	}
	switch name {
	case "r2":
		base.R2AccountID = "deadbeefdeadbeefdeadbeefdeadbeef"
		base.R2APIToken = "test-token"
	case "s3":
		base.AWSRoleARN = "arn:aws:iam::123456789012:role/instanode-test"
	}
	return base
}

// TestRegistry_AllProvidersSatisfyContract iterates every registered backend
// and checks the shared invariants. Required by CLAUDE.md rule 18: a hand-
// typed slice of backends would silently fail to cover a fifth backend
// added later.
func TestRegistry_AllProvidersSatisfyContract(t *testing.T) {
	registered := storageprovider.ListRegistered()
	assert.GreaterOrEqual(t, len(registered), 3,
		"expected at least 3 backends registered (do-spaces, r2, s3); got %v", registered)

	for _, name := range registered {
		name := name
		t.Run(name, func(t *testing.T) {
			cfg := configForBackend(name)
			p, err := storageprovider.Factory(cfg)
			if err != nil {
				t.Fatalf("Factory(%q): %v", name, err)
			}
			if p == nil {
				t.Fatalf("Factory(%q) returned nil provider", name)
			}
			assert.Equal(t, name, p.Name(), "Name() must match registered name")

			caps := p.Capabilities()
			// Internal consistency: prefix-scoping is a strict super-set of
			// bucket-scoping (any backend that enforces s3:prefix can also
			// scope by bucket).
			if caps.PrefixScopedKeys {
				assert.True(t, caps.BucketScopedKeys,
					"%s: PrefixScopedKeys=true should imply BucketScopedKeys=true", name)
			}

			// RevokeTenantCredentials("") must be a safe no-op so the broker-
			// mode teardown path can call it unconditionally.
			assert.NoError(t, p.RevokeTenantCredentials(context.Background(), ""),
				"%s: RevokeTenantCredentials(\"\") must be a no-op", name)
		})
	}
}

// TestFactory_UnknownBackendReturnsError verifies the factory hard-fails on
// an unknown backend name. Silent fallback to a less-secure backend is the
// failure mode this abstraction exists to prevent.
func TestFactory_UnknownBackendReturnsError(t *testing.T) {
	_, err := storageprovider.Factory(storageprovider.Config{Backend: "made-up"})
	assert.Error(t, err)
	assert.ErrorIs(t, err, storageprovider.ErrUnknownBackend)
}

// TestNormalizeBackend covers the alias table — every operator-facing string
// that should map to a canonical name. Hand-typed because the table itself is
// the SUT.
func TestNormalizeBackend(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"unknown":        "",
		"do-spaces":      "do-spaces",
		"DO_SPACES":      "do-spaces",
		"digitalocean":   "do-spaces",
		"spaces":         "do-spaces",
		"r2":             "r2",
		"cloudflare":     "r2",
		"cloudflare-r2":  "r2",
		"s3":             "s3",
		"aws":            "s3",
		"AWS-S3":         "s3",
		"minio":          "minio",
		"minio-admin":    "minio",
		"admin":          "minio",
		"iam":            "minio",
	}
	for in, want := range cases {
		got := storageprovider.NormalizeBackend(in)
		assert.Equal(t, want, got, "NormalizeBackend(%q)", in)
	}
}
