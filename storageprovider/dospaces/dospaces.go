// Package dospaces implements StorageCredentialProvider against DigitalOcean
// Spaces — InstaNode's current object-storage backend.
//
// # What this backend can and cannot do
//
// DO Spaces is S3-API-compatible at the data plane but NOT at the IAM plane.
// As of 2026-05-20:
//
//   - There is NO portable per-tenant IAM-user API. Spaces "Access Keys" are
//     account-wide; the only scoping the dashboard exposes is "all buckets"
//     vs "this bucket" (bucket-scoped keys, GA Jan 2025). There is no
//     supported way to enforce `s3:prefix` for a key — the condition is
//     silently no-op'd.
//   - There is a soft cap of ~100 buckets per account, so BucketPerTenant
//     is impractical at platform scale.
//   - There is no STS / temporary-credentials endpoint.
//
// Consequence: every tenant that lands here CANNOT receive a long-lived
// credential safely — they would each hold a key that can read every
// sibling's objects. The api therefore uses Capabilities() to route
// /storage/new responses for DO Spaces into BROKER MODE: the credential
// stays in the api process and the tenant calls /storage/:token/presign for
// short-lived presigned URLs.
//
// IssueTenantCredentials still works (returns the master key) so legacy
// tenants provisioned before the abstraction shipped keep their existing
// connection_url. The api annotates the response with `mode=shared-master-key`
// so ops sees what isolation is in effect.
package dospaces

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"instant.dev/common/storageprovider"
)

// Name is the canonical backend identifier.
const Name = "do-spaces"

// Provider is the DO Spaces implementation.
//
// It carries the master access key + secret so it can hand them out in
// shared-master-key mode AND so the api can use them to compute presigned
// URLs for broker-mode access. The bucket is the platform-shared bucket
// (e.g. "instant-shared" in DO Spaces region nyc3).
type Provider struct {
	endpoint     string
	publicURL    string
	region       string
	bucket       string
	masterKey    string
	masterSecret string
	useTLS       bool
}

// New constructs a DO Spaces provider from cfg. Returns an error when the
// master credentials are missing — without them, even broker-mode presigning
// cannot work.
func New(cfg storageprovider.Config) (storageprovider.StorageCredentialProvider, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("dospaces: OBJECT_STORE_ENDPOINT is required (e.g. nyc3.digitaloceanspaces.com)")
	}
	if cfg.MasterKey == "" || cfg.MasterSecret == "" {
		return nil, fmt.Errorf("dospaces: OBJECT_STORE_ACCESS_KEY + OBJECT_STORE_SECRET_KEY are required")
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = "instant-shared"
	}
	region := cfg.Region
	if region == "" {
		region = "nyc3"
	}
	return &Provider{
		endpoint:     endpoint,
		publicURL:    cfg.PublicURL,
		region:       region,
		bucket:       bucket,
		masterKey:    cfg.MasterKey,
		masterSecret: cfg.MasterSecret,
		useTLS:       cfg.UseTLS,
	}, nil
}

// Name returns "do-spaces".
func (p *Provider) Name() string { return Name }

// Capabilities — honest about what DO Spaces actually provides.
//
//   - PrefixScopedKeys=false  → tenants land in broker mode
//   - BucketScopedKeys=true   → Jan-2025 GA feature, still requires admin API
//   - STS=false               → no temp-credentials endpoint
//   - BucketPerTenant=false   → ~100 bucket soft cap per account
//   - MaxKeysPerAccount=200   → documented soft cap
func (p *Provider) Capabilities() storageprovider.Capabilities {
	return storageprovider.Capabilities{
		PrefixScopedKeys:  false,
		BucketScopedKeys:  true,
		STS:               false,
		BucketPerTenant:   false,
		ServerAccessLogs:  false,
		MaxKeysPerAccount: 200,
	}
}

// IssueTenantCredentials returns the platform's master key + the tenant's
// computed prefix. This is the historical "shared-master-key" behaviour and
// is INSECURE in the cross-tenant sense — kept only so legacy tenants
// continue to work. New /storage/new responses route DO Spaces tenants to
// broker mode instead of calling this.
//
// Logs `pattern=shared-master-key` on every call so ops can see at a glance
// what isolation tenants are actually getting.
func (p *Provider) IssueTenantCredentials(ctx context.Context, in storageprovider.IssueRequest) (*storageprovider.TenantCreds, error) {
	prefix := strings.TrimSuffix(strings.TrimSpace(in.Prefix), "/")
	if prefix == "" {
		prefix = in.ResourceToken
	}
	bucket := in.Bucket
	if bucket == "" {
		bucket = p.bucket
	}
	endpoint := p.customerEndpointURL()

	slog.Info("dospaces.IssueTenantCredentials",
		"backend", Name,
		"pattern", "shared-master-key",
		"isolation", "prefix-by-convention-only",
		"token", in.ResourceToken,
		"bucket", bucket,
		"prefix", prefix,
	)

	return &storageprovider.TenantCreds{
		AccessKey: p.masterKey,
		SecretKey: p.masterSecret,
		Endpoint:  endpoint,
		Region:    p.region,
		Bucket:    bucket,
		Prefix:    prefix,
		ExpiresAt: nil, // long-lived
		KeyID:     "",  // master key — no per-tenant id to revoke
	}, nil
}

// RevokeTenantCredentials is a no-op on DO Spaces — there is no per-tenant
// IAM user to remove. Logged so ops sees cleanup is intentionally a no-op.
func (p *Provider) RevokeTenantCredentials(ctx context.Context, keyID string) error {
	slog.Info("dospaces.RevokeTenantCredentials",
		"backend", Name,
		"pattern", "shared-master-key",
		"key_id", keyID,
		"note", "no-op — master-key model has no per-tenant identity",
	)
	return nil
}

// MasterAccessKey returns the master access key. Exposed so the api can
// compute presigned URLs for broker-mode access without re-reading config.
func (p *Provider) MasterAccessKey() string { return p.masterKey }

// MasterSecretKey returns the master secret key.
func (p *Provider) MasterSecretKey() string { return p.masterSecret }

// Endpoint returns the configured S3 endpoint (host[:port], no scheme).
func (p *Provider) Endpoint() string { return p.endpoint }

// PublicURL returns the customer-facing public URL prefix, with scheme.
func (p *Provider) PublicURL() string {
	if p.publicURL != "" {
		return p.publicURL
	}
	return p.customerEndpointURL()
}

// Bucket returns the shared bucket name.
func (p *Provider) Bucket() string { return p.bucket }

// Region returns the configured region.
func (p *Provider) Region() string { return p.region }

// UseTLS reports whether the SDK should dial the endpoint over TLS.
func (p *Provider) UseTLS() bool { return p.useTLS }

// customerEndpointURL returns the URL form (with scheme) of the customer-
// facing endpoint, falling back to scheme-prefixing p.endpoint when no
// publicURL was configured. Used in TenantCreds.Endpoint.
func (p *Provider) customerEndpointURL() string {
	if p.publicURL != "" {
		return p.publicURL
	}
	scheme := "http"
	if p.useTLS {
		scheme = "https"
	}
	host := p.endpoint
	if i := strings.Index(host, "://"); i > 0 {
		// Already a URL.
		return host
	}
	return scheme + "://" + host
}

func init() {
	storageprovider.Register(Name, New)
}
