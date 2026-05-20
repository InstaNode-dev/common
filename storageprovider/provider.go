// Package storageprovider defines the cloud-agnostic interface for issuing
// per-tenant object-storage credentials.
//
// # Why this package exists
//
// Today's POST /storage/new is bound to DO Spaces' shared-master-key model
// (every tenant gets the same access key + a prefix-by-convention). To migrate
// to Cloudflare R2 or AWS S3 — both of which DO support real per-tenant
// scoping — without rewriting the api, we extract the credential-issuance
// surface into an interface and one implementation per backend.
//
// Each implementation reports what isolation it CAN do via Capabilities(),
// and the api's POST /storage/new consults that to decide whether to:
//
//   1. issue a per-tenant prefix-scoped key (R2, S3, MinIO)
//   2. mint a dedicated bucket per paying tenant (R2, S3 — paid tiers)
//   3. fall back to BROKER MODE: no long-lived credential is handed out,
//      the tenant calls POST /storage/:token/presign to mint short-lived
//      presigned URLs on demand (DO Spaces today — no real isolation
//      available, so the master key never leaves the api)
//
// Switching backends = flipping OBJECT_STORE_BACKEND + a data migration; no
// application code changes.
//
// Lives in `common` so api + worker can share the same interface (worker's
// storage scanner also needs to construct backend clients to enumerate bytes).
package storageprovider

import (
	"context"
	"errors"
	"time"
)

// StorageCredentialProvider issues per-tenant scoped credentials against an
// S3-compatible object store. Implementations exist for DO Spaces, R2, AWS S3,
// and MinIO; the api selects one at boot via Factory(cfg).
//
// All methods are safe for concurrent use across goroutines.
type StorageCredentialProvider interface {
	// IssueTenantCredentials creates a tenant-scoped credential for the given
	// resource token. May return long-lived keys (TTL=0) or short-lived
	// STS tokens (TTL>0) depending on backend capability + caller request.
	IssueTenantCredentials(ctx context.Context, in IssueRequest) (*TenantCreds, error)

	// RevokeTenantCredentials revokes a previously-issued credential by its
	// backend-specific KeyID (returned in TenantCreds at issuance time).
	// Called on resource deletion or rotation. No-op for STS / broker creds.
	RevokeTenantCredentials(ctx context.Context, keyID string) error

	// Capabilities returns what isolation the backend actually provides.
	// Callers consult this to decide whether to expose a credential, mint
	// a dedicated bucket, or fall back to broker mode.
	Capabilities() Capabilities

	// Name returns a stable identifier ("do-spaces", "r2", "s3", "minio").
	// Used in logs, audit events, and resource metadata.
	Name() string
}

// IssueRequest carries the parameters for IssueTenantCredentials.
type IssueRequest struct {
	// ResourceToken is the tenant-owned token (resource.token, UUID-formatted).
	// Used to name the backend identity (IAM user / R2 API token / S3 session
	// id) so backends with a name-based credential model can reverse-map
	// from a token to the credential it minted.
	ResourceToken string

	// Bucket is the tenant's bucket (in BucketPerTenant mode) OR the shared
	// bucket. Empty = let the provider pick the shared default.
	Bucket string

	// Prefix is the tenant's key prefix within Bucket (no trailing slash).
	// Empty = let the provider compute from ResourceToken.
	Prefix string

	// TTL controls credential lifetime:
	//   0   → long-lived (Pattern B: per-tenant IAM user / R2 API token)
	//   >0  → short-lived (Pattern C: AssumeRole / R2 Temp Credentials)
	//
	// Backends without STS capability ignore TTL (always long-lived).
	TTL time.Duration
}

// TenantCreds is the credential set returned to a tenant.
type TenantCreds struct {
	// AccessKey is the access-key-id (e.g. "AKIAEXAMPLE", "key_abc123").
	AccessKey string

	// SecretKey is the secret access key.
	SecretKey string

	// SessionToken is the STS session token. Empty unless TTL>0 was requested
	// AND Capabilities().STS is true.
	SessionToken string

	// Endpoint is the S3-compatible endpoint URL (e.g. "https://nyc3.digitaloceanspaces.com").
	Endpoint string

	// Region is the bucket region ("nyc3", "auto" for R2, "us-east-1").
	Region string

	// Bucket is the bucket the tenant has access to.
	Bucket string

	// Prefix is the slash-free key prefix the credential is scoped to.
	// Tenants are expected to prepend "<Prefix>/" to every object key.
	Prefix string

	// ExpiresAt is the credential expiry. Nil = long-lived.
	ExpiresAt *time.Time

	// KeyID is the backend-specific identifier used by RevokeTenantCredentials.
	// For IAM-style backends this is the access-key-id; for R2 it is the API
	// token id; for STS sessions it is empty (no revoke needed).
	KeyID string
}

// Capabilities describes what isolation a backend can ENFORCE.
//
// Callers MUST consult this before deciding how to respond to /storage/new —
// surfacing a long-lived credential when PrefixScopedKeys is false means the
// tenant could read sibling tenants' objects, which is the failure class this
// abstraction exists to eliminate.
type Capabilities struct {
	// PrefixScopedKeys = the backend can ENFORCE an s3:prefix condition
	// in IAM/policy so a tenant's key can only see its own prefix.
	// (R2, S3, MinIO: true. DO Spaces: false — s3:prefix is silently ignored.)
	PrefixScopedKeys bool

	// BucketScopedKeys = the backend can issue a key scoped to a single
	// bucket (no prefix enforcement). Useful for BucketPerTenant flows.
	BucketScopedKeys bool

	// STS = the backend supports short-lived AssumeRole / temporary
	// credentials. Returned in TenantCreds.SessionToken.
	STS bool

	// BucketPerTenant = the backend can cheaply create one bucket per tenant.
	// Set true for backends with effectively-unbounded bucket counts (S3,
	// R2); false for DO Spaces (~100 buckets/account hard cap).
	BucketPerTenant bool

	// ServerAccessLogs = the backend can deliver per-object access logs
	// (e.g. S3 server-access logs, R2 access logs). Informational; not used
	// for routing.
	ServerAccessLogs bool

	// MaxKeysPerAccount is the hard cap on the number of access keys a single
	// platform account can mint. 0 = unbounded. Used by callers to decide
	// whether to recycle / pool keys.
	MaxKeysPerAccount int
}

// ErrNotImplemented is returned by stub providers (e.g. S3Provider before
// AWS credentials are wired) so callers can detect and degrade.
var ErrNotImplemented = errors.New("storageprovider: not implemented")

// ErrUnknownBackend is returned by Factory when OBJECT_STORE_BACKEND is set
// to a value that does not match any registered provider.
var ErrUnknownBackend = errors.New("storageprovider: unknown backend (valid: do-spaces, r2, s3, minio)")
