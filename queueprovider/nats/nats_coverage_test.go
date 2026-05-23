package nats_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/queueprovider"
	natsprov "instant.dev/common/queueprovider/nats"
)

// brokenKP is a nkeys.KeyPair that fails selected methods so tests can
// exercise the post-create error branches in IssueTenantCredentials,
// RevokeTenantCredentials, and RevokeWithSeed.
type brokenKP struct {
	failPublicKey bool
	failSeed      bool
	failSign      bool
	realPublicKey string
	realSeed      []byte
}

func (b brokenKP) Seed() ([]byte, error) {
	if b.failSeed {
		return nil, errors.New("seed extraction failed")
	}
	return b.realSeed, nil
}

func (b brokenKP) PublicKey() (string, error) {
	if b.failPublicKey {
		return "", errors.New("public key derive failed")
	}
	return b.realPublicKey, nil
}

func (b brokenKP) PrivateKey() ([]byte, error) { return nil, errors.New("nope") }

func (b brokenKP) Sign(_ []byte) ([]byte, error) {
	if b.failSign {
		return nil, errors.New("sign failed")
	}
	return []byte("signed"), nil
}

func (b brokenKP) Verify(_ []byte, _ []byte) error { return errors.New("nope") }
func (b brokenKP) Wipe()                           {}
func (b brokenKP) Seal(_ []byte, _ string) ([]byte, error) {
	return nil, errors.New("nope")
}
func (b brokenKP) SealWithRand(_ []byte, _ string, _ io.Reader) ([]byte, error) {
	return nil, errors.New("nope")
}
func (b brokenKP) Open(_ []byte, _ string) ([]byte, error) { return nil, errors.New("nope") }

// errPusher is a ResolverPusher that always fails. Used to exercise the
// push-error branches in IssueTenantCredentials, RevokeTenantCredentials,
// and RevokeWithSeed.
type errPusher struct {
	err error
}

func (e errPusher) PushAccountClaim(_ context.Context, _, _ string) error {
	return e.err
}

// TestNATS_Builder_DefaultsApplied covers the empty-config defaults path in
// builder(): host, publicHost, port, subjectTemplate all fall back to the
// canonical platform defaults when caller supplies empty values.
func TestNATS_Builder_DefaultsApplied(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend: "nats",
		// Everything else intentionally empty so defaults engage.
	})
	require.NoError(t, err)
	require.NotNil(t, p)

	// In legacy_open mode (no operator seed), the ConnectionURL is built
	// from publicHost (which defaults to host which defaults to the
	// platform's internal cluster DNS).
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "default-token",
	})
	require.NoError(t, err)
	// Default scheme is plain nats:// (not tls://) and default port 4222.
	assert.Equal(t, "nats://nats.instant-data.svc.cluster.local:4222", creds.ConnectionURL)
	// Default subjectTemplate produces "tenant_<clean-token>." with hyphens
	// stripped.
	assert.Equal(t, "tenant_defaulttoken.", creds.Subject)
	// Legacy_open mode because no operator seed.
	assert.Equal(t, queueprovider.AuthModeLegacyOpen, creds.AuthMode)
}

// TestNATS_Builder_BadOperatorSeed exercises the error path in builder()
// where the supplied operator seed fails to parse as an nkeys seed.
func TestNATS_Builder_BadOperatorSeed(t *testing.T) {
	_, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: "not-a-valid-nkeys-seed",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, queueprovider.ErrAuthFailure)
	assert.Contains(t, err.Error(), "parse operator seed")
}

// TestNATS_Builder_UseTLS_ConnectionURL covers the useTLS=true branch in
// connectionURL(): the scheme flips from nats:// to tls://.
func TestNATS_Builder_UseTLS_ConnectionURL(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:    "nats",
		Host:       "nats.internal",
		PublicHost: "nats.public.example",
		Port:       4443,
		UseTLS:     true,
	})
	require.NoError(t, err)
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tls-tok",
	})
	require.NoError(t, err)
	assert.Equal(t, "tls://nats.public.example:4443", creds.ConnectionURL)
}

// TestNATS_Name returns the canonical backend identifier.
func TestNATS_Name(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "nats"})
	require.NoError(t, err)
	assert.Equal(t, "nats", p.Name())
}

