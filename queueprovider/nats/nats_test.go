package nats_test

import (
	"context"
	"strings"
	"testing"
	"time"

	natsjwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/queueprovider"
	natsprov "instant.dev/common/queueprovider/nats"
)

// newOperatorSeed mints a fresh operator NKey seed for tests. In production
// this seed is generated once via `nsc` and stored in the nats-operator
// Secret; here we generate it inline so we don't have to ship test keys.
func newOperatorSeed(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateOperator()
	require.NoError(t, err)
	seed, err := kp.Seed()
	require.NoError(t, err)
	return string(seed)
}

// recordingPusher captures every PushAccountClaim call so we can assert the
// resolver was driven correctly.
type recordingPusher struct {
	pushes []struct {
		Pub string
		JWT string
	}
}

func (r *recordingPusher) PushAccountClaim(_ context.Context, pub, accountJWT string) error {
	r.pushes = append(r.pushes, struct {
		Pub string
		JWT string
	}{pub, accountJWT})
	return nil
}

// TestNATS_IssueIsolatedCredentials_MintsValidUserJWT verifies the happy path:
// when an operator seed is configured, IssueTenantCredentials mints a fresh
// account, pushes its claim to the resolver, and signs a user JWT scoped to
// the tenant's subject prefix only.
//
// This is the registry-iterating regression test that catches "we accidentally
// gave tenant A access to tenant B's subjects".
func TestNATS_IssueIsolatedCredentials_MintsValidUserJWT(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		Host:             "nats.test.local",
		PublicHost:       "nats.example.dev",
		Port:             4222,
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	natsProv, ok := p.(*natsprov.Provider)
	require.True(t, ok)
	pusher := &recordingPusher{}
	natsProv.SetResolverPusher(pusher)

	caps := p.Capabilities()
	assert.True(t, caps.PerTenantAccounts)
	assert.True(t, caps.SubjectScopedAuth)
	assert.True(t, caps.StreamIsolation)

	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "00000000-0000-0000-0000-000000000001",
		Subject:       "tenant_00000000000000000000000000000001.",
	})
	require.NoError(t, err)
	assert.Equal(t, queueprovider.AuthModeIsolated, creds.AuthMode)
	assert.NotEmpty(t, creds.JWT, "user JWT must be minted")
	assert.NotEmpty(t, creds.NKey, "user NKey seed must be minted")
	assert.True(t, strings.HasPrefix(creds.NKey, "SU"),
		"NKey seed prefix must be SU (user) — got %q", creds.NKey[:2])
	assert.NotEmpty(t, creds.CredsFile, ".creds file blob must be rendered")
	assert.True(t, strings.HasPrefix(creds.KeyID, "A"),
		"KeyID must be account public (prefix A) — got %q", creds.KeyID[:1])
	assert.Equal(t, "nats://nats.example.dev:4222", creds.ConnectionURL)

	// Resolver was driven.
	require.Len(t, pusher.pushes, 1)
	assert.Equal(t, creds.KeyID, pusher.pushes[0].Pub)

	// The user JWT decodes and lists the tenant's subject as the ONLY
	// pub/sub allow entry outside the JetStream $JS.API surface.
	userClaims, err := natsjwt.DecodeUserClaims(creds.JWT)
	require.NoError(t, err)
	assert.Equal(t, creds.KeyID, userClaims.IssuerAccount,
		"user JWT must be signed by the tenant's account")
	wildcardSubject := "tenant_00000000000000000000000000000001.>"
	assert.Contains(t, userClaims.Pub.Allow, wildcardSubject)
	assert.Contains(t, userClaims.Sub.Allow, wildcardSubject)

	// And does NOT contain another tenant's subject.
	otherSubject := "tenant_otherother.>"
	for _, allow := range userClaims.Pub.Allow {
		assert.NotEqual(t, otherSubject, allow,
			"tenant A's JWT must not allow pub on tenant B's subject")
	}
	for _, allow := range userClaims.Sub.Allow {
		assert.NotEqual(t, otherSubject, allow,
			"tenant A's JWT must not allow sub on tenant B's subject")
	}
}

// TestNATS_TwoTenants_DisjointSubjectPermissions verifies the isolation
// guarantee that justifies this entire package: two distinct tenants get
// JWTs whose subject allow-lists are disjoint.
func TestNATS_TwoTenants_DisjointSubjectPermissions(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		Host:             "nats.test.local",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	tokA := "11111111-1111-1111-1111-111111111111"
	tokB := "22222222-2222-2222-2222-222222222222"
	subjA := "tenant_aaaa11111111."
	subjB := "tenant_bbbb22222222."

	credsA, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: tokA,
		Subject:       subjA,
	})
	require.NoError(t, err)

	credsB, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: tokB,
		Subject:       subjB,
	})
	require.NoError(t, err)

	// Different account public keys (= different JetStream namespaces).
	assert.NotEqual(t, credsA.KeyID, credsB.KeyID,
		"each tenant must get its own account")

	claimsA, err := natsjwt.DecodeUserClaims(credsA.JWT)
	require.NoError(t, err)
	claimsB, err := natsjwt.DecodeUserClaims(credsB.JWT)
	require.NoError(t, err)

	wildA := subjA + ">"
	wildB := subjB + ">"

	assert.Contains(t, claimsA.Pub.Allow, wildA)
	assert.NotContains(t, claimsA.Pub.Allow, wildB,
		"tenant A pub-allow must NOT include tenant B's subject — this is the breach we're fixing")
	assert.Contains(t, claimsB.Sub.Allow, wildB)
	assert.NotContains(t, claimsB.Sub.Allow, wildA,
		"tenant B sub-allow must NOT include tenant A's subject — this is the breach we're fixing")

	// And the accounts are signed by different parents.
	assert.Equal(t, credsA.KeyID, claimsA.IssuerAccount)
	assert.Equal(t, credsB.KeyID, claimsB.IssuerAccount)
}

// TestNATS_TTL_AppliesUserJWTExpiry verifies short-lived user JWTs honor TTL.
func TestNATS_TTL_AppliesUserJWTExpiry(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		Host:             "nats.test.local",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	ttl := 7 * 24 * time.Hour
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-ttl",
		Subject:       "tenant_tokttl.",
		TTL:           ttl,
	})
	require.NoError(t, err)
	require.NotNil(t, creds.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(ttl), *creds.ExpiresAt, time.Minute)
	userClaims, err := natsjwt.DecodeUserClaims(creds.JWT)
	require.NoError(t, err)
	assert.NotZero(t, userClaims.Expires)
}

// TestNATS_Revoke_PushesAccountUpdate verifies the teardown path re-pushes a
// reset claim.
func TestNATS_Revoke_PushesAccountUpdate(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		Host:             "nats.test.local",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	pusher := &recordingPusher{}
	natsProv.SetResolverPusher(pusher)

	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-revoke",
	})
	require.NoError(t, err)

	require.Len(t, pusher.pushes, 1)
	err = p.RevokeTenantCredentials(context.Background(), creds.KeyID)
	require.NoError(t, err)
	require.Len(t, pusher.pushes, 2,
		"Revoke should have pushed an updated claim for the account")
	assert.Equal(t, creds.KeyID, pusher.pushes[1].Pub)
}
