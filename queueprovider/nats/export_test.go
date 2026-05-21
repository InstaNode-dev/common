package nats

import "sync"

// PurgeAccountCacheForTest empties the in-memory accountCache. Used by tests
// to simulate the post-restart scenario where the cache is empty and the only
// way to revoke is via the persisted (encrypted) account seed via
// RevokeWithSeed. NOT exported outside _test.go.
func (p *Provider) PurgeAccountCacheForTest() {
	p.accountCache = sync.Map{}
}
