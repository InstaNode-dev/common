package s3_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/s3"
)

// TestS3_New_RequiresRoleARN — without AWS_ROLE_ARN we can't AssumeRole, so
// the constructor must hard-fail.
func TestS3_New_RequiresRoleARN(t *testing.T) {
	_, err := s3.New(storageprovider.Config{
		MasterKey: "k", MasterSecret: "s",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS_ROLE_ARN")
}

// TestS3_Capabilities — S3 is the most-capable backend.
func TestS3_Capabilities(t *testing.T) {
	p, err := s3.New(storageprovider.Config{
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "k",
		MasterSecret: "s",
	})
	require.NoError(t, err)
	caps := p.Capabilities()
	assert.True(t, caps.PrefixScopedKeys)
	assert.True(t, caps.STS)
	assert.True(t, caps.BucketPerTenant)
}

// TestS3_IssueTenantCredentials_PolicyCarriesPrefixCondition is the central
// contract test for S3 — it asserts that the session policy submitted to
// AssumeRole carries Condition.StringLike: s3:prefix = <token>/*. Without
// that condition, the issued session credentials could list the entire
// bucket, which is the cross-tenant boundary the migration to S3 is supposed
// to enforce.
func TestS3_IssueTenantCredentials_PolicyCarriesPrefixCondition(t *testing.T) {
	rawProvider, err := s3.New(storageprovider.Config{
		Region:       "us-east-1",
		Bucket:       "instant-shared",
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "k",
		MasterSecret: "s",
	})
	require.NoError(t, err)

	p := rawProvider.(*s3.Provider)

	var capturedPolicy string
	var capturedRoleARN string
	var capturedDuration int32
	p.SetAssumeRoleFunc(func(ctx context.Context, in s3.AssumeRoleInput) (*s3.AssumeRoleOutput, error) {
		capturedPolicy = in.Policy
		capturedRoleARN = in.RoleARN
		capturedDuration = in.DurationSeconds
		return &s3.AssumeRoleOutput{
			AccessKeyID:     "AK_STS",
			SecretAccessKey: "SK_STS",
			SessionToken:    "TOK_STS",
			Expiration:      time.Now().Add(15 * time.Minute),
		}, nil
	})

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tenant-xyz",
		Prefix:        "tenant-xyz",
		TTL:           15 * time.Minute,
	})
	require.NoError(t, err)

	// Output sanity.
	assert.Equal(t, "AK_STS", creds.AccessKey)
	assert.Equal(t, "TOK_STS", creds.SessionToken)
	assert.Equal(t, "arn:aws:iam::123:role/x", capturedRoleARN)
	assert.Equal(t, int32(900), capturedDuration, "15min in seconds")

	// Inspect the policy. It must contain a Statement with
	// Condition.StringLike.s3:prefix matching "tenant-xyz/*".
	require.NotEmpty(t, capturedPolicy)
	var p2 struct {
		Statement []struct {
			Effect    string              `json:"Effect"`
			Action    []string            `json:"Action"`
			Resource  []string            `json:"Resource"`
			Condition map[string]map[string][]string `json:"Condition"`
		} `json:"Statement"`
	}
	require.NoError(t, json.Unmarshal([]byte(capturedPolicy), &p2))

	var foundPrefixCond bool
	for _, st := range p2.Statement {
		if cond, ok := st.Condition["StringLike"]; ok {
			if pfx, ok := cond["s3:prefix"]; ok {
				assert.Contains(t, pfx, "tenant-xyz/*",
					"session policy MUST scope s3:ListBucket by s3:prefix = <token>/*")
				foundPrefixCond = true
			}
		}
	}
	assert.True(t, foundPrefixCond,
		"session policy MUST carry Condition.StringLike.s3:prefix — the cross-tenant boundary")
}

// TestS3_DefaultAssumeRoleNotWired — production callers that forget to inject
// an AssumeRole client get a loud error, not a silent shared-master fallback.
func TestS3_DefaultAssumeRoleNotWired(t *testing.T) {
	p, err := s3.New(storageprovider.Config{
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "k",
		MasterSecret: "s",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tenant",
		TTL:           15 * time.Minute,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, s3.ErrAssumeRoleNotWired)
}
