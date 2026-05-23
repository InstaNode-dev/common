package r2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/r2"
)

// TestR2_Name verifies the canonical backend identifier.
func TestR2_Name(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)
	assert.Equal(t, "r2", p.Name())
}

// TestR2_New_RequiresMasterKey covers the third validation branch in New —
// missing master key/secret should error.
func TestR2_New_RequiresMasterKey(t *testing.T) {
	_, err := r2.New(storageprovider.Config{
		R2AccountID: "abc",
		R2APIToken:  "tok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OBJECT_STORE_ACCESS_KEY")
}

// TestR2_New_DefaultEndpointFromAccountID covers the endpoint==""
// branch — the provider must synthesize the endpoint from accountID.
func TestR2_New_DefaultEndpointFromAccountID(t *testing.T) {
	cfg := storageprovider.Config{
		R2AccountID:  "myacct",
		R2APIToken:   "tok",
		MasterKey:    "mk",
		MasterSecret: "ms",
	}
	p, err := r2.New(cfg)
	require.NoError(t, err)
	concrete, ok := p.(interface{ Endpoint() string })
	require.True(t, ok)
	assert.Equal(t, "myacct.r2.cloudflarestorage.com", concrete.Endpoint())
}

// TestR2_New_DefaultBucket covers the bucket=="" branch — defaults to
// "instant-shared".
func TestR2_New_DefaultBucket(t *testing.T) {
	cfg := storageprovider.Config{
		R2AccountID:  "myacct",
		R2APIToken:   "tok",
		MasterKey:    "mk",
		MasterSecret: "ms",
	}
	p, err := r2.New(cfg)
	require.NoError(t, err)
	concrete, ok := p.(interface{ Bucket() string })
	require.True(t, ok)
	assert.Equal(t, "instant-shared", concrete.Bucket())
}

// TestR2_DefaultAPIBase covers the apiBase() default branch (httpEndpoint=="").
// We can't easily inspect apiBase() directly, but issuing while httpEndpoint
// is unset will produce a network request the real Cloudflare host —
// short-circuit by giving the http client zero timeout so the call fails
// before the test depends on real DNS. Provider construction validates that
// the default path is reachable through the same code path.
func TestR2_DefaultAPIBase(t *testing.T) {
	// Ensure default base is used.
	r2.SetAPIBaseForTest("")
	p, err := r2.New(storageprovider.Config{
		R2AccountID:  "acct",
		R2APIToken:   "tok",
		MasterKey:    "mk",
		MasterSecret: "ms",
	})
	require.NoError(t, err)

	// Drive IssueTenantCredentials with a cancelled context so the
	// request build path (which calls apiBase()) is exercised but the
	// HTTP roundtrip immediately fails. This covers apiBase() default.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.IssueTenantCredentials(ctx, storageprovider.IssueRequest{
		ResourceToken: "x",
		Prefix:        "x",
	})
	require.Error(t, err)
}

// TestR2_IssueLongLivedKey_HTTPError covers the resp.StatusCode>=400 branch.
func TestR2_IssueLongLivedKey_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":10000,"message":"forbidden"}]}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "tok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// TestR2_IssueLongLivedKey_InvalidJSON covers the parse-error branch.
func TestR2_IssueLongLivedKey_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "tok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

// TestR2_IssueLongLivedKey_SuccessFalse covers the success=false branch.
func TestR2_IssueLongLivedKey_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 7000, "message": "bad"}]}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "tok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "success=false")
}

// TestR2_IssueTempCreds_HTTPError covers the temp-creds 4xx branch.
func TestR2_IssueTempCreds_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`gateway down`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "tok",
		TTL:           5 * time.Minute,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

// TestR2_IssueTempCreds_InvalidJSON covers parse failure on temp-creds path.
func TestR2_IssueTempCreds_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
		TTL:           1 * time.Minute,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

// TestR2_IssueTempCreds_SuccessFalse covers the temp-creds success=false branch.
func TestR2_IssueTempCreds_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 1, "message": "no"}]}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
		TTL:           30 * time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "success=false")
}

