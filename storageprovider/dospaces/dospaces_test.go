package dospaces_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/dospaces"
)

// TestNew_RequiresEndpoint — constructor refuses an empty endpoint.
func TestNew_RequiresEndpoint(t *testing.T) {
	_, err := dospaces.New(storageprovider.Config{
		MasterKey:    "k",
		MasterSecret: "s",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OBJECT_STORE_ENDPOINT")
}

// TestNew_RequiresMasterCreds — refuses empty key/secret. Without them,
// even broker-mode presigning cannot work.
func TestNew_RequiresMasterCreds(t *testing.T) {
	_, err := dospaces.New(storageprovider.Config{
		Endpoint: "nyc3.digitaloceanspaces.com",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OBJECT_STORE_ACCESS_KEY")
}

// TestCapabilities_ReflectsDOSpacesReality — DO Spaces has bucket-scoped
// keys (Jan 2025 GA) but NOT prefix-scoped keys. This is the honesty
// boundary the abstraction needs to surface.
func TestCapabilities_ReflectsDOSpacesReality(t *testing.T) {
	p, err := dospaces.New(storageprovider.Config{
		Endpoint:     "nyc3.digitaloceanspaces.com",
		MasterKey:    "K",
		MasterSecret: "S",
	})
	require.NoError(t, err)

	caps := p.Capabilities()
	assert.False(t, caps.PrefixScopedKeys, "DO Spaces does NOT enforce s3:prefix")
	assert.True(t, caps.BucketScopedKeys, "DO Spaces has bucket-scoped keys since Jan 2025")
	assert.False(t, caps.STS, "DO Spaces has no temp-credentials endpoint")
	assert.False(t, caps.BucketPerTenant, "DO Spaces has ~100 bucket cap")
	assert.Equal(t, 200, caps.MaxKeysPerAccount, "documented soft cap")
}

// TestIssueTenantCredentials_ReturnsMasterKey — DO Spaces issuance returns
// the platform master key (shared-master-key pattern). The api routes around
// this via Capabilities() before calling it for new tenants.
func TestIssueTenantCredentials_ReturnsMasterKey(t *testing.T) {
	p, err := dospaces.New(storageprovider.Config{
		Endpoint:     "nyc3.digitaloceanspaces.com",
		PublicURL:    "https://s3.instanode.dev",
		Region:       "nyc3",
		Bucket:       "instant-shared",
		MasterKey:    "MASTER_KEY",
		MasterSecret: "MASTER_SECRET",
		UseTLS:       true,
	})
	require.NoError(t, err)

	a, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "token-A",
	})
	require.NoError(t, err)
	b, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "token-B",
	})
	require.NoError(t, err)

	// Same master key issued to both tenants — the cross-tenant boundary the
	// api MUST avoid for new tenants by reading Capabilities().PrefixScopedKeys.
	assert.Equal(t, "MASTER_KEY", a.AccessKey)
	assert.Equal(t, a.AccessKey, b.AccessKey)
	// But prefixes differ per-token, so within the honor-system tenants stay
	// in their own namespaces.
	assert.NotEqual(t, a.Prefix, b.Prefix)
	// KeyID is empty (no per-tenant identity to revoke).
	assert.Empty(t, a.KeyID)
}

// TestRevokeTenantCredentials_NoOp — there is no per-tenant DO Spaces
// identity to remove, so Revoke is a logged no-op.
func TestRevokeTenantCredentials_NoOp(t *testing.T) {
	p, err := dospaces.New(storageprovider.Config{
		Endpoint:     "nyc3.digitaloceanspaces.com",
		MasterKey:    "K",
		MasterSecret: "S",
	})
	require.NoError(t, err)
	assert.NoError(t, p.RevokeTenantCredentials(context.Background(), "key_anything"))
}

// TestFactoryWiresDOSpaces — verifies the init()-time registration landed.
func TestFactoryWiresDOSpaces(t *testing.T) {
	p, err := storageprovider.Factory(storageprovider.Config{
		Backend:      "do-spaces",
		Endpoint:     "nyc3.digitaloceanspaces.com",
		MasterKey:    "K",
		MasterSecret: "S",
	})
	require.NoError(t, err)
	assert.Equal(t, "do-spaces", p.Name())
}
