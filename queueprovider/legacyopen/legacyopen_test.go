package legacyopen

import (
	"context"
	"strings"
	"testing"

	"instant.dev/common/queueprovider"
)

func TestProvider_NameAndCapabilities(t *testing.T) {
	p, err := builder(queueprovider.Config{Backend: "legacy_open"})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if p.Name() != "legacy_open" {
		t.Errorf("Name = %q", p.Name())
	}
	caps := p.Capabilities()
	if caps.PerTenantAccounts || caps.SubjectScopedAuth || caps.StreamIsolation {
		t.Errorf("legacy_open should report no capabilities, got %+v", caps)
	}
}

func TestBuilder_HostAndPortDefaults(t *testing.T) {
	p, err := builder(queueprovider.Config{})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	pr, ok := p.(*Provider)
	if !ok {
		t.Fatalf("unexpected type %T", p)
	}
	if pr.port != 4222 {
		t.Errorf("port default = %d", pr.port)
	}
	if pr.publicHost == "" {
		t.Error("publicHost should default")
	}

	// Explicit port + publicHost honored.
	p2, _ := builder(queueprovider.Config{Host: "h.local", PublicHost: "p.public", Port: 5555})
	pr2 := p2.(*Provider)
	if pr2.port != 5555 || pr2.publicHost != "p.public" {
		t.Errorf("unexpected provider: %+v", pr2)
	}
}

func TestIssueTenantCredentials(t *testing.T) {
	p, _ := builder(queueprovider.Config{Host: "h", PublicHost: "p", Port: 4222})
	creds, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{
		ResourceToken: "tok",
		Subject:       "tenant_tok.",
	})
	if err != nil {
		t.Fatalf("IssueTenantCredentials: %v", err)
	}
	if creds.AuthMode != queueprovider.AuthModeLegacyOpen {
		t.Errorf("AuthMode = %q", creds.AuthMode)
	}
	if !strings.HasPrefix(creds.ConnectionURL, "nats://p:") {
		t.Errorf("ConnectionURL = %q", creds.ConnectionURL)
	}
	if creds.Subject != "tenant_tok." {
		t.Errorf("Subject = %q", creds.Subject)
	}
	if creds.JWT != "" || creds.NKey != "" {
		t.Error("legacy_open must carry no JWT/NKey")
	}
}

func TestIssueTenantCredentials_MissingToken(t *testing.T) {
	p, _ := builder(queueprovider.Config{})
	_, err := p.IssueTenantCredentials(context.Background(), queueprovider.IssueRequest{})
	if err == nil {
		t.Fatal("expected error for empty ResourceToken")
	}
}

func TestRevokeTenantCredentials_Noop(t *testing.T) {
	p, _ := builder(queueprovider.Config{})
	if err := p.RevokeTenantCredentials(context.Background(), ""); err != nil {
		t.Errorf("Revoke(\"\") should be no-op, got %v", err)
	}
	if err := p.RevokeTenantCredentials(context.Background(), "any"); err != nil {
		t.Errorf("Revoke should be no-op, got %v", err)
	}
}
