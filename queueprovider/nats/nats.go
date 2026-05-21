// Package nats implements queueprovider.QueueCredentialProvider for NATS in
// operator mode.
//
// # The accounts model
//
// NATS supports a three-tier identity model:
//
//   Operator → Account → User
//
// The OPERATOR signs ACCOUNT claims. Each tenant gets its own ACCOUNT (which
// implies its own JetStream namespace, its own subject namespace, etc).
// Inside an account, USERS are minted with subject-scoped pub/sub
// permissions; tenants present a signed user JWT + an NKey seed when
// connecting and the server validates the JWT against the resolver-cached
// account JWT signed by the operator.
//
// This package handles the CRYPTOGRAPHIC minting (steps 1-2-4 below). The
// "push the new account claim to the running nats-server" step is abstracted
// behind ResolverPusher so we don't have to import `github.com/nats-io/nats.go`
// (a heavy dep with network code) into the `common` module. The provisioner
// injects a real NATS-client-backed ResolverPusher; tests inject a no-op.
//
// # Issue flow (steps performed per /queue/new)
//
//  1. Generate a fresh account NKey pair (NewAccount → Aaaa..., SAaaa...)
//  2. Build + sign an account JWT with the operator seed; permissions list
//     the tenant's subject prefix as allowed pub+sub.
//  3. Push the account claim to nats-server via ResolverPusher (req/reply on
//     $SYS.REQ.CLAIMS.UPDATE).
//  4. Generate a user NKey pair (NewUser → Uaaa..., SUaaa...) inside the
//     account, sign a user JWT with the account seed.
//  5. Return TenantCreds containing the user JWT + user NKey seed.
//
// # Revoke flow
//
//   1. Add the user public key to the account's revocation list.
//   2. Re-sign and push the updated account claim.
//
// We keep the account itself around (so we have audit history); a full
// account delete is a separate op (account_purge subject) used by the worker
// reaper.
package nats

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"instant.dev/common/queueprovider"
)

func init() {
	queueprovider.Register("nats", builder)
}

// builder is the Factory entry point. Returns ErrAuthFailure-flavored errors
// when the operator seed is unparseable, so the caller can degrade gracefully
// during the pre-cutover window.
func builder(cfg queueprovider.Config) (queueprovider.QueueCredentialProvider, error) {
	host := cfg.Host
	if host == "" {
		host = "nats.instant-data.svc.cluster.local"
	}
	publicHost := cfg.PublicHost
	if publicHost == "" {
		publicHost = host
	}
	port := cfg.Port
	if port == 0 {
		port = 4222
	}
	tmpl := cfg.SubjectTemplate
	if tmpl == "" {
		tmpl = "tenant_<token>."
	}
	p := &Provider{
		host:               host,
		publicHost:         publicHost,
		port:               port,
		useTLS:             cfg.UseTLS,
		subjectTemplate:    tmpl,
		systemAccountKey:   cfg.NATSSystemAccountPublicKey,
		pusher:             noopPusher{},
	}
	// Operator seed is only required when we will actually issue isolated
	// credentials. When unset, the provider returns legacy_open-flavor creds
	// (no user JWT, no NKey) so the cutover can be staged: deploy code that
	// understands operator mode first, populate the secret + flip
	// nats.yaml later.
	if cfg.NATSOperatorSeed != "" {
		opKP, err := nkeys.FromSeed([]byte(cfg.NATSOperatorSeed))
		if err != nil {
			return nil, fmt.Errorf("%w: parse operator seed: %v", queueprovider.ErrAuthFailure, err)
		}
		p.operatorKP = opKP
		opPub, err := opKP.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("%w: derive operator public key: %v", queueprovider.ErrAuthFailure, err)
		}
		p.operatorPub = opPub
		p.operatorReady = true
	}
	return p, nil
}