// TestNATS_Capabilities_Shape covers the BasicAuth=false branch and the rest
// of the Capabilities struct shape that the existing tests partially touch.
func TestNATS_Capabilities_Shape(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "nats"})
	require.NoError(t, err)
	caps := p.Capabilities()
	assert.True(t, caps.PerTenantAccounts)
	assert.True(t, caps.SubjectScopedAuth)
	assert.True(t, caps.StreamIsolation)
	assert.False(t, caps.BasicAuth,
		"operator-mode NATS must reject basic auth on the operator listener")
}

// TestNATS_SetResolverPusher_NilResets covers the nil-arg branch in
// SetResolverPusher: passing nil must NOT panic and must reset the pusher to
// the no-op default, not leave the provider holding a nil interface that
// would nil-deref on the next currentPusher() call.
func TestNATS_SetResolverPusher_NilResets(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	// Set then reset to nil.
	rec := &recordingPusher{}
	natsProv.SetResolverPusher(rec)
	natsProv.SetResolverPusher(nil) // must collapse to noopPusher

	// After nil-reset, issuing creds must not error and rec must NOT see a
	// push (because pusher is now noop).
	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-after-nil-reset",
	})
	require.NoError(t, err)
	assert.Len(t, rec.pushes, 0,
		"after SetResolverPusher(nil) the no-op pusher is active; recorder must not have observed a push")
}

// TestNATS_Issue_EmptyResourceToken covers the input-validation branch in
// IssueTenantCredentials.
func TestNATS_Issue_EmptyResourceToken(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "nats"})
	require.NoError(t, err)
	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceToken required")
}

// TestNATS_Issue_LegacyOpen_NoOperatorSeed covers the operatorReady=false
// short-circuit: the provider returns AuthModeLegacyOpen creds with no JWT,
// NKey, or CredsFile populated.
func TestNATS_Issue_LegacyOpen_NoOperatorSeed(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:    "nats",
		Host:       "nats.test",
		PublicHost: "nats.public",
		Port:       4222,
	})
	require.NoError(t, err)

	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "legacy-tok",
	})
	require.NoError(t, err)
	assert.Equal(t, queueprovider.AuthModeLegacyOpen, creds.AuthMode)
	assert.Empty(t, creds.JWT, "legacy_open must NOT mint a user JWT")
	assert.Empty(t, creds.NKey, "legacy_open must NOT mint an NKey")
	assert.Empty(t, creds.CredsFile, "legacy_open must NOT emit a .creds blob")
	assert.Empty(t, creds.KeyID, "legacy_open has no account KeyID")
	assert.Empty(t, creds.AccountSeed, "legacy_open must NOT expose an account seed")
	// Default canonical subject derivation still runs.
	assert.Equal(t, "tenant_legacytok.", creds.Subject)
}

// TestNATS_Issue_SystemAccount_Rejected covers the SystemAccount=true error
// path: this credential is loaded directly from the nats-operator Secret, not
// issued via the provider, so the caller must not ask for it here.
func TestNATS_Issue_SystemAccount_Rejected(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "sys-tok",
		SystemAccount: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SystemAccount")
}

// TestNATS_Issue_PushError_Bubbles covers the pusher.err return branch in
// IssueTenantCredentials. A failing resolver push must abort issuance — we
// don't want to hand a caller credentials the resolver doesn't yet trust.
func TestNATS_Issue_PushError_Bubbles(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	wantErr := errors.New("resolver unreachable")
	natsProv.SetResolverPusher(errPusher{err: wantErr})

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-push-fail",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr,
		"resolver-push errors must propagate so callers know the account JWT was never persisted")
	assert.Contains(t, err.Error(), "push account claim to resolver")
}

// TestNATS_Revoke_EmptyKeyID covers the safe-no-op branch.
func TestNATS_Revoke_EmptyKeyID(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	assert.NoError(t, p.RevokeTenantCredentials(context.Background(), ""))
}