// TestR2_IssueTempCreds_UnparseableExpiry covers the time.Parse failure
// branch where Expiration is non-empty but malformed — expiresAt stays nil.
func TestR2_IssueTempCreds_UnparseableExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": {
				"accessKeyId": "AK",
				"secretAccessKey": "SK",
				"sessionToken": "ST",
				"expiration": "not-a-timestamp"
			}
		}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
		TTL:           30 * time.Second,
	})
	require.NoError(t, err)
	assert.Nil(t, creds.ExpiresAt, "unparseable expiration → ExpiresAt should remain nil")
}

// TestR2_IssueTempCreds_EmptyExpiration covers the empty-expiration branch.
func TestR2_IssueTempCreds_EmptyExpiration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": {
				"accessKeyId": "AK",
				"secretAccessKey": "SK",
				"sessionToken": "ST",
				"expiration": ""
			}
		}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
		TTL:           30 * time.Second,
	})
	require.NoError(t, err)
	assert.Nil(t, creds.ExpiresAt)
}

// TestR2_IssueTenantCredentials_EmptyPrefixUsesToken covers the prefix==""
// fallback to ResourceToken.
func TestR2_IssueTenantCredentials_EmptyPrefixUsesToken(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tenant-fallback",
		Prefix:        "", // empty — should fall back to ResourceToken
	})
	require.NoError(t, err)
	assert.Equal(t, "tenant-fallback", creds.Prefix)
}

// TestR2_IssueTenantCredentials_TrailingSlashStripped covers the TrimSuffix("/")
// path in the prefix normalization.
func TestR2_IssueTenantCredentials_TrailingSlashStripped(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "prefix-with-slash/",
	})
	require.NoError(t, err)
	assert.Equal(t, "prefix-with-slash", creds.Prefix)
}

// TestR2_IssueTenantCredentials_BucketOverride covers the in.Bucket!="" branch.
func TestR2_IssueTenantCredentials_BucketOverride(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m)

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "tok",
		Prefix:        "tok",
		Bucket:        "custom-bucket",
	})
	require.NoError(t, err)
	assert.Equal(t, "custom-bucket", creds.Bucket)
}

// TestR2_Revoke_HTTPError covers the >=400 branch in Revoke (non-404).
func TestR2_Revoke_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`boom`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	err = p.RevokeTenantCredentials(context.Background(), "key-id-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// TestR2_Revoke_NotFoundIsIdempotent covers the 404 idempotency branch.
func TestR2_Revoke_NotFoundIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	// 404 should be swallowed as already-gone.
	require.NoError(t, p.RevokeTenantCredentials(context.Background(), "key-id-xyz"))
}

// TestR2_Revoke_TransportError covers httpClient.Do error branch in Revoke.
func TestR2_Revoke_TransportError(t *testing.T) {
	r2.SetAPIBaseForTest("http://127.0.0.1:1")
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so Do() fails immediately
	err = p.RevokeTenantCredentials(ctx, "k1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do request")
}

// TestR2_IssueLongLivedKey_TransportError covers the httpClient.Do error
// branch in the long-lived path.
func TestR2_IssueLongLivedKey_TransportError(t *testing.T) {
	r2.SetAPIBaseForTest("http://127.0.0.1:1")
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.IssueTenantCredentials(ctx, storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
	})
	require.Error(t, err)
}

// TestR2_IssueTempCreds_TransportError covers the httpClient.Do error branch
// in the temp-creds path.
func TestR2_IssueTempCreds_TransportError(t *testing.T) {
	r2.SetAPIBaseForTest("http://127.0.0.1:1")
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "tok",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.IssueTenantCredentials(ctx, storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
		TTL:           1 * time.Minute,
	})
	require.Error(t, err)
}

