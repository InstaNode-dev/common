// Package queueprovider defines the message-broker-agnostic interface for
// issuing per-tenant queue credentials.
//
// # Why this package exists
//
// NATS today (2026-05-20) runs unauthenticated in `instant-data`. Any pod in
// the cluster — including any customer container we deploy via /deploy/new —
// can dial `nats://nats.instant-data.svc.cluster.local:4222` and read/write
// every other tenant's subjects and JetStream streams. The "subject prefix
// derived from token" pattern is naming convention, not isolation.
//
// This package abstracts credential issuance the same way `common/storage
// provider` abstracts object-storage credential issuance: one interface, four
// implementations, a factory selected by env var. Today the wire is NATS in
// operator mode (per-tenant accounts with signed user JWTs). When a future
// migration to RabbitMQ Streams or Kafka happens, only one new package
// implementing the interface + one factory line changes — handler code is
// untouched.
//
// Each implementation reports what isolation it CAN enforce via Capabilities(),
// so the api's POST /queue/new can degrade safely on backends without
// subject-level authorization.
//
// Lives in `common` so api + worker + provisioner share the same interface.
package queueprovider

import (
	"context"
	"errors"
	"time"
)

// QueueCredentialProvider issues per-tenant scoped credentials against a
// message-broker backend. Implementations exist for NATS (real), RabbitMQ
// (skeleton), and Kafka (skeleton). The api selects one at boot via
// Factory(cfg).
//
// All methods are safe for concurrent use across goroutines.
type QueueCredentialProvider interface {
	// IssueTenantCredentials creates a tenant-scoped credential for the given
	// resource token. May return long-lived creds (TTL=0) or short-lived
	// signed-JWT creds (TTL>0) depending on backend capability + caller
	// request.
	IssueTenantCredentials(ctx context.Context, in IssueRequest) (*TenantCreds, error)

	// RevokeTenantCredentials revokes a previously-issued credential by its
	// backend-specific KeyID (returned in TenantCreds at issuance time).
	// Called on resource deletion or rotation. Empty keyID is a safe no-op
	// so the broker-mode teardown path can call it unconditionally.
	RevokeTenantCredentials(ctx context.Context, keyID string) error

	// Capabilities returns what isolation the backend actually provides.
	// Callers consult this to decide whether to expose a credential or fall
	// back to a broker-mediated pattern.
	Capabilities() Capabilities

	// Name returns a stable identifier ("nats", "rabbitmq", "kafka",
	// "legacy_open"). Used in logs, audit events, and resource metadata.
	Name() string
}

// IssueRequest carries the parameters for IssueTenantCredentials.
type IssueRequest struct {
	// ResourceToken is the tenant-owned token (resource.token, UUID-formatted).
	// Used to name the backend identity (NATS account name / RabbitMQ vhost /
	// Kafka principal) so backends with a name-based credential model can
	// reverse-map from a token to the credential it minted.
	ResourceToken string

	// Subject is the subject prefix the tenant is scoped to. The backend
	// MUST enforce that this tenant can only publish/subscribe under this
	// prefix. Conventional value: "tenant_<token>." — see
	// queueprovider/nats/subject.go for canonical derivation.
	Subject string

	// TTL controls credential lifetime:
	//   0   → long-lived (account/user lives until Revoke is called)
	//   >0  → short-lived signed user JWT with embedded expiry
	//
	// Backends without per-credential TTL ignore this (always long-lived).
	TTL time.Duration

	// SystemAccount is true when the caller wants a credential bound to the
	// platform's system account rather than a tenant account. Used by the
	// worker scanner to enumerate every tenant's streams for quota
	// accounting. Most provisioning paths set this false.
	SystemAccount bool
}

