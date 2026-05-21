package nats

import (
	"sync"
	"time"

	"github.com/nats-io/nkeys"
)

// PurgeAccountCacheForTest empties the in-memory accountCache. Used by tests
// to simulate the post-restart scenario where the cache is empty and the only
// way to revoke is via the persisted (encrypted) account seed via
// RevokeWithSeed. NOT exported outside _test.go.
func (p *Provider) PurgeAccountCacheForTest() {
	p.accountCache = sync.Map{}
}

// PoisonAccountCacheForTest stuffs a fake cachedAccount under the supplied
// resource token. Used to exercise the lookupCachedByPub "no-match-keep-
// scanning" branch with multiple entries in the cache.
func (p *Provider) PoisonAccountCacheForTest(token string) {
	p.accountCache.Store(token, cachedAccount{
		accountKP:  nil,
		accountPub: "AOTHER_PUB_KEY_THAT_NEVER_MATCHES",
		accountJWT: "",
		createdAt:  time.Now(),
	})
}

// SetCreateAccountKPForTest swaps the package-level account-NKey constructor.
// Returns a restore func. Lets tests drive the error branches that
// nkeys.CreateAccount otherwise never trips.
func SetCreateAccountKPForTest(fn func() (nkeys.KeyPair, error)) (restore func()) {
	orig := createAccountKP
	createAccountKP = fn
	return func() { createAccountKP = orig }
}

// SetCreateUserKPForTest is the user-NKey-constructor counterpart.
func SetCreateUserKPForTest(fn func() (nkeys.KeyPair, error)) (restore func()) {
	orig := createUserKP
	createUserKP = fn
	return func() { createUserKP = orig }
}

// SetFormatUserCredsForTest swaps the jwt.FormatUserConfig hook.
func SetFormatUserCredsForTest(fn func(string, []byte) ([]byte, error)) (restore func()) {
	orig := formatUserCreds
	formatUserCreds = fn
	return func() { formatUserCreds = orig }
}

// SetParseOperatorKPForTest swaps the nkeys.FromSeed hook used by both
// builder() (to parse the operator seed) and RevokeWithSeed (to parse the
// per-tenant account seed).
func SetParseOperatorKPForTest(fn func([]byte) (nkeys.KeyPair, error)) (restore func()) {
	orig := parseOperatorKP
	parseOperatorKP = fn
	return func() { parseOperatorKP = orig }
}
