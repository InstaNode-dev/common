package storageprovider

import (
	"fmt"
	"strings"
)

// Config is the operator-facing configuration for the storage backend. The
// api wires this from env vars (OBJECT_STORE_* + R2_* + AWS_*) and passes it
// to Factory() at boot. Each provider documents which fields it requires.
type Config struct {
	// Backend selects the implementation. One of: "do-spaces", "r2", "s3",
	// "minio". Aliases ("digitalocean", "spaces") collapse to "do-spaces";
	// "cloudflare" → "r2"; "aws" → "s3"; "admin" / "iam" → "minio". Empty
	// or unknown values land on "minio" — the safest local-dev default.
	Backend string

	// Shared S3-compatible knobs (all backends).
	Endpoint    string // host or host:port (no scheme)
	PublicURL   string // customer-facing URL (with scheme), falls back to Endpoint
	Region      string // "nyc3", "auto", "us-east-1"
	Bucket      string // shared bucket name; default "instant-shared"
	MasterKey   string // OBJECT_STORE_ACCESS_KEY (root credential)
	MasterSecret string // OBJECT_STORE_SECRET_KEY (root credential)
	UseTLS      bool   // true for DO Spaces / R2 / S3; false for in-cluster MinIO

	// R2-specific.
	R2AccountID string // CF_ACCOUNT_ID / R2_ACCOUNT_ID
	R2APIToken  string // R2_API_TOKEN — required for IssueTenantCredentials

	// S3-specific.
	AWSRoleARN string // IAM role to AssumeRole into for per-tenant sessions

	// MinIO-specific (alias of MasterKey/MasterSecret but operators sometimes
	// supply MINIO_ROOT_USER / MINIO_ROOT_PASSWORD instead).
	MinIORootUser     string
	MinIORootPassword string
}

// NormalizeBackend maps the operator-facing value (with all the historical
// aliases) onto one of the four canonical backend strings.
func NormalizeBackend(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "do-spaces", "do_spaces", "dospaces", "do", "digitalocean", "spaces":
		return "do-spaces"
	case "r2", "cloudflare", "cf-r2", "cloudflare-r2":
		return "r2"
	case "s3", "aws", "aws-s3":
		return "s3"
	case "minio", "minio-admin", "admin", "iam":
		return "minio"
	default:
		return ""
	}
}

// Factory selects and constructs the right StorageCredentialProvider for cfg.
// Returns ErrUnknownBackend when cfg.Backend is unrecognised, so the caller
// can fail loudly instead of silently degrading to a less-secure backend.
//
// To keep `common` zero-dep on cloud SDKs (so import-graph stays cheap for
// every consumer), the actual provider implementations live in subpackages
// that register themselves via init(). Factory consults the global registry
// populated by those inits.
func Factory(cfg Config) (StorageCredentialProvider, error) {
	name := NormalizeBackend(cfg.Backend)
	if name == "" {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, cfg.Backend)
	}
	ctor, ok := lookupBuilder(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q (no implementation registered — did you import the impl package?)", ErrUnknownBackend, name)
	}
	return ctor(cfg)
}

// Builder is the constructor signature every backend implementation
// registers with the global registry via Register. The api / worker import
// the impl subpackages they want available — that way `common` stays free of
// cloud-SDK transitive deps for tooling that doesn't need them.
type Builder func(cfg Config) (StorageCredentialProvider, error)

var builders = map[string]Builder{}

// Register adds a Builder under name. Called from each provider package's
// init(). Idempotent — a second registration with the same name silently
// overwrites the first (used in tests to inject a fake).
func Register(name string, b Builder) {
	builders[NormalizeBackend(name)] = b
}

func lookupBuilder(name string) (Builder, bool) {
	b, ok := builders[name]
	return b, ok
}

// ListRegistered returns the names of every backend currently registered.
// Used by the registry-iterating contract test.
func ListRegistered() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, k)
	}
	return out
}