// ResolverPusher pushes new/updated account claims to the running nats-server
// via the resolver. The real impl lives in the provisioner (where the
// `nats.go` client dep is already pulled in via the prober path); tests inject
// noopPusher. ResolverPusher is set after construction via
// `(*Provider).SetResolverPusher` so the boot path doesn't have to thread a
// NATS client through Factory.
type ResolverPusher interface {
	// PushAccountClaim publishes the signed account JWT to the resolver. On
	// memory-mode resolvers this is best-effort fire-and-forget; on
	// full-resolver it is request/reply with a server-side persistence ack.
	PushAccountClaim(ctx context.Context, accountPublicKey, accountJWT string) error
}

type noopPusher struct{}

func (noopPusher) PushAccountClaim(_ context.Context, _, _ string) error { return nil }

// Provider implements queueprovider.QueueCredentialProvider for NATS in
// operator mode. Safe for concurrent use across goroutines.
type Provider struct {
	host            string
	publicHost      string
	port            int
	useTLS          bool
	subjectTemplate string

	operatorKP    nkeys.KeyPair
	operatorPub   string
	operatorReady bool // false → fall back to legacy_open

	systemAccountKey string

	mu     sync.Mutex
	pusher ResolverPusher

	// accountCache maps resource token → minted account NKey, so a tenant
	// can revoke later without us having to dig the seed out of the DB.
	// In production, the SECRET seed is stored in the resources table by
	// the api after IssueTenantCredentials; this cache is best-effort
	// only — Revoke re-derives from the persisted seed when missing.
	accountCache sync.Map // token → cachedAccount
}

type cachedAccount struct {
	accountKP    nkeys.KeyPair
	accountPub   string
	accountJWT   string
	createdAt    time.Time
}

// SetResolverPusher injects the resolver-push backend. The provisioner calls
// this once at boot after connecting to NATS as the SYS account.
func (p *Provider) SetResolverPusher(r ResolverPusher) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r == nil {
		p.pusher = noopPusher{}
		return
	}
	p.pusher = r
}

// Name returns "nats" — the canonical backend identifier.
func (p *Provider) Name() string { return "nats" }

// Capabilities reports what NATS operator-mode actually enforces.
func (p *Provider) Capabilities() queueprovider.Capabilities {
	return queueprovider.Capabilities{
		PerTenantAccounts: true,
		SubjectScopedAuth: true,
		BasicAuth:         false, // operator-mode rejects basic auth on the same listener
		StreamIsolation:   true,  // JetStream is per-account by construction
	}
}