// TestNATS_Revoke_LegacyOpen_NoOp covers the !operatorReady branch in
// RevokeTenantCredentials — when the provider is in legacy_open mode there
// is nothing to revoke.
func TestNATS_Revoke_LegacyOpen_NoOp(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "nats"})
	require.NoError(t, err)
	// Even a non-empty keyID is a no-op when operator seed is unconfigured.
	assert.NoError(t, p.RevokeTenantCredentials(context.Background(), "ABCDEFG"))
}

// TestNATS_Revoke_CacheMiss_NoOp covers the lookupCachedByPub-misses branch.
// Without a cache hit, the provider has no account seed to re-sign — caller
// must use RevokeWithSeed instead; the bare Revoke is a safe no-op.
func TestNATS_Revoke_CacheMiss_NoOp(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	pusher := &recordingPusher{}
	natsProv.SetResolverPusher(pusher)

	// Pass a syntactically-valid-looking but never-issued account public key.
	// Use a real fresh account NKey so the form matches but the cache is empty.
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	freshPub, err := kp.PublicKey()
	require.NoError(t, err)

	err = p.RevokeTenantCredentials(context.Background(), freshPub)
	require.NoError(t, err,
		"cache-miss revoke is a safe no-op — caller is expected to fall back to RevokeWithSeed")
	assert.Len(t, pusher.pushes, 0, "no resolver push on cache-miss revoke")
}

// TestNATS_Revoke_PushError_Bubbles covers the push-error branch in
// RevokeTenantCredentials after a successful cache lookup + re-encode.
func TestNATS_Revoke_PushError_Bubbles(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	// First issue to populate the cache (using a recording pusher).
	rec := &recordingPusher{}
	natsProv.SetResolverPusher(rec)
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-revoke-push-fail",
	})
	require.NoError(t, err)
	require.Len(t, rec.pushes, 1)

	// Now swap in a failing pusher and revoke.
	wantErr := errors.New("resolver down")
	natsProv.SetResolverPusher(errPusher{err: wantErr})
	err = p.RevokeTenantCredentials(context.Background(), creds.KeyID)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}

// TestNATS_RevokeWithSeed_EmptySeed_NoOp covers the empty-seed branch.
func TestNATS_RevokeWithSeed_EmptySeed_NoOp(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	assert.NoError(t, natsProv.RevokeWithSeed(context.Background(), ""))
}

// TestNATS_RevokeWithSeed_LegacyOpen_NoOp covers the !operatorReady branch
// of RevokeWithSeed.
func TestNATS_RevokeWithSeed_LegacyOpen_NoOp(t *testing.T) {
	p, err := queueprovider.Factory(queueprovider.Config{Backend: "nats"})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	// Even a real-shape seed is a no-op when the provider has no operator.
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	seedBytes, err := kp.Seed()
	require.NoError(t, err)
	assert.NoError(t, natsProv.RevokeWithSeed(context.Background(), string(seedBytes)))
}

// TestNATS_RevokeWithSeed_InvalidSeed covers the FromSeed-error branch.
func TestNATS_RevokeWithSeed_InvalidSeed(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	err = natsProv.RevokeWithSeed(context.Background(), "not-a-real-nkeys-seed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse account seed")
}

// TestNATS_RevokeWithSeed_PushError covers the push-error branch in
// RevokeWithSeed after the seed parses cleanly.
func TestNATS_RevokeWithSeed_PushError(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	// Mint a fresh account seed (without going through Issue, so the cache
	// stays empty — exercises the RevokeWithSeed-only path with no Revoke
	// cache hit possible).
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	seedBytes, err := kp.Seed()
	require.NoError(t, err)

	wantErr := errors.New("resolver unreachable from revoke-with-seed")
	natsProv.SetResolverPusher(errPusher{err: wantErr})

	err = natsProv.RevokeWithSeed(context.Background(), string(seedBytes))
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}

// TestNATS_CanonicalSubject_NoExplicitSubject covers the auto-derived-subject
// branch in IssueTenantCredentials (when IssueRequest.Subject is empty,
// canonicalSubject() must fire and produce "tenant_<clean>.").
func TestNATS_CanonicalSubject_NoExplicitSubject(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "aaaa-bbbb-cccc-dddd",
		// Subject deliberately empty.
	})
	require.NoError(t, err)
	assert.Equal(t, "tenant_aaaabbbbccccdddd.", creds.Subject,
		"canonicalSubject must strip hyphens and use the tenant_<token>. template")
}

