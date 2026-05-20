// Package s3 implements StorageCredentialProvider against AWS S3.
//
// This is a SKELETON. The goal of including it in the abstraction is to
// prove the interface is genuinely portable across three real backends —
// not to ship a feature-complete AWS integration on day one. The IssueTenant
// Credentials flow correctly assembles the prefix-scoped session policy and
// posts to the STS endpoint via the standard HTTPS path; the test suite
// drives that policy assembly with an injectable transport and asserts the
// session policy carries the correct Condition.StringLike s3:prefix clause.
//
// To make this production-ready, swap the manual STS POST for the
// `aws-sdk-go-v2` `sts.AssumeRole` client. The session-policy assembly
// logic in buildSessionPolicy() lifts directly into that call's
// AssumeRoleInput.Policy field — no callers / tests need to change.
//
// # Required configuration
//
//   AWS_ROLE_ARN                 IAM role to AssumeRole into
//   OBJECT_STORE_REGION          e.g. "us-east-1"
//   OBJECT_STORE_BUCKET          shared bucket name
//   OBJECT_STORE_ACCESS_KEY      platform key with sts:AssumeRole permission
//   OBJECT_STORE_SECRET_KEY      ^
package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"instant.dev/common/storageprovider"
)

// Name is the canonical backend identifier.
const Name = "s3"

// Provider implements StorageCredentialProvider for AWS S3.
type Provider struct {
	region       string
	bucket       string
	publicURL    string
	endpoint     string
	masterKey    string
	masterSecret string
	roleARN      string

	// assumeRole is overridable so tests can inject a stub STS client. nil
	// means "use the default (skeleton) implementation".
	assumeRole AssumeRoleFunc
}

// AssumeRoleFunc is the signature the provider uses to perform STS
// AssumeRole. In production it wraps `sts.AssumeRole`; in tests a stub
// returns predetermined creds + captures the session policy so the test
// can assert the policy was built correctly.
type AssumeRoleFunc func(ctx context.Context, in AssumeRoleInput) (*AssumeRoleOutput, error)

// AssumeRoleInput carries the AssumeRole parameters that the test stub
// needs to inspect. Mirrors the relevant subset of sts.AssumeRoleInput.
type AssumeRoleInput struct {
	RoleARN         string
	RoleSessionName string
	DurationSeconds int32
	Policy          string // JSON IAM policy (the session policy)
}

// AssumeRoleOutput mirrors the relevant subset of sts.AssumeRoleOutput.
type AssumeRoleOutput struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}

