package dospaces_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"instant.dev/common/storageprovider"
	"instant.dev/common/storageprovider/dospaces"
)

// TestGetters_AndCustomerEndpointFallbacks exercises every simple accessor on
// the Provider plus the fallback paths inside customerEndpointURL() that the
// happy-path tests don't reach.
//
// Why each branch matters:
//
//   - MasterAccessKey / MasterSecretKey / Endpoint / Bucket / Region / UseTLS
//     are read by the api at boot when building the presigner for broker mode;
//     if any returns "" we ship an unsigned URL into a customer's logs.
//   - PublicURL() with publicURL=="" must fall through customerEndpointURL()
//     so we never return an empty string (broker-mode would silently 404).
//   - customerEndpointURL() with UseTLS=false should prepend "http://", and
//     when the endpoint already carries a scheme (some operators wire
//     `https://...`) it should pass through unmodified.
func TestGetters_AndCustomerEndpointFallbacks(t *testing.T) {
	t.Run("all_getters_return_configured_values", func(t *testing.T) {
		p, err := dospaces.New(storageprovider.Config{
			Endpoint:     "nyc3.digitaloceanspaces.com",
			PublicURL:    "https://s3.instanode.dev",
			Region:       "nyc3",
			Bucket:       "instant-shared",
			MasterKey:    "MK",
			MasterSecret: "MS",
			UseTLS:       true,
		})
		require.NoError(t, err)
		prov := p.(*dospaces.Provider)

		assert.Equal(t, "MK", prov.MasterAccessKey())
		assert.Equal(t, "MS", prov.MasterSecretKey())
		assert.Equal(t, "nyc3.digitaloceanspaces.com", prov.Endpoint())
		assert.Equal(t, "https://s3.instanode.dev", prov.PublicURL())
		assert.Equal(t, "instant-shared", prov.Bucket())
		assert.Equal(t, "nyc3", prov.Region())
		assert.True(t, prov.UseTLS())
	})

	t.Run("public_url_falls_back_to_scheme_prefixed_endpoint_no_tls", func(t *testing.T) {
		p, err := dospaces.New(storageprovider.Config{
			Endpoint:     "minio.local:9000",
			MasterKey:    "MK",
			MasterSecret: "MS",
			UseTLS:       false,
		})
		require.NoError(t, err)
		prov := p.(*dospaces.Provider)

		// PublicURL must fall back to customerEndpointURL ("http://minio.local:9000").
		assert.Equal(t, "http://minio.local:9000", prov.PublicURL())
		assert.False(t, prov.UseTLS())
	})

	t.Run("public_url_falls_back_to_scheme_prefixed_endpoint_tls", func(t *testing.T) {
		p, err := dospaces.New(storageprovider.Config{
			Endpoint:     "nyc3.digitaloceanspaces.com",
			MasterKey:    "MK",
			MasterSecret: "MS",
			UseTLS:       true,
		})
		require.NoError(t, err)
		prov := p.(*dospaces.Provider)
		assert.Equal(t, "https://nyc3.digitaloceanspaces.com", prov.PublicURL())
	})

	t.Run("endpoint_with_existing_scheme_passes_through", func(t *testing.T) {
		// Some operator wires OBJECT_STORE_ENDPOINT=https://foo.bar — the
		// customerEndpointURL must NOT double-prefix.
		p, err := dospaces.New(storageprovider.Config{
			Endpoint:     "https://already-schemed.example",
			MasterKey:    "MK",
			MasterSecret: "MS",
			UseTLS:       false,
		})
		require.NoError(t, err)
		prov := p.(*dospaces.Provider)
		assert.Equal(t, "https://already-schemed.example", prov.PublicURL())
	})
}