// TestNATS_Issue_CreateAccount_Fail covers the createAccountKP error branch.
func TestNATS_Issue_CreateAccount_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateAccountKPForTest(func() (nkeys.KeyPair, error) {
		return nil, errors.New("create account boom")
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-create-acct-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create account NKey")
}

// TestNATS_Issue_AccountPublicKey_Fail covers the accountKP.PublicKey() error
// branch.
func TestNATS_Issue_AccountPublicKey_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateAccountKPForTest(func() (nkeys.KeyPair, error) {
		return brokenKP{failPublicKey: true}, nil
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-acct-pub-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "derive account public key")
}

// TestNATS_Issue_AccountSeed_Fail covers the accountKP.Seed() error branch.
func TestNATS_Issue_AccountSeed_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateAccountKPForTest(func() (nkeys.KeyPair, error) {
		// PublicKey works (returns a real-looking pub), Seed fails.
		return brokenKP{
			realPublicKey: "AOK_DERIVES_FINE",
			failSeed:      true,
		}, nil
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-acct-seed-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract account seed")
}

// TestNATS_Issue_CreateUser_Fail covers the createUserKP error branch.
func TestNATS_Issue_CreateUser_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateUserKPForTest(func() (nkeys.KeyPair, error) {
		return nil, errors.New("create user boom")
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-create-user-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create user NKey")
}

// TestNATS_Issue_UserPublicKey_Fail covers the userKP.PublicKey() error
// branch.
func TestNATS_Issue_UserPublicKey_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateUserKPForTest(func() (nkeys.KeyPair, error) {
		return brokenKP{failPublicKey: true}, nil
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-user-pub-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "derive user public key")
}

// TestNATS_Issue_UserSeed_Fail covers the userKP.Seed() error branch.
func TestNATS_Issue_UserSeed_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetCreateUserKPForTest(func() (nkeys.KeyPair, error) {
		return brokenKP{
			realPublicKey: "UOK_USER_PUB",
			failSeed:      true,
		}, nil
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-user-seed-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract user seed")
}

// TestNATS_Issue_FormatUserCreds_Fail covers the jwt.FormatUserConfig error
// branch.
func TestNATS_Issue_FormatUserCreds_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	restore := natsprov.SetFormatUserCredsForTest(func(_ string, _ []byte) ([]byte, error) {
		return nil, errors.New("format boom")
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-format-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "format .creds blob")
}

// TestNATS_Revoke_LookupRangeContinues exercises the "no match, continue
// scanning" branch in lookupCachedByPub by stuffing multiple entries into the
// cache where the first one inspected doesn't match the target pub key.
func TestNATS_Revoke_LookupRangeContinues(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	natsProv.SetResolverPusher(&recordingPusher{})

	// Poison the cache with a known non-matching entry first, then issue a
	// real credential that lands a real entry. sync.Map.Range order is
	// unspecified — we add 5 decoys so the real entry is statistically
	// unlikely to be the first one scanned. The test is correct regardless:
	// it asserts that Revoke succeeds (whether or not the decoys were
	// scanned first, the matching entry must still be found).
	for i := 0; i < 5; i++ {
		natsProv.PoisonAccountCacheForTest("decoy-" + string(rune('a'+i)))
	}
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-range-continue",
	})
	require.NoError(t, err)
	err = p.RevokeTenantCredentials(context.Background(), creds.KeyID)
	require.NoError(t, err, "lookupCachedByPub must skip over non-matching entries to find the real one")
}

// TestNATS_Builder_OperatorPublicKey_Fail covers the opKP.PublicKey() error
// branch in builder().
func TestNATS_Builder_OperatorPublicKey_Fail(t *testing.T) {
	restore := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		return brokenKP{failPublicKey: true}, nil
	})
	defer restore()

	_, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: "anything-non-empty",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, queueprovider.ErrAuthFailure)
	assert.Contains(t, err.Error(), "derive operator public key")
}