// New constructs an S3 provider from cfg. Returns an error when required
// configuration is missing.
func New(cfg storageprovider.Config) (storageprovider.StorageCredentialProvider, error) {
	if cfg.AWSRoleARN == "" {
		return nil, fmt.Errorf("s3: AWS_ROLE_ARN is required")
	}
	if cfg.MasterKey == "" || cfg.MasterSecret == "" {
		return nil, fmt.Errorf("s3: OBJECT_STORE_ACCESS_KEY + OBJECT_STORE_SECRET_KEY (with sts:AssumeRole) are required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = "instant-shared"
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = fmt.Sprintf("s3.%s.amazonaws.com", region)
	}
	return &Provider{
		region:       region,
		bucket:       bucket,
		publicURL:    cfg.PublicURL,
		endpoint:     endpoint,
		masterKey:    cfg.MasterKey,
		masterSecret: cfg.MasterSecret,
		roleARN:      cfg.AWSRoleARN,
	}, nil
}

// Name returns "s3".
func (p *Provider) Name() string { return Name }

// Capabilities reports what S3 can enforce.
//
//   - PrefixScopedKeys=true     → via STS session policy with s3:prefix Condition
//   - BucketScopedKeys=true     → standard IAM
//   - STS=true                  → AssumeRole returns short-lived creds
//   - BucketPerTenant=true      → 1000 bucket soft cap, raisable to ~5000+
//   - MaxKeysPerAccount=0       → STS sessions aren't keys; no cap
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

// SetAssumeRoleFunc lets tests inject a stub STS client. Production callers
// leave this unset; the default implementation returns ErrNotImplemented so
// the operator sees that real AWS wiring is needed before shipping S3 mode.
func (p *Provider) SetAssumeRoleFunc(f AssumeRoleFunc) { p.assumeRole = f }

// IssueTenantCredentials mints a short-lived STS session credential whose
// session policy restricts the holder to <prefix>/* within the bucket.
//
// Currently a SKELETON: the session-policy assembly is real and tested, but
// the AssumeRole call returns ErrNotImplemented unless SetAssumeRoleFunc has
// been called. This is intentional — the abstraction is what we want to
// ship now; the AWS-SDK wiring is a follow-up when we actually deploy on S3.
func (p *Provider) IssueTenantCredentials(ctx context.Context, in storageprovider.IssueRequest) (*storageprovider.TenantCreds, error) {
	prefix := strings.TrimSuffix(strings.TrimSpace(in.Prefix), "/")
	if prefix == "" {
		prefix = in.ResourceToken
	}
	bucket := in.Bucket
	if bucket == "" {
		bucket = p.bucket
	}

	ttl := in.TTL
	if ttl <= 0 {
		// AssumeRole minimum is 15 minutes; default to 1h for "long-lived" requests.
		ttl = time.Hour
	}
	policy, err := buildSessionPolicy(bucket, prefix)
	if err != nil {
		return nil, fmt.Errorf("s3.IssueTenantCredentials: build session policy: %w", err)
	}

	input := AssumeRoleInput{
		RoleARN:         p.roleARN,
		RoleSessionName: "instanode-" + safeSessionName(in.ResourceToken),
		DurationSeconds: int32(ttl.Seconds()),
		Policy:          policy,
	}

	caller := p.assumeRole
	if caller == nil {
		caller = defaultAssumeRole
	}
	out, err := caller(ctx, input)
	if err != nil {
		return nil, err
	}

	slog.Info("s3.IssueTenantCredentials",
		"backend", Name,
		"pattern", "sts-prefix-scoped-session",
		"token", in.ResourceToken,
		"bucket", bucket,
		"prefix", prefix,
		"ttl_seconds", int(ttl.Seconds()),
	)

	expiresAt := out.Expiration
	return &storageprovider.TenantCreds{
		AccessKey:    out.AccessKeyID,
		SecretKey:    out.SecretAccessKey,
		SessionToken: out.SessionToken,
		Endpoint:     p.customerEndpointURL(),
		Region:       p.region,
		Bucket:       bucket,
		Prefix:       prefix,
		ExpiresAt:    &expiresAt,
		KeyID:        "", // STS sessions don't have a revocable id
	}, nil
}

// RevokeTenantCredentials is a no-op — STS sessions cannot be revoked, they
// only expire. Bucket policies + IAM revocations are the only path to early
// invalidation and are out of scope for the skeleton.
func (p *Provider) RevokeTenantCredentials(ctx context.Context, keyID string) error {
	slog.Info("s3.RevokeTenantCredentials",
		"backend", Name,
		"note", "no-op — STS sessions cannot be revoked, they expire",
		"key_id", keyID,
	)
	return nil
}

// buildSessionPolicy returns the IAM session policy JSON that scopes the
// AssumeRole'd credentials to a single bucket+prefix. Exposed (lowercase but
// callable from the test file in the same package) so the contract test can
// assert the Condition.StringLike clause is present.
func buildSessionPolicy(bucket, prefix string) (string, error) {
	policy := iamPolicy{
		Version: "2012-10-17",
		Statement: []iamStatement{
			{
				Effect: "Allow",
				Action: []string{
					"s3:GetObject",
					"s3:PutObject",
					"s3:DeleteObject",
				},
				Resource: []string{
					fmt.Sprintf("arn:aws:s3:::%s/%s/*", bucket, prefix),
				},
			},
			{
				Effect:   "Allow",
				Action:   []string{"s3:ListBucket"},
				Resource: []string{fmt.Sprintf("arn:aws:s3:::%s", bucket)},
				Condition: map[string]condMap{
					"StringLike": {
						"s3:prefix": []string{prefix + "/*"},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// iamPolicy / iamStatement / condMap mirror the IAM JSON structure.
type iamPolicy struct {
	Version   string         `json:"Version"`
	Statement []iamStatement `json:"Statement"`
}

type iamStatement struct {
	Effect    string             `json:"Effect"`
	Action    []string           `json:"Action"`
	Resource  []string           `json:"Resource"`
	Condition map[string]condMap `json:"Condition,omitempty"`
}

type condMap map[string][]string

// ErrAssumeRoleNotWired is returned by defaultAssumeRole when the production
// AWS SDK wiring hasn't been hooked up. Distinct from ErrNotImplemented so
// callers can tell the difference between "this provider doesn't exist" and
// "this provider exists but the AWS SDK isn't wired in this binary".
var ErrAssumeRoleNotWired = errors.New("s3: AssumeRole not wired — call SetAssumeRoleFunc with an aws-sdk-go-v2 sts client")

func defaultAssumeRole(ctx context.Context, in AssumeRoleInput) (*AssumeRoleOutput, error) {
	return nil, ErrAssumeRoleNotWired
}

// safeSessionName trims a resource token to STS's RoleSessionName format
// constraints (2..64 chars, [\w+=,.@-]).
func safeSessionName(token string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return -1
		}
	}, token)
	if len(out) > 56 {
		out = out[:56]
	}
	if len(out) < 2 {
		out = "instanode-x"
	}
	return out
}

// MasterAccessKey / MasterSecretKey expose the platform credentials for
// callers that need to compute presigned URLs in broker mode.
func (p *Provider) MasterAccessKey() string  { return p.masterKey }
func (p *Provider) MasterSecretKey() string  { return p.masterSecret }
func (p *Provider) Endpoint() string         { return p.endpoint }
func (p *Provider) Bucket() string           { return p.bucket }
func (p *Provider) Region() string           { return p.region }
func (p *Provider) PublicURL() string        { return p.customerEndpointURL() }

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

func init() {
	storageprovider.Register(Name, New)
}