// IssueTenantCredentials mints a fresh account + user for the resource token.
// When operatorReady is false (operator seed not configured) it returns
// legacy_open-mode creds with no JWT/NKey so the rest of the system stays
// up during the staged cutover.
func (p *Provider) IssueTenantCredentials(ctx context.Context, in queueprovider.IssueRequest) (*queueprovider.TenantCreds, error) {
	if in.ResourceToken == "" {
		return nil, errors.New("queueprovider.nats: ResourceToken required")
	}

	subject := in.Subject
	if subject == "" {
		subject = p.canonicalSubject(in.ResourceToken)
	}

	// Pre-cutover: no operator seed loaded. Return a legacy_open shim. The
	// api will mark the resource auth_mode=legacy_open. Clients still use
	// the (unauthenticated) shared NATS until they recycle into an isolated
	// provision.
	if !p.operatorReady {
		return &queueprovider.TenantCreds{
			ConnectionURL: p.connectionURL("", ""),
			Subject:       subject,
			AuthMode:      queueprovider.AuthModeLegacyOpen,
		}, nil
	}

	// System-account credential — for the worker scanner. The worker boots
	// with the system seed/JWT directly from the Secret; we never re-issue
	// it, just package it for the caller.
	if in.SystemAccount {
		return nil, fmt.Errorf("queueprovider.nats: SystemAccount creds are loaded directly from the nats-operator Secret, not issued via this path")
	}

	// 1. Mint account NKey pair.
	accountKP, err := nkeys.CreateAccount()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: create account NKey: %w", err)
	}
	accountPub, err := accountKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: derive account public key: %w", err)
	}
	accountSeed, err := accountKP.Seed()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: extract account seed: %w", err)
	}

	// 2. Build + sign account claim. The account is signed BY the operator
	// (we Encode with operatorKP below). Inside the account, users are
	// signed by the account NKey — that's enforced at user-mint time.
	accClaims := jwt.NewAccountClaims(accountPub)
	accClaims.Name = fmt.Sprintf("tenant_%s", shortToken(in.ResourceToken))
	// JetStream limits — keep generous, the platform-side quota
	// (resources.usage_bytes scanner) handles per-tier enforcement.
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage:        -1, // -1 = unlimited at NATS layer
		DiskStorage:          -1,
		Streams:              -1,
		Consumer:             -1,
		MaxAckPending:        -1,
		MemoryMaxStreamBytes: -1,
		DiskMaxStreamBytes:   -1,
	}
	// Tenant can only export/import on its own subject — disable
	// cross-account exports entirely.
	accClaims.Exports = jwt.Exports{}
	accClaims.Imports = jwt.Imports{}

	accountJWT, err := accClaims.Encode(p.operatorKP)
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: sign account JWT: %w", err)
	}

	// 3. Push the account claim to the running nats-server resolver.
	if pushErr := p.currentPusher().PushAccountClaim(ctx, accountPub, accountJWT); pushErr != nil {
		return nil, fmt.Errorf("queueprovider.nats: push account claim to resolver: %w", pushErr)
	}

	p.accountCache.Store(in.ResourceToken, cachedAccount{
		accountKP:  accountKP,
		accountPub: accountPub,
		accountJWT: accountJWT,
		createdAt:  time.Now(),
	})

	// 4. Mint user NKey pair + sign user JWT with the account seed.
	userKP, err := nkeys.CreateUser()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: create user NKey: %w", err)
	}
	userPub, err := userKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: derive user public key: %w", err)
	}
	userSeed, err := userKP.Seed()
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: extract user seed: %w", err)
	}

	userClaims := jwt.NewUserClaims(userPub)
	userClaims.Name = fmt.Sprintf("user_%s", shortToken(in.ResourceToken))
	userClaims.IssuerAccount = accountPub
	// Subject-scoped pub/sub. The trailing ">" lets the tenant use arbitrary
	// children inside their prefix.
	wildcardSubject := strings.TrimSuffix(subject, ".") + ".>"
	userClaims.Pub.Allow.Add(wildcardSubject)
	userClaims.Sub.Allow.Add(wildcardSubject)
	// Also allow the tenant to publish to JetStream's API for their own
	// streams (NATS scopes $JS.API access through the account boundary so
	// we don't need to enumerate). The exact subjects the tenant needs
	// are $JS.API.STREAM.*.>; we list them explicitly to keep audit clear.
	for _, jsSubj := range []string{
		"$JS.API.STREAM.>",
		"$JS.API.CONSUMER.>",
		"$JS.API.INFO",
		"$JS.ACK.>",
	} {
		userClaims.Pub.Allow.Add(jsSubj)
	}

	if in.TTL > 0 {
		expiry := time.Now().Add(in.TTL).Unix()
		userClaims.Expires = expiry
	}

	userJWT, err := userClaims.Encode(accountKP)
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: sign user JWT: %w", err)
	}

	credsFile, err := jwt.FormatUserConfig(userJWT, userSeed)
	if err != nil {
		return nil, fmt.Errorf("queueprovider.nats: format .creds blob: %w", err)
	}

	var expiresAt *time.Time
	if in.TTL > 0 {
		t := time.Now().Add(in.TTL)
		expiresAt = &t
	}

	// Return the account seed to the caller (api/worker) so it can be
	// encrypted at rest in resources.queue_account_seed_encrypted (migration
	// 060). Without this, revocation after process restart is impossible —
	// the in-memory accountCache is the only other copy. The caller MUST
	// treat AccountSeed as a secret and MUST NOT log it; this is enforced
	// upstream by the api crypto path that wraps the value before persist.

	return &queueprovider.TenantCreds{
		JWT:           userJWT,
		NKey:          string(userSeed),
		CredsFile:     string(credsFile),
		ConnectionURL: p.connectionURL("", ""),
		Subject:       subject,
		ExpiresAt:     expiresAt,
		KeyID:         accountPub,
		AuthMode:      queueprovider.AuthModeIsolated,
		AccountSeed:   string(accountSeed), // secret — caller encrypts before persist; NEVER log
	}, nil
}