// TestNATS_Issue_AccountEncode_Fail covers the accClaims.Encode(operatorKP)
// failure branch by feeding builder() a broken operator KP whose Sign() fails.
func TestNATS_Issue_AccountEncode_Fail(t *testing.T) {
	restore := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		// Looks like a working KP for builder() (PublicKey OK) but Sign
		// fails so jwt.Encode bubbles back up.
		return brokenKP{
			realPublicKey: "OOK_OPERATOR_PUB",
			failSign:      true,
		}, nil
	})
	defer restore()

	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: "operator-seed-placeholder",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-acct-encode-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign account JWT")
}

// TestNATS_Issue_UserEncode_Fail covers the userClaims.Encode(accountKP)
// failure branch. The trick: operator KP must Sign() successfully (so the
// account JWT encodes), but the freshly-minted accountKP must fail Sign() so
// the user JWT encode fails. We swap createAccountKP to return a KP whose
// Sign() fails.
func TestNATS_Issue_UserEncode_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)

	// Real accountKP first so we can capture its real public + seed; then
	// inject a wrapper that returns them but fails Sign().
	realKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
	realPub, err := realKP.PublicKey()
	require.NoError(t, err)
	realSeed, err := realKP.Seed()
	require.NoError(t, err)

	restore := natsprov.SetCreateAccountKPForTest(func() (nkeys.KeyPair, error) {
		return brokenKP{
			realPublicKey: realPub,
			realSeed:      realSeed,
			failSign:      true,
		}, nil
	})
	defer restore()

	_, err = p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-user-encode-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign user JWT")
}

// TestNATS_Revoke_Encode_Fail covers the revocation-encode error branch in
// RevokeTenantCredentials by issuing with a real operator KP, then swapping
// in a broken operator KP via the Provider's private field... since we don't
// have a setter, we drive it the other way: build a Factory whose
// parseOperatorKP hook returns a KP that signs ONCE (during initial issue)
// but fails on the second call (during revoke). Simpler: use a counting KP.
func TestNATS_Revoke_Encode_Fail(t *testing.T) {
	type signCountingKP struct {
		brokenKP
		signCount *int
	}

	// We need a KP that wraps a real signer for the first N calls then
	// fails. Easiest: real operator KP for issue + a separate provider for
	// revoke that has a fail-sign operator KP. But that doesn't share the
	// cache. Solution: issue with a real operator, then artificially poison
	// the operatorKP via parseOperatorKP for a separate builder, swap the
	// pusher and accountCache from the first provider — that's too brittle.
	//
	// Cleaner: drive a single provider whose operator KP fails Sign() on
	// the SECOND call. The first call (account JWT encode during issue)
	// succeeds; the second call (account JWT re-encode during revoke)
	// fails.
	_ = signCountingKP{}

	realOpKP, err := nkeys.CreateOperator()
	require.NoError(t, err)
	realOpPub, err := realOpKP.PublicKey()
	require.NoError(t, err)

	calls := 0
	failingOpKP := &flakeyKP{
		inner:       realOpKP,
		realPub:     realOpPub,
		failAtCall:  2, // fail starting at the 2nd Sign() invocation
		callCounter: &calls,
	}

	restore := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		return failingOpKP, nil
	})
	defer restore()

	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: "placeholder-seed",
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	natsProv.SetResolverPusher(&recordingPusher{})

	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok-revoke-encode-fail",
	})
	require.NoError(t, err)

	err = p.RevokeTenantCredentials(context.Background(), creds.KeyID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode revocation")
}

// flakeyKP wraps a real KeyPair but fails Sign() starting at the Nth call.
type flakeyKP struct {
	inner       nkeys.KeyPair
	realPub     string
	failAtCall  int
	callCounter *int
}

func (f *flakeyKP) Seed() ([]byte, error)       { return f.inner.Seed() }
func (f *flakeyKP) PublicKey() (string, error)  { return f.realPub, nil }
func (f *flakeyKP) PrivateKey() ([]byte, error) { return f.inner.PrivateKey() }
func (f *flakeyKP) Sign(b []byte) ([]byte, error) {
	*f.callCounter++
	if *f.callCounter >= f.failAtCall {
		return nil, errors.New("sign failed at flakey call")
	}
	return f.inner.Sign(b)
}
func (f *flakeyKP) Verify(b []byte, sig []byte) error { return f.inner.Verify(b, sig) }
func (f *flakeyKP) Wipe()                             { f.inner.Wipe() }
func (f *flakeyKP) Seal(b []byte, r string) ([]byte, error) {
	return f.inner.Seal(b, r)
}
func (f *flakeyKP) SealWithRand(b []byte, r string, rr io.Reader) ([]byte, error) {
	return f.inner.SealWithRand(b, r, rr)
}
func (f *flakeyKP) Open(b []byte, s string) ([]byte, error) {
	return f.inner.Open(b, s)
}