// TenantCreds is the credential set returned to a tenant.
//
// Different flavors populate different fields:
//   - basic-auth flavor (e.g. RabbitMQ): Username + Password
//   - JWT/NKey flavor (NATS accounts model): JWT + NKey
//   - both: ConnectionURL pre-built with the right scheme + creds embedded
//     so the caller doesn't have to know which flavor was minted.
type TenantCreds struct {
	// Username for basic-auth flavor. Empty for JWT/NKey flavor.
	Username string

	// Password for basic-auth flavor. Empty for JWT/NKey flavor.
	Password string

	// JWT is the signed user JWT (NATS accounts model). Base64-encoded
	// JWT, RFC 7519 compact form.
	JWT string

	// NKey is the user NKey seed (NATS accounts model). Format: "SU..." —
	// a base32-encoded 64-byte seed. Treated as a secret.
	NKey string

	// CredsFile is the canonical NATS `.creds` blob containing both the JWT
	// and the seed, ready to be written to disk by the client. Optional —
	// when non-empty, clients can ignore JWT + NKey and pass this file path
	// to `nats.UserCredentials(path)`.
	CredsFile string

	// ConnectionURL is the pre-built broker URL. For basic-auth flavor:
	//   nats://<user>:<pass>@<host>:4222
	// For JWT flavor:
	//   nats://<host>:4222  (caller passes JWT/NKey out-of-band)
	ConnectionURL string

	// Subject is the resolved subject prefix (echoes IssueRequest.Subject or
	// the canonical default the provider chose).
	Subject string

	// ExpiresAt is the credential expiry. Nil = long-lived.
	ExpiresAt *time.Time

	// KeyID is the backend-specific identifier used by RevokeTenantCredentials.
	// For NATS this is the account public key ("A..." NKey).
	// For RabbitMQ this is the username.
	// For Kafka this is the principal name.
	KeyID string

	// AuthMode is the resource's auth_mode column value:
	//   "isolated"     — real per-tenant credential, the default
	//   "legacy_open"  — grandfathered pre-cutover row, no credential
	//
	// Echoed in the API response so the caller knows whether isolation is
	// actually being enforced for this resource.
	AuthMode string

	// AccountSeed is the NATS account NKey seed (operator-mode backends only)
	// — required for revocation after process restart. The api MUST encrypt
	// this value at rest (AES-256-GCM keyring, same path as connection_url)
	// and persist it in the resources.queue_account_seed_encrypted column
	// (migration 060). On teardown the api/worker decrypts and passes the
	// seed back to RevokeWithSeed so the account claim can be re-signed and
	// pushed to the resolver even after the in-memory accountCache has been
	// lost to a pod restart.
	//
	// Treated as a secret. NEVER log this field — it is a private NKey seed
	// (format "SA...") that grants account-level signing authority. Backends
	// that don't use NKey/JWT (RabbitMQ, Kafka skeletons, legacy_open) leave
	// this empty.
	AccountSeed string
}

// Capabilities describes what isolation a backend can ENFORCE.
//
// Callers MUST consult this before deciding how to respond to /queue/new —
// surfacing a long-lived credential when SubjectScopedAuth is false means the
// tenant could read sibling tenants' subjects, which is the failure class this
// abstraction exists to eliminate.
type Capabilities struct {
	// PerTenantAccounts = the backend supports a true per-tenant account
	// model (NATS accounts; not just per-user creds). Implies completely
	// separate JetStream namespaces, subject namespaces, etc.
	PerTenantAccounts bool

	// SubjectScopedAuth = the backend can ENFORCE pub/sub permissions
	// scoped to a subject prefix. (NATS: true. RabbitMQ: limited.
	// Kafka: ACL-based, true.)
	SubjectScopedAuth bool

	// BasicAuth = the backend supports username/password authentication.
	// Most backends do; NATS supports it but the modern path is JWT.
	BasicAuth bool

	// StreamIsolation = JetStream / queue streams are isolated between
	// tenants by the auth model. True iff PerTenantAccounts OR
	// SubjectScopedAuth ENFORCES stream-level isolation.
	StreamIsolation bool
}

// AuthModeIsolated is the per-tenant-credential auth mode (default for new
// provisions after the operator-mode cutover).
const AuthModeIsolated = "isolated"

// AuthModeLegacyOpen is the grandfathered no-auth mode (pre-cutover queues).
// Resources in this mode keep working unauthenticated until they recycle.
// New provisions never use this mode.
const AuthModeLegacyOpen = "legacy_open"

// ErrNotImplemented is returned by stub providers (e.g. rabbitmq, kafka before
// they're wired) so callers can detect and degrade.
var ErrNotImplemented = errors.New("queueprovider: not implemented")

// ErrUnknownBackend is returned by Factory when QUEUE_BACKEND is set to a
// value that does not match any registered provider.
var ErrUnknownBackend = errors.New("queueprovider: unknown backend (valid: nats, rabbitmq, kafka, legacy_open)")

// ErrAuthFailure is returned when a credential issuance fails because the
// backend rejected the operator/system credential — usually a sign that the
// operator seed in the k8s Secret is mismatched against the running
// nats-server's operator JWT. Counted in nats_auth_failures_total.
var ErrAuthFailure = errors.New("queueprovider: backend auth failure (operator/system credential rejected)")
