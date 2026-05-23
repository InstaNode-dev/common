package s3_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/s3"
)

// TestS3_New_RequiresMasterCreds — sts:AssumeRole requires platform creds.
func TestS3_New_RequiresMasterCreds(t *testing.T) {
	_, err := s3.New(storageprovider.Config{
		AWSRoleARN: "arn:aws:iam::123:role/x",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OBJECT_STORE_ACCESS_KEY")
}

// TestS3_Getters_AndCustomerEndpointFallbacks covers Master*/Endpoint/Bucket/
// Region/PublicURL/Name + the two customerEndpointURL branches (already-schemed
// and bare-host).
func TestS3_Getters_AndCustomerEndpointFallbacks(t *testing.T) {
	t.Run("bare_host_gets_https_prefix", func(t *testing.T) {
		raw, err := s3.New(storageprovider.Config{
			AWSRoleARN:   "arn:aws:iam::123:role/x",
			MasterKey:    "MK",
			MasterSecret: "MS",
		})
		require.NoError(t, err)
		p := raw.(*s3.Provider)

		assert.Equal(t, "MK", p.MasterAccessKey())
		assert.Equal(t, "MS", p.MasterSecretKey())
		assert.Equal(t, "s3.us-east-1.amazonaws.com", p.Endpoint())
		assert.Equal(t, "instant-shared", p.Bucket())
		assert.Equal(t, "us-east-1", p.Region())
		assert.Equal(t, "s3", p.Name())
		assert.Equal(t, "https://s3.us-east-1.amazonaws.com", p.PublicURL())
	})

	t.Run("endpoint_already_schemed_passes_through", func(t *testing.T) {
		raw, err := s3.New(storageprovider.Config{
			Endpoint:     "https://custom.example",
			AWSRoleARN:   "arn:aws:iam::123:role/x",
			MasterKey:    "MK",
			MasterSecret: "MS",
		})
		require.NoError(t, err)
		p := raw.(*s3.Provider)
		assert.Equal(t, "https://custom.example", p.PublicURL())
	})

	t.Run("public_url_set_overrides_endpoint", func(t *testing.T) {
		raw, err := s3.New(storageprovider.Config{
			Endpoint:     "s3.amazonaws.com",
			PublicURL:    "https://cdn.example.dev",
			AWSRoleARN:   "arn:aws:iam::123:role/x",
			MasterKey:    "MK",
			MasterSecret: "MS",
		})
		require.NoError(t, err)
		p := raw.(*s3.Provider)
		assert.Equal(t, "https://cdn.example.dev", p.PublicURL())
	})

	t.Run("region_defaults_and_bucket_defaults_apply", func(t *testing.T) {
		raw, err := s3.New(storageprovider.Config{
			AWSRoleARN:   "arn:aws:iam::123:role/x",
			MasterKey:    "MK",
			MasterSecret: "MS",
		})
		require.NoError(t, err)
		p := raw.(*s3.Provider)
		assert.Equal(t, "us-east-1", p.Region())
		assert.Equal(t, "instant-shared", p.Bucket())
	})
}

// TestS3_RevokeTenantCredentials_IsNoOp — STS sessions can't be revoked, the
// method must return nil regardless of input.
func TestS3_RevokeTenantCredentials_IsNoOp(t *testing.T) {
	raw, err := s3.New(storageprovider.Config{
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "MK",
		MasterSecret: "MS",
	})
	require.NoError(t, err)
	p := raw.(*s3.Provider)

	require.NoError(t, p.RevokeTenantCredentials(context.Background(), ""))
	require.NoError(t, p.RevokeTenantCredentials(context.Background(), "any-key"))
}

// TestS3_IssueTenantCredentials_DefaultsTTLAndPrefix covers:
//   - TTL==0 falls through to the 1-hour default
//   - Empty Prefix falls back to ResourceToken
//   - Empty Bucket falls back to provider default
func TestS3_IssueTenantCredentials_DefaultsTTLAndPrefix(t *testing.T) {
	raw, err := s3.New(storageprovider.Config{
		Region:       "us-west-2",
		Bucket:       "my-default",
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "MK",
		MasterSecret: "MS",
	})
	require.NoError(t, err)
	p := raw.(*s3.Provider)

	var captured s3.AssumeRoleInput
	p.SetAssumeRoleFunc(func(ctx context.Context, in s3.AssumeRoleInput) (*s3.AssumeRoleOutput, error) {
		captured = in
		return &s3.AssumeRoleOutput{
			AccessKeyID:     "AK",
			SecretAccessKey: "SK",
			SessionToken:    "TOK",
			Expiration:      time.Now().Add(time.Hour),
		}, nil
	})

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok-ABC",
		// No Prefix, no Bucket, no TTL — exercises every fallback line.
	})
	require.NoError(t, err)
	assert.Equal(t, "tok-ABC", creds.Prefix, "prefix must default to resource token")
	assert.Equal(t, "my-default", creds.Bucket, "bucket must default to provider default")
	assert.Equal(t, int32(3600), captured.DurationSeconds, "TTL==0 must default to 1h")
}

// TestS3_SafeSessionName covers the trimming + min-length fallback. RoleSessionName
// is constrained by AWS to [\w+=,.@-] and length 2..64; safeSessionName must drop
// disallowed characters AND backfill "instanode-x" when the result is too short
// to be a valid session name.
func TestS3_SafeSessionName(t *testing.T) {
	raw, err := s3.New(storageprovider.Config{
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "MK",
		MasterSecret: "MS",
	})
	require.NoError(t, err)
	p := raw.(*s3.Provider)

	cases := []struct {
		name      string
		token     string
		expected  string
	}{
		{
			name:     "all_disallowed_chars_falls_back_to_instanode_x",
			token:    "!!!@@@###",
			expected: "instanode-instanode-x",
		},
		{
			name:     "long_token_truncated_to_56",
			token:    strings.Repeat("a", 200),
			expected: "instanode-" + strings.Repeat("a", 56),
		},
		{
			name:     "digits_underscores_dashes_kept",
			token:    "tenant-01_abc",
			expected: "instanode-tenant-01_abc",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var captured s3.AssumeRoleInput
			p.SetAssumeRoleFunc(func(ctx context.Context, in s3.AssumeRoleInput) (*s3.AssumeRoleOutput, error) {
				captured = in
				return &s3.AssumeRoleOutput{
					AccessKeyID:     "AK",
					SecretAccessKey: "SK",
					SessionToken:    "TOK",
					Expiration:      time.Now().Add(15 * time.Minute),
				}, nil
			})
			_, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
				ResourceToken: c.token,
				TTL:           15 * time.Minute,
			})
			require.NoError(t, err)
			assert.Equal(t, c.expected, captured.RoleSessionName)
		})
	}
}

// TestS3_AssumeRoleError_Propagates verifies the error from a failing
// AssumeRole call surfaces to the caller rather than being swallowed into
// a "best-effort" silent return.
func TestS3_AssumeRoleError_Propagates(t *testing.T) {
	raw, err := s3.New(storageprovider.Config{
		AWSRoleARN:   "arn:aws:iam::123:role/x",
		MasterKey:    "MK",
		MasterSecret: "MS",
	})
	require.NoError(t, err)
	p := raw.(*s3.Provider)
	p.SetAssumeRoleFunc(func(ctx context.Context, in s3.AssumeRoleInput) (*s3.AssumeRoleOutput, error) {
		return nil, assertError("aws-sdk: throttled")
	})
	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "x",
		TTL:           15 * time.Minute,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "throttled")
}

type assertError string

func (a assertError) Error() string { return string(a) }
