// Package kafka is a SKELETON implementation of
// queueprovider.QueueCredentialProvider for Apache Kafka (or Redpanda).
//
// This package exists as a portability proof: it satisfies the interface, so
// the contract test passes against it, and the day we want to migrate to
// Kafka the only code change is wiring `IssueTenantCredentials` to the real
// Kafka admin client (SASL/SCRAM user + topic-prefix ACL).
//
// Until then, IssueTenantCredentials returns queueprovider.ErrNotImplemented
// so any operator who flips QUEUE_BACKEND=kafka accidentally gets a hard fail
// instead of silent unauthenticated traffic.
package kafka

import (
	"context"
	"errors"
	"fmt"

	"instant.dev/common/queueprovider"
)

func init() {
	queueprovider.Register("kafka", builder)
}

func builder(cfg queueprovider.Config) (queueprovider.QueueCredentialProvider, error) {
	host := cfg.Host
	if host == "" {
		host = "kafka.instant-data.svc.cluster.local"
	}
	return &Provider{host: host}, nil
}

// Provider is the Kafka skeleton. Not used in production.
type Provider struct {
	host string
}

func (Provider) Name() string { return "kafka" }

func (Provider) Capabilities() queueprovider.Capabilities {
	return queueprovider.Capabilities{
		PerTenantAccounts: false, // Kafka has no nested account model — one cluster, ACL'd principals
		SubjectScopedAuth: true,  // topic-prefix ACLs are the analog of NATS subject scoping
		BasicAuth:         true,  // SASL/PLAIN or SASL/SCRAM
		StreamIsolation:   true,  // topic-prefix ACL enforces stream isolation
	}
}

func (Provider) IssueTenantCredentials(_ context.Context, _ queueprovider.IssueRequest) (*queueprovider.TenantCreds, error) {
	return nil, fmt.Errorf("%w: kafka backend is a skeleton — wire IssueTenantCredentials before flipping QUEUE_BACKEND=kafka", queueprovider.ErrNotImplemented)
}

func (Provider) RevokeTenantCredentials(_ context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	return errors.New("queueprovider.kafka: revoke skeleton not implemented")
}