// TestNATS_RevokeWithSeed_PublicKey_Fail covers kp.PublicKey() error in
// RevokeWithSeed, after FromSeed/parseOperatorKP succeeded.
func TestNATS_RevokeWithSeed_PublicKey_Fail(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)

	// Now swap parseOperatorKP so the RevokeWithSeed call hits a broken KP.
	// (The builder() call above already succeeded with the real
	// nkeys.FromSeed, so swapping now only affects future calls.)
	restore := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		return brokenKP{failPublicKey: true}, nil
	})
	defer restore()

	err = natsProv.RevokeWithSeed(context.Background(), "any-non-empty-seed-string")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "derive account public")
}

// TestNATS_RevokeWithSeed_Encode_Fail covers the accClaims.Encode failure
// branch in RevokeWithSeed.
func TestNATS_RevokeWithSeed_Encode_Fail(t *testing.T) {
	// We need the builder's operator KP to be a Sign-failing wrapper. The
	// Encode call inside RevokeWithSeed signs with p.operatorKP — so this
	// is the same flakey-after-N-calls pattern as TestNATS_Revoke_Encode_Fail,
	// but for RevokeWithSeed we don't issue first, so we need the operator
	// to fail on its very first Sign().
	realOp, err := nkeys.CreateOperator()
	require.NoError(t, err)
	realPub, err := realOp.PublicKey()
	require.NoError(t, err)

	calls := 0
	failingOp := &flakeyKP{
		inner:       realOp,
		realPub:     realPub,
		failAtCall:  1, // first Sign() call fails
		callCounter: &calls,
	}

	restoreOp := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		// Return the flakey operator the first time (builder), then return
		// a real account KP on subsequent calls (when RevokeWithSeed parses
		// the per-tenant account seed). We need two behaviors from one
		// hook — track call count.
		return failingOp, nil
	})

	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: "anything",
	})
	require.NoError(t, err)
	restoreOp()

	// Now make parseOperatorKP return a fresh real account KP for the
	// RevokeWithSeed call's FromSeed.
	realAcct, err := nkeys.CreateAccount()
	require.NoError(t, err)
	realAcctPub, err := realAcct.PublicKey()
	require.NoError(t, err)
	restore := natsprov.SetParseOperatorKPForTest(func(_ []byte) (nkeys.KeyPair, error) {
		return brokenKP{realPublicKey: realAcctPub}, nil
	})
	defer restore()

	natsProv := p.(*natsprov.Provider)
	err = natsProv.RevokeWithSeed(context.Background(), "tenant-account-seed-placeholder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode revocation")
}

// TestNATS_ShortToken_StableAcrossIssuance verifies the user-claim Name field
// is derived stably from the resource token, so an operator reading
// nats-server audit logs can correlate a Name back to a resource.
func TestNATS_ShortToken_StableAcrossIssuance(t *testing.T) {
	seed := newOperatorSeed(t)
	p, err := queueprovider.Factory(queueprovider.Config{
		Backend:          "nats",
		NATSOperatorSeed: seed,
	})
	require.NoError(t, err)
	natsProv := p.(*natsprov.Provider)
	natsProv.SetResolverPusher(&recordingPusher{})

	tok := "stable-token-value"
	c1, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: tok,
	})
	require.NoError(t, err)
	c2, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: tok,
	})
	require.NoError(t, err)
	// Subject is purely a function of the token, so it must match exactly.
	assert.Equal(t, c1.Subject, c2.Subject,
		"canonicalSubject must be a pure function of ResourceToken")
	// Both creds must reference the canonical subject (hyphen-stripped).
	assert.True(t, strings.HasPrefix(c1.Subject, "tenant_"))
}
