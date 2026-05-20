// Package rabbitmq is a SKELETON implementation of
// queueprovider.QueueCredentialProvider for RabbitMQ.
//
// This package exists as a portability proof: it satisfies the interface, so
// the contract test passes against it, and the day we want to migrate to
// RabbitMQ Streams the only code change is wiring `IssueTenantCredentials` to
// the real RabbitMQ HTTP API (vhost-per-tenant + user-per-tenant with
// permissions on that vhost only).
//
// Until then, IssueTenantCredentials returns queueprovider.ErrNotImplemented
// so any operator who flips QUEUE_BACKEND=rabbitmq accidentally gets a hard
// fail instead of silent unauthenticated traffic.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"

	"instant.dev/common/queueprovider"
)

func init() {
	queueprovider.Register("rabbitmq", builder)
}

func builder(cfg queueprovider.Config) (queueprovider.QueueCredentialProvider, error) {
	host := cfg.Host
	if host == "" {
		host = "rabbitmq.instant-data.svc.cluster.local"
	}
	return &Provider{host: host}, nil
}

// Provider is the RabbitMQ skeleton. Not used in production.
type Provider struct {
	host string
}

func (Provider) Name() string { return "rabbitmq" }

func (Provider) Capabilities() queueprovider.Capabilities {
	// Conservative: RabbitMQ supports vhost-per-tenant + per-user
	// permissions, so we'd light up PerTenantAccounts + BasicAuth when
	// wired. Subject-scoped pub/sub maps onto vhost permission regex —
	// possible but more limited than NATS. Leave SubjectScopedAuth=true
	// to advertise the eventual capability; the impl will need to honor
	// it.
	return queueprovider.Capabilities{
		PerTenantAccounts: true,
		SubjectScopedAuth: true,
		BasicAuth:         true,
		StreamIsolation:   true,
	}
}

func (Provider) IssueTenantCredentials(_ context.Context, _ queueprovider.IssueRequest) (*queueprovider.TenantCreds, error) {
	return nil, fmt.Errorf("%w: rabbitmq backend is a skeleton — wire IssueTenantCredentials before flipping QUEUE_BACKEND=rabbitmq", queueprovider.ErrNotImplemented)
}

func (Provider) RevokeTenantCredentials(_ context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	return errors.New("queueprovider.rabbitmq: revoke skeleton not implemented")
}
