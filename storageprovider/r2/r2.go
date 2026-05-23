// Package r2 implements StorageCredentialProvider against Cloudflare R2.
//
// # What this backend can do
//
// R2 is the target migration: real per-tenant scoping at the IAM layer, no
// egress fees, soft cap of 1000 buckets per account (effectively unbounded
// for our scale). Two credential paths:
//
//   1. Long-lived API tokens, scoped to a bucket + prefix via the R2
//      API at /accounts/:id/r2/buckets/:bucket/keys. Issued when
//      IssueRequest.TTL == 0. Revocable by KeyID.
//
//   2. Short-lived "Temporary Credentials" via
//      /accounts/:id/r2/temp-access-credentials. Issued when TTL > 0.
//      Returns AccessKey + SecretKey + SessionToken with an absolute
//      ExpiresAt. Not revocable (let them expire).
//
// Either path produces a credential that ENFORCES the s3:prefix condition
// at the R2 IAM layer — true tenant isolation.
//
// # Required configuration
//
//   R2_ACCOUNT_ID                Cloudflare account id (33-char hex)
//   R2_API_TOKEN                 token with "Object Read & Write" + "Edit"
//                                permissions for the bucket
//   OBJECT_STORE_ENDPOINT        "<account>.r2.cloudflarestorage.com"
//   OBJECT_STORE_PUBLIC_URL      "https://r2.instanode.dev" (optional)
//   OBJECT_STORE_BUCKET          shared bucket name
//
// Region for R2 is always "auto".
package r2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"instant.dev/common/storageprovider"
)

// Name is the canonical backend identifier.
const Name = "r2"

// Provider implements StorageCredentialProvider for Cloudflare R2.
type Provider struct {
	endpoint     string
	publicURL    string
	bucket       string
	accountID    string
	apiToken     string
	httpClient   *http.Client
	masterKey    string
	masterSecret string
}

// New constructs an R2 provider. Returns an error when required configuration
// is missing.
func New(cfg storageprovider.Config) (storageprovider.StorageCredentialProvider, error) {
	if cfg.R2AccountID == "" {
		return nil, fmt.Errorf("r2: R2_ACCOUNT_ID is required")
	}
	if cfg.R2APIToken == "" {
		return nil, fmt.Errorf("r2: R2_API_TOKEN is required")
	}
	if cfg.MasterKey == "" || cfg.MasterSecret == "" {
		return nil, fmt.Errorf("r2: OBJECT_STORE_ACCESS_KEY + OBJECT_STORE_SECRET_KEY are required " +
			"for fallback / broker-mode presigning")
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = cfg.R2AccountID + ".r2.cloudflarestorage.com"
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = "instant-shared"
	}
	return &Provider{
		endpoint:     endpoint,
		publicURL:    cfg.PublicURL,
		bucket:       bucket,
		accountID:    cfg.R2AccountID,
		apiToken:     cfg.R2APIToken,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		masterKey:    cfg.MasterKey,
		masterSecret: cfg.MasterSecret,
	}, nil
}

// Name returns "r2".
func (p *Provider) Name() string { return Name }

// Capabilities reports what R2 can actually enforce.
//
//   - PrefixScopedKeys=true   → s3:prefix IS enforced in R2 IAM
//   - BucketScopedKeys=true
//   - STS=true                → /temp-access-credentials returns short-lived
//   - BucketPerTenant=true    → 1000 buckets/account, effectively unlimited
//   - MaxKeysPerAccount=0     → no documented hard cap on API tokens
func (p *Provider) Capabilities() storageprovider.Capabilities {
	return storageprovider.Capabilities{
		PrefixScopedKeys:  true,
		BucketScopedKeys:  true,
		STS:               true,
		BucketPerTenant:   true,
		ServerAccessLogs:  true,
		MaxKeysPerAccount: 0,
	}
}

// httpEndpoint allows tests to override the API base URL. Empty = use
// the real Cloudflare endpoint.
var httpEndpoint = ""