// TestR2_AccessorMethods covers MasterAccessKey, MasterSecretKey, Endpoint,
// Bucket, PublicURL — all simple getters that were 0% covered.
func TestR2_AccessorMethods(t *testing.T) {
	p, err := r2.New(storageprovider.Config{
		R2AccountID:  "acct",
		R2APIToken:   "tok",
		MasterKey:    "MK",
		MasterSecret: "MS",
		Endpoint:     "endpoint.example.com",
		PublicURL:    "https://public.example.com",
		Bucket:       "my-bucket",
	})
	require.NoError(t, err)

	concrete := p.(interface {
		MasterAccessKey() string
		MasterSecretKey() string
		Endpoint() string
		Bucket() string
		PublicURL() string
	})

	assert.Equal(t, "MK", concrete.MasterAccessKey())
	assert.Equal(t, "MS", concrete.MasterSecretKey())
	assert.Equal(t, "endpoint.example.com", concrete.Endpoint())
	assert.Equal(t, "my-bucket", concrete.Bucket())
	assert.Equal(t, "https://public.example.com", concrete.PublicURL())
}

// TestR2_PublicURL_FallsBackToEndpoint covers the publicURL=="" branch of
// PublicURL() which delegates to customerEndpointURL().
func TestR2_PublicURL_FallsBackToEndpoint(t *testing.T) {
	p, err := r2.New(storageprovider.Config{
		R2AccountID:  "acct",
		R2APIToken:   "tok",
		MasterKey:    "MK",
		MasterSecret: "MS",
		Endpoint:     "endpoint.example.com",
		// PublicURL deliberately unset.
		Bucket: "b",
	})
	require.NoError(t, err)
	concrete := p.(interface{ PublicURL() string })
	got := concrete.PublicURL()
	assert.Equal(t, "https://endpoint.example.com", got, "no public URL → https://endpoint")
}

// TestR2_PublicURL_EndpointWithScheme covers the customerEndpointURL branch
// where the endpoint already carries a scheme (no prefix added).
func TestR2_PublicURL_EndpointWithScheme(t *testing.T) {
	p, err := r2.New(storageprovider.Config{
		R2AccountID:  "acct",
		R2APIToken:   "tok",
		MasterKey:    "MK",
		MasterSecret: "MS",
		Endpoint:     "http://insecure.example.com",
		Bucket:       "b",
	})
	require.NoError(t, err)
	concrete := p.(interface{ PublicURL() string })
	assert.Equal(t, "http://insecure.example.com", concrete.PublicURL())
}

// TestR2_CustomerEndpointURL_PublicURLSet covers the publicURL!="" branch
// inside customerEndpointURL — surfaced via IssueTenantCredentials when
// a credential's Endpoint comes from customerEndpointURL.
func TestR2_CustomerEndpointURL_PublicURLSet(t *testing.T) {
	m := newMockR2()
	defer m.close()
	p := buildProvider(t, m) // PublicURL = "https://r2.instanode.dev"

	creds, err := p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://r2.instanode.dev", creds.Endpoint,
		"customerEndpointURL must prefer publicURL when set")
}

// TestR2_Register confirms that the init() side-effect registered the
// backend factory under "r2". Calling storageprovider.Factory dispatches to it.
func TestR2_Register(t *testing.T) {
	p, err := storageprovider.Factory(storageprovider.Config{
		Backend:      "r2",
		R2AccountID:  "acct",
		R2APIToken:   "tok",
		MasterKey:    "MK",
		MasterSecret: "MS",
	})
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "r2", p.Name())
}

// TestR2_IssueLongLivedKey_RequestHeaders confirms the request carries the
// expected Authorization + Content-Type headers (small extra branch coverage
// confidence — the request build path is exercised regardless).
func TestR2_IssueLongLivedKey_RequestHeaders(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.True(t, strings.Contains(r.URL.Path, "/keys"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true, "result": {"accessKeyId":"A","secretAccessKey":"S","id":"K"}}`))
	}))
	defer srv.Close()
	r2.SetAPIBaseForTest(srv.URL)
	t.Cleanup(func() { r2.SetAPIBaseForTest("") })

	p, err := r2.New(storageprovider.Config{
		R2AccountID: "acct", R2APIToken: "test-token",
		MasterKey: "mk", MasterSecret: "ms",
	})
	require.NoError(t, err)

	_, err = p.IssueTenantCredentials(context.Background(), storageprovider.IssueRequest{
		ResourceToken: "t",
		Prefix:        "t",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}
