// Package legacyopen implements queueprovider.QueueCredentialProvider as a
// pass-through that returns NO credentials. Used during the staged cutover to
// operator-mode NATS:
//
//   - PRE-cutover: the api can boot with QUEUE_BACKEND=legacy_open and serve
//     /queue/new returning the existing unauthenticated nats:// URL while the
//     operator secrets get generated, the nats.yaml gets flipped, etc.
//   - DURING-cutover: existing rows have auth_mode='legacy_open' and the api
//     looks them up + returns them via the legacy code path; new rows are
//     written with auth_mode='isolated' via the `nats` provider.
//   - POST-cutover: this package is no longer referenced. Delete it.
//
// Capabilities() returns all-false, so a /storage-style capability-aware
// fallback in the handler can degrade safely if anyone accidentally flips
// QUEUE_BACKEND=legacy_open in production after the cutover.
package legacyopen

import (
	"context"
	"fmt"

	"instant.dev/common/queueprovider"
)

func init() {
	queueprovider.Register("legacy_open", builder)
}

func builder(cfg queueprovider.Config) (queueprovider.QueueCredentialProvider, error) {
	host := cfg.Host
	if host == "" {
		host = "nats.instant-data.svc.cluster.local"
	}
	publicHost := cfg.PublicHost
	if publicHost == "" {
		publicHost = host
	}
	port := cfg.Port
	if port == 0 {
		port = 4222
	}
	return &Provider{publicHost: publicHost, port: port}, nil
}

// Provider returns no credentials — same un-authed NATS the pre-cutover
// platform already exposed.
type Provider struct {
	publicHost string
	port       int
}

func (Provider) Name() string { return "legacy_open" }

// Capabilities reports all-false because legacy_open enforces NOTHING. A
// caller consulting Capabilities() can detect this and refuse to return a
// resource for a new tenant (or surface a "your queue is unauthenticated,
// please re-provision" warning).
func (Provider) Capabilities() queueprovider.Capabilities {
	return queueprovider.Capabilities{}
}

// IssueTenantCredentials returns a TenantCreds with auth_mode=legacy_open and
// no credentials. Subject is echoed so the handler can still build the
// response. The api MUST persist auth_mode=legacy_open on the resource row
// when it sees this.
func (p Provider) IssueTenantCredentials(_ context.Context, in queueprovider.IssueRequest) (*queueprovider.TenantCreds, error) {
	if in.ResourceToken == "" {
		return nil, fmt.Errorf("queueprovider.legacyopen: ResourceToken required")
	}
	return &queueprovider.TenantCreds{
		ConnectionURL: fmt.Sprintf("nats://%s:%d", p.publicHost, p.port),
		Subject:       in.Subject,
		AuthMode:      queueprovider.AuthModeLegacyOpen,
	}, nil
}

func (Provider) RevokeTenantCredentials(_ context.Context, _ string) error { return nil }
