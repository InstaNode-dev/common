package r2_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/r2"
)

// mockR2API is a stub Cloudflare R2 API. It captures every request body so
// tests can assert the provider sent the expected JSON shape — in particular,
// that prefix-scoping made it into the `parameters.prefixes` field.
type mockR2API struct {
	mu        sync.Mutex
	server    *httptest.Server
	requests  []capturedReq
	keyResp   string // canned JSON for POST /keys
	tempResp  string // canned JSON for POST /temp-access-credentials
	delStatus int    // status code for DELETE /keys/:id (default 200)
}

type capturedReq struct {
	Method string
	Path   string
	Body   string
}

func newMockR2() *mockR2API {
	m := &mockR2API{delStatus: http.StatusOK}
	m.keyResp = `{
		"success": true,
		"result": {
			"accessKeyId": "AK_R2_TENANT",
			"secretAccessKey": "SK_R2_TENANT",
			"id": "key-id-abc"
		}
	}`
	m.tempResp = `{
		"success": true,
		"result": {
			"accessKeyId": "AK_R2_TEMP",
			"secretAccessKey": "SK_R2_TEMP",
			"sessionToken": "SESSION_TOKEN",
			"expiration": "2030-01-01T00:00:00Z"
		}
	}`
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.requests = append(m.requests, capturedReq{
			Method: r.Method, Path: r.URL.Path, Body: string(body),
		})
		m.mu.Unlock()

		switch {
		case strings.Contains(r.URL.Path, "/temp-access-credentials"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(m.tempResp))
		case strings.HasSuffix(r.URL.Path, "/keys") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(m.keyResp))
		case strings.Contains(r.URL.Path, "/keys/") && r.Method == http.MethodDelete:
			w.WriteHeader(m.delStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return m
}

func (m *mockR2API) close() { m.server.Close() }

func (m *mockR2API) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return ""
	}
	return m.requests[len(m.requests)-1].Body
}

// buildProvider wires the R2 provider to point at the mock server.
func buildProvider(t *testing.T, m *mockR2API) storageprovider.StorageCredentialProvider {
	t.Helper()
	r2.SetAPIBaseForTest(m.server.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })
	p, err := r2.New(storageprovider.Config{
		Backend:      "r2",
		Endpoint:     "test.r2.cloudflarestorage.com",
		PublicURL:    "https://r2.instanode.dev",
		Bucket:       "instant-shared",
		MasterKey:    "MASTER_R2",
		MasterSecret: "MASTER_R2_SECRET",
		R2AccountID:  "deadbeef",
		R2APIToken:   "test-token",
	})
	require.NoError(t, err)
	return p
}

// TestR2Capabilities — R2 is the prefix-scoped reference. PrefixScopedKeys
// MUST be true; otherwise the api routes R2 tenants into broker mode by
// mistake and we've lost the migration.
func TestR2Capabilities(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	caps := p.Capabilities()
	assert.True(t, caps.PrefixScopedKeys, "R2 ENFORCES s3:prefix — the whole point of migrating")
	assert.True(t, caps.BucketScopedKeys)
	assert.True(t, caps.STS)
	assert.True(t, caps.BucketPerTenant)
}

// TestR2_IssueLongLivedKey_PostsPrefixScopedRequest verifies that asking for
// TTL=0 hits the buckets/keys endpoint AND that the request body carries the
// prefix the api requested. This is the test that proves the policy is
// prefix-scoped: a future regression that drops the prefixes field would
// make every R2 tenant a global-bucket-key holder.
func TestR2_IssueLongLivedKey_PostsPrefixScopedRequest(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tenant-abc",
		Prefix:        "tenant-abc",
		TTL:           0,
	})
	require.NoError(t, err)

	assert.Equal(t, "AK_R2_TENANT", creds.AccessKey)
	assert.Equal(t, "SK_R2_TENANT", creds.SecretKey)
	assert.Empty(t, creds.SessionToken, "long-lived key has no session token")
	assert.Nil(t, creds.ExpiresAt, "long-lived key has no expiry")
	assert.Equal(t, "key-id-abc", creds.KeyID, "KeyID must be returned for revoke")
	assert.Equal(t, "auto", creds.Region)
	assert.Equal(t, "tenant-abc", creds.Prefix)

	// The request body should carry parameters.prefixes = ["tenant-abc/"].
	// Parse it directly so a body-format change shows up here.
	body := m.lastBody()
	require.NotEmpty(t, body)
	var sent struct {
		Permissions []string `json:"permissions"`
		Parameters  struct {
			Prefixes []string `json:"prefixes"`
		} `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Contains(t, sent.Permissions, "object-read-write")
	assert.Equal(t, []string{"tenant-abc/"}, sent.Parameters.Prefixes,
		"R2 request MUST scope by prefix — this is the migration's whole purpose")
}

// TestR2_IssueTempCreds_ScopesPrefixAndTTL verifies the TTL>0 branch hits
// the temp-access-credentials endpoint and returns a session token.
func TestR2_IssueTempCreds_ScopesPrefixAndTTL(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tenant-temp",
		Prefix:        "tenant-temp",
		TTL:           15 * time.Minute,
	})
	require.NoError(t, err)

	assert.Equal(t, "AK_R2_TEMP", creds.AccessKey)
	assert.Equal(t, "SESSION_TOKEN", creds.SessionToken)
	require.NotNil(t, creds.ExpiresAt, "temp creds must carry an expiry")

	body := m.lastBody()
	var sent struct {
		Bucket     string   `json:"bucket"`
		Prefixes   []string `json:"prefixes"`
		Permission string   `json:"permission"`
		TTLSeconds int      `json:"ttlSeconds"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Equal(t, "instant-shared", sent.Bucket)
	assert.Equal(t, []string{"tenant-temp/"}, sent.Prefixes)
	assert.Equal(t, 900, sent.TTLSeconds, "15min TTL → 900s on the wire")
}

// TestR2_Revoke_DeletesByKeyID hits DELETE /keys/:id with the KeyID.
func TestR2_Revoke_DeletesByKeyID(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	require.NoError(t, p.RevokeTenantCredentials(context.Background(), "key-id-abc"))

	// Last request should be DELETE … /keys/key-id-abc.
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.requests)
	last := m.requests[len(m.requests)-1]
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Contains(t, last.Path, "/keys/key-id-abc")
}

// TestR2_Revoke_EmptyKeyIDIsNoOp verifies the broker-mode teardown path
// (no KeyID to revoke) doesn't make a network call.
func TestR2_Revoke_EmptyKeyIDIsNoOp(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	require.NoError(t, p.RevokeTenantCredentials(context.Background(), ""))
	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Empty(t, m.requests, "empty KeyID must not hit the network")
}

// TestR2_New_RequiresAccountAndToken — both R2-specific env vars are required.
func TestR2_New_RequiresAccountAndToken(t *testing.T) {
	_, err := r2.New(storageprovider.Config{MasterKey: "k", MasterSecret: "s"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R2_ACCOUNT_ID")

	_, err = r2.New(storageprovider.Config{
		R2AccountID:  "abc",
		MasterKey:    "k",
		MasterSecret: "s",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R2_API_TOKEN")
}