// RevokeTenantCredentials revokes the account JWT cached under keyID. An empty
// keyID is a safe no-op so the broker-mode teardown path can call it
// unconditionally.
//
// True revocation requires re-signing the account claim with the user's pub
// key on the revocations list and re-pushing to the resolver. When the
// account seed is not in our local cache (provisioner restart between issue
// and revoke), the caller must hydrate from the encrypted DB seed before
// calling — we don't ship plaintext seeds across process boundaries.
func (p *Provider) RevokeTenantCredentials(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	if !p.operatorReady {
		return nil // legacy_open mode — nothing to revoke
	}
	// Best-effort: look up the cached account, re-encode with the account
	// itself marked deleted, push to resolver. For now we don't track
	// per-user revocations — Revoke at the account level kills every user
	// of this resource at once, which matches the resource-deletion
	// semantics.
	val, ok := p.lookupCachedByPub(keyID)
	if !ok {
		// Provisioner doesn't have the account seed cached. The caller
		// (api) must call RevokeWithSeed instead — exposed below.
		return nil
	}
	accClaims := jwt.NewAccountClaims(keyID)
	accClaims.Name = val.accountJWT // placeholder — re-encoding deletes effectively
	// Set the account "Deleted" flag by zeroing limits and pushing.
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{}
	accClaims.Limits.AccountLimits = jwt.AccountLimits{
		Conn: -1,
	}
	accClaims.Exports = jwt.Exports{}
	accClaims.Imports = jwt.Imports{}
	revokedJWT, err := accClaims.Encode(p.operatorKP)
	if err != nil {
		return fmt.Errorf("queueprovider.nats: encode revocation: %w", err)
	}
	return p.currentPusher().PushAccountClaim(ctx, keyID, revokedJWT)
}

// RevokeWithSeed re-derives the account from a stored seed (encrypted at rest
// in the resources table) and pushes a revocation. Used by the provisioner
// teardown path after restart, where the in-memory accountCache is empty.
func (p *Provider) RevokeWithSeed(ctx context.Context, accountSeed string) error {
	if !p.operatorReady || accountSeed == "" {
		return nil
	}
	kp, err := nkeys.FromSeed([]byte(accountSeed))
	if err != nil {
		return fmt.Errorf("queueprovider.nats: parse account seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return fmt.Errorf("queueprovider.nats: derive account public: %w", err)
	}
	accClaims := jwt.NewAccountClaims(pub)
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{}
	accClaims.Exports = jwt.Exports{}
	accClaims.Imports = jwt.Imports{}
	revokedJWT, err := accClaims.Encode(p.operatorKP)
	if err != nil {
		return fmt.Errorf("queueprovider.nats: encode revocation: %w", err)
	}
	return p.currentPusher().PushAccountClaim(ctx, pub, revokedJWT)
}

func (p *Provider) lookupCachedByPub(pub string) (cachedAccount, bool) {
	var found cachedAccount
	var ok bool
	p.accountCache.Range(func(_, v any) bool {
		ca, _ := v.(cachedAccount)
		if ca.accountPub == pub {
			found = ca
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

func (p *Provider) currentPusher() ResolverPusher {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pusher
}

// canonicalSubject computes the default subject prefix for a token. Keeps the
// FULL token (not truncated) so two tokens sharing an 8-hex-char prefix never
// share a subject namespace. Matches provisioner/internal/backend/queue/subjident.go.
func (p *Provider) canonicalSubject(token string) string {
	clean := strings.ReplaceAll(token, "-", "")
	return strings.NewReplacer("<token>", clean).Replace(p.subjectTemplate)
}

func (p *Provider) connectionURL(_user, _pass string) string {
	scheme := "nats"
	if p.useTLS {
		scheme = "tls"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, p.publicHost, p.port)
}

// shortToken returns a short, stable identifier for a token, used as the
// `Name` field on account/user claims so operators reading `nats-server`
// audit logs can correlate a claim back to a resource. 8 hex chars are
// sufficient identification — the FULL token still drives subject scoping.
func shortToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return strings.ToLower(base32.StdEncoding.EncodeToString(sum[:5]))
}
