package kafka

import (
	"context"
	"errors"
	"testing"

	"instant.dev/common/queueprovider"
)

func TestProvider_NameAndCapabilities(t *testing.T) {
	p, err := builder(queueprovider.Config{Backend: "kafka"})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if p.Name() != "kafka" {
		t.Errorf("Name = %q", p.Name())
	}
	caps := p.Capabilities()
	if caps.PerTenantAccounts {
		t.Error("kafka cap: PerTenantAccounts must be false")
	}
	if !caps.SubjectScopedAuth || !caps.BasicAuth || !caps.StreamIsolation {
		t.Errorf("unexpected caps: %+v", caps)
	}
}

func TestProvider_DefaultHost(t *testing.T) {
	p, err := builder(queueprovider.Config{})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if pr, ok := p.(*Provider); !ok || pr.host == "" {
		t.Errorf("expected default host populated, got %+v", p)
	}
}

func TestIssueTenantCredentials_NotImplemented(t *testing.T) {
	p, _ := builder(queueprovider.Config{Backend: "kafka", Host: "h"})
	_, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{ResourceToken: "t"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, queueprovider.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRevokeTenantCredentials(t *testing.T) {
	p, _ := builder(queueprovider.Config{Backend: "kafka", Host: "h"})
	if err := p.RevokeTenantCredentials(context.Background(), ""); err != nil {
		t.Errorf("empty keyID should be no-op, got %v", err)
	}
	if err := p.RevokeTenantCredentials(context.Background(), "principal-1"); err == nil {
		t.Error("expected skeleton error for non-empty keyID")
	}
}
