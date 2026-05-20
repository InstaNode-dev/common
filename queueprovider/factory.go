package queueprovider

import (
	"fmt"
	"strings"
)

// Config is the operator-facing configuration for the queue backend. The api +
// provisioner wire this from env vars (QUEUE_BACKEND + per-backend knobs) and
// pass it to Factory() at boot. Each provider documents which fields it
// requires.
type Config struct {
	// Backend selects the implementation. One of: "nats", "rabbitmq",
	// "kafka", "legacy_open". Aliases ("jetstream" → "nats", "rabbit" →
	// "rabbitmq", "redpanda" → "kafka") collapse to the canonical name.
	// Empty defaults to "nats".
	Backend string

	// NATS host or host:port (no scheme). Default: nats.instant-data.svc.cluster.local
	Host string

	// PublicHost is the hostname embedded in customer-facing URLs. Falls
	// back to Host when empty.
	PublicHost string

	// Port is the broker port. Default: 4222 (NATS), 5672 (RabbitMQ), 9092
	// (Kafka).
	Port int

	// UseTLS controls whether ConnectionURL uses tls:// (NATS) /
	// amqps:// (RabbitMQ).
	UseTLS bool

	// NATS-specific: operator seed (SO...) — signs new tenant account JWTs.
	// Loaded from `nats-operator` k8s Secret.
	NATSOperatorSeed string

	// NATS-specific: system-account JWT — referenced by `system_account` in
	// nats.conf. Loaded from `nats-operator` k8s Secret.
	NATSSystemAccountJWT string

	// NATS-specific: system-account public key (A...). Cached so we don't
	// re-decode the JWT every call.
	NATSSystemAccountPublicKey string

	// NATS-specific: system-account seed. Required for the
	// resolver-claim-push path (the provisioner pushes new account JWTs over
	// the SYS NATS connection).
	NATSSystemAccountSeed string

	// NATS-specific: system-user JWT + seed. The worker uses these to
	// enumerate every tenant's JetStream streams for quota accounting.
	NATSSystemUserJWT  string
	NATSSystemUserSeed string

	// Subject prefix template. Defaults to "tenant_<token>." where <token>
	// is the resource token. Backends that don't enforce subject scoping
	// (RabbitMQ skeleton, Kafka skeleton) ignore this.
	SubjectTemplate string
}

// NormalizeBackend maps the operator-facing value (with all the historical
// aliases) onto one of the canonical backend strings.
func NormalizeBackend(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "nats", "jetstream", "nats-jetstream":
		return "nats"
	case "rabbitmq", "rabbit", "amqp":
		return "rabbitmq"
	case "kafka", "redpanda":
		return "kafka"
	case "legacy_open", "legacy-open", "noauth", "none":
		return "legacy_open"
	default:
		return ""
	}
}

// Factory selects and constructs the right QueueCredentialProvider for cfg.
// Returns ErrUnknownBackend when cfg.Backend is unrecognised, so the caller
// can fail loudly instead of silently degrading to a less-secure backend.
//
// To keep `common` zero-dep on broker SDKs (so import-graph stays cheap for
// every consumer), the actual provider implementations live in subpackages
// that register themselves via init(). Factory consults the global registry
// populated by those inits.
func Factory(cfg Config) (QueueCredentialProvider, error) {
	name := NormalizeBackend(cfg.Backend)
	if name == "" {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, cfg.Backend)
	}
	ctor, ok := lookupBuilder(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q (no implementation registered — did you import the impl package?)", ErrUnknownBackend, name)
	}
	return ctor(cfg)
}

// Builder is the constructor signature every backend implementation
// registers with the global registry via Register. The api / worker / provi-
// sioner import the impl subpackages they want available — that way `common`
// stays free of broker-SDK transitive deps for tooling that doesn't need them.
type Builder func(cfg Config) (QueueCredentialProvider, error)

var builders = map[string]Builder{}

// Register adds a Builder under name. Called from each provider package's
// init(). Idempotent — a second registration with the same name silently
// overwrites the first (used in tests to inject a fake).
func Register(name string, b Builder) {
	builders[NormalizeBackend(name)] = b
}

func lookupBuilder(name string) (Builder, bool) {
	b, ok := builders[name]
	return b, ok
}

// ListRegistered returns the names of every backend currently registered.
// Used by the registry-iterating contract test.
func ListRegistered() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, k)
	}
	return out
}