// SetAPIBaseForTest overrides the Cloudflare API base URL. Tests call this
// before driving a Provider through Issue / Revoke against a httptest.Server.
// Pass "" to restore the default. Not for production use.
func SetAPIBaseForTest(base string) { httpEndpoint = base }

func (p *Provider) apiBase() string {
	if httpEndpoint != "" {
		return httpEndpoint
	}
	return "https://api.cloudflare.com"
}

// r2KeyRequest is the body for POST /accounts/:id/r2/buckets/:bucket/keys.
type r2KeyRequest struct {
	Name        string                 `json:"name"`
	Permissions []string               `json:"permissions"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// r2KeyResponse is the response shape from Cloudflare's R2 keys endpoint.
type r2KeyResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
		KeyID           string `json:"id"`
	} `json:"result"`
}

// r2TempCredsRequest is the body for POST /accounts/:id/r2/temp-access-credentials.
type r2TempCredsRequest struct {
	Bucket      string   `json:"bucket"`
	Prefixes    []string `json:"prefixes,omitempty"`
	Permission  string   `json:"permission"`
	TTLSeconds  int      `json:"ttlSeconds"`
	ParentToken string   `json:"parentAccessKeyId,omitempty"`
}

// r2TempCredsResponse is the response shape from the temp-creds endpoint.
type r2TempCredsResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
		SessionToken    string `json:"sessionToken"`
		Expiration      string `json:"expiration"`
	} `json:"result"`
}

// IssueTenantCredentials mints a prefix-scoped credential for the request.
//
// TTL == 0  → long-lived API token, revocable by KeyID
// TTL > 0   → temporary STS-style credentials, not revocable (let them expire)
func (p *Provider) IssueTenantCredentials(ctx context.Context, in storageprovider.IssueRequest) (*storageprovider.TenantCreds, error) {
	prefix := strings.TrimSuffix(strings.TrimSpace(in.Prefix), "/")
	if prefix == "" {
		prefix = in.ResourceToken
	}
	bucket := in.Bucket
	if bucket == "" {
		bucket = p.bucket
	}

	if in.TTL > 0 {
		return p.issueTempCreds(ctx, in, bucket, prefix)
	}
	return p.issueLongLivedKey(ctx, in, bucket, prefix)
}

// issueLongLivedKey calls the R2 buckets/keys endpoint to mint a permanent
// prefix-scoped API token. Used for hobby/pro/team tiers that want a stable
// credential they can ship into their CI environments.
func (p *Provider) issueLongLivedKey(ctx context.Context, in storageprovider.IssueRequest, bucket, prefix string) (*storageprovider.TenantCreds, error) {
	body := r2KeyRequest{
		Name:        "instanode-" + in.ResourceToken,
		Permissions: []string{"object-read-write"},
		Parameters: map[string]interface{}{
			// R2 enforces this — calls outside the prefix return 403.
			"prefixes": []string{prefix + "/"},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("r2.issueLongLivedKey: marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/client/v4/accounts/%s/r2/buckets/%s/keys",
		p.apiBase(), p.accountID, bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("r2.issueLongLivedKey: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("r2.issueLongLivedKey: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("r2.issueLongLivedKey: %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed r2KeyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("r2.issueLongLivedKey: parse response: %w (body=%s)", err, string(respBody))
	}
	if !parsed.Success {
		return nil, fmt.Errorf("r2.issueLongLivedKey: api returned success=false: %+v", parsed.Errors)
	}

	slog.Info("r2.IssueTenantCredentials",
		"backend", Name,
		"pattern", "prefix-scoped-api-token",
		"token", in.ResourceToken,
		"bucket", bucket,
		"prefix", prefix,
		"key_id", parsed.Result.KeyID,
	)

	return &storageprovider.TenantCreds{
		AccessKey: parsed.Result.AccessKeyID,
		SecretKey: parsed.Result.SecretAccessKey,
		Endpoint:  p.customerEndpointURL(),
		Region:    "auto",
		Bucket:    bucket,
		Prefix:    prefix,
		ExpiresAt: nil,
		KeyID:     parsed.Result.KeyID,
	}, nil
}

// issueTempCreds calls the R2 temp-access-credentials endpoint to mint
// short-lived STS-style credentials. Used for anonymous / broker-mode where
// a long-lived key is overkill.
func (p *Provider) issueTempCreds(ctx context.Context, in storageprovider.IssueRequest, bucket, prefix string) (*storageprovider.TenantCreds, error) {
	body := r2TempCredsRequest{
		Bucket:      bucket,
		Prefixes:    []string{prefix + "/"},
		Permission:  "object-read-write",
		TTLSeconds:  int(in.TTL.Seconds()),
		ParentToken: p.masterKey,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("r2.issueTempCreds: marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/client/v4/accounts/%s/r2/temp-access-credentials",
		p.apiBase(), p.accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("r2.issueTempCreds: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("r2.issueTempCreds: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("r2.issueTempCreds: %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed r2TempCredsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("r2.issueTempCreds: parse response: %w (body=%s)", err, string(respBody))
	}
	if !parsed.Success {
		return nil, fmt.Errorf("r2.issueTempCreds: api returned success=false: %+v", parsed.Errors)
	}

	var expiresAt *time.Time
	if parsed.Result.Expiration != "" {
		if t, perr := time.Parse(time.RFC3339, parsed.Result.Expiration); perr == nil {
			expiresAt = &t
		}
	}

	slog.Info("r2.IssueTenantCredentials",
		"backend", Name,
		"pattern", "prefix-scoped-temp-credentials",
		"token", in.ResourceToken,
		"bucket", bucket,
		"prefix", prefix,
		"ttl_seconds", body.TTLSeconds,
	)

	return &storageprovider.TenantCreds{
		AccessKey:    parsed.Result.AccessKeyID,
		SecretKey:    parsed.Result.SecretAccessKey,
		SessionToken: parsed.Result.SessionToken,
		Endpoint:     p.customerEndpointURL(),
		Region:       "auto",
		Bucket:       bucket,
		Prefix:       prefix,
		ExpiresAt:    expiresAt,
		KeyID:        "", // temp creds aren't revocable
	}, nil
}

// RevokeTenantCredentials deletes the named R2 API token. No-op (returns nil)
// when keyID is empty (temp-creds case).
func (p *Provider) RevokeTenantCredentials(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	url := fmt.Sprintf("%s/client/v4/accounts/%s/r2/buckets/%s/keys/%s",
		p.apiBase(), p.accountID, p.bucket, keyID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("r2.RevokeTenantCredentials: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiToken)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("r2.RevokeTenantCredentials: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Key already gone — idempotent.
		return nil
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("r2.RevokeTenantCredentials: %d: %s", resp.StatusCode, string(respBody))
	}
	slog.Info("r2.RevokeTenantCredentials",
		"backend", Name,
		"key_id", keyID,
	)
	return nil
}

// MasterAccessKey returns the platform master key (used by the api for
// broker-mode presigning when capability-fallback hits a TTL-driven path).
func (p *Provider) MasterAccessKey() string { return p.masterKey }

// MasterSecretKey returns the platform master secret.
func (p *Provider) MasterSecretKey() string { return p.masterSecret }

// Endpoint returns the configured S3 endpoint (host[:port], no scheme).
func (p *Provider) Endpoint() string { return p.endpoint }

// Bucket returns the shared bucket name.
func (p *Provider) Bucket() string { return p.bucket }

// PublicURL returns the customer-facing URL prefix (with scheme).
func (p *Provider) PublicURL() string {
	if p.publicURL != "" {
		return p.publicURL
	}
	return p.customerEndpointURL()
}

func (p *Provider) customerEndpointURL() string {
	if p.publicURL != "" {
		return p.publicURL
	}
	host := p.endpoint
	if strings.Contains(host, "://") {
		return host
	}
	return "https://" + host
}

// ErrR2Unavailable is returned when the R2 API is unreachable. Distinct from
// a generic error so callers can decide whether to fail open or hard-deny.
var ErrR2Unavailable = errors.New("r2: api unreachable")

func init() {
	storageprovider.Register(Name, New)
}
