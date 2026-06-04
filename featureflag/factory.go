package featureflag

import (
	"fmt"
	"strings"
)

// Canonical backend identifiers. These are the strings every layer (api,
// worker, provisioner, k8s ConfigMaps) compares against.
const (
	// BackendStatic is the pure in-memory backend (default). Reads a flag
	// definition map / file; NO network; tests + CI need no running flagd.
	BackendStatic = "static"

	// BackendFlagd is the OpenFeature SDK + flagd backend (gRPC streaming).
	// Requires a reachable flagd; absence degrades to BackendStatic.
	BackendFlagd = "flagd"
)

// Config is the operator-facing configuration for the feature-flag backend.
// The api / worker / provisioner wire this from env vars
// (FEATURE_FLAG_BACKEND + per-backend knobs) and pass it to Factory() at boot.
type Config struct {
	// Backend selects the implementation. One of "static", "flagd". Aliases
	// ("memory"/"inmem"/"file" -> "static"; "openfeature"/"grpc" -> "flagd").
	// Empty defaults to "static" — the safest, dependency-free backend.
	Backend string

	// StaticFlags is the in-memory flag definition consumed by the static
	// backend. Keyed by flag key. When both StaticFlags and StaticFilePath are
	// empty the static backend serves an empty flag set (every flag => default,
	// i.e. fully fail-closed). Ignored by the flagd backend.
	StaticFlags map[string]StaticFlag

	// StaticFilePath, when set, loads the static flag definition from a
	// flagd-format JSON file at boot (the in-cluster ConfigMap mount path).
	// StaticFlags wins over StaticFilePath on key collision. Ignored by flagd.
	StaticFilePath string

	// Flagd-specific. Host + Port of the flagd gRPC endpoint. Defaults:
	// host "localhost", port 8013 (flagd rpc resolver default).
	FlagdHost string
	FlagdPort uint16

	// FlagdInProcess selects the in-process resolver (flagd syncs the flag set
	// over gRPC and evaluates locally — lowest latency) instead of the default
	// rpc resolver (each eval is a gRPC round-trip). Both are sub-second.
	FlagdInProcess bool
}

// NormalizeBackend maps the operator-facing value (with historical aliases)
// onto one of the canonical backend strings. An unrecognised non-empty value
// returns "" so Factory can decide how to degrade.
func NormalizeBackend(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "static", "memory", "in-memory", "inmem", "file":
		return BackendStatic
	case "flagd", "openfeature", "open-feature", "grpc":
		return BackendFlagd
	default:
		return ""
	}
}

// Factory selects and constructs the right [Provider] for cfg, ALREADY WRAPPED
// by [Wrap] so the returned provider is guaranteed fail-closed.
//
// Unlike storageprovider/queueprovider — where an unknown backend hard-fails so
// a service never silently degrades to a less-secure store — featureflag
// degrades DELIBERATELY: an unbuilt feature defaulting OFF is the SAFE state, so
// if the requested backend is unknown or its construction fails, Factory falls
// back to the static backend (fail-closed defaults) and returns a nil error.
// A flag system that refuses to boot would take the whole service down; that is
// strictly worse than serving defaults. The returned error is non-nil only as
// an ADVISORY (so the caller can log/alert that it degraded) — the provider is
// always usable.
func Factory(cfg Config) (Provider, error) {
	name := NormalizeBackend(cfg.Backend)
	if name == "" {
		// Unknown backend: degrade to static, surface advisory error.
		p, _ := newStatic(cfg)
		return Wrap(p), fmt.Errorf("%w: %q (degraded to static, fail-closed defaults)", ErrUnknownBackend, cfg.Backend)
	}

	ctor, ok := lookupBuilder(name)
	if !ok {
		// Backend recognised but its impl package wasn't imported (e.g. flagd
		// excluded from a slim build). Degrade to static.
		p, _ := newStatic(cfg)
		return Wrap(p), fmt.Errorf("featureflag: backend %q not registered — did you import the impl package? (degraded to static)", name)
	}

	p, err := ctor(cfg)
	if err != nil || p == nil {
		// Construction failed (flagd unreachable at boot, bad config, ...).
		// Degrade to static rather than failing the service.
		sp, _ := newStatic(cfg)
		return Wrap(sp), fmt.Errorf("featureflag: backend %q failed to construct (%v) — degraded to static", name, err)
	}
	return Wrap(p), nil
}

// Builder is the constructor signature each backend registers via Register from
// its package init(). Keeping flagd's OpenFeature + gRPC transitive deps in a
// subpackage means `common` consumers that only use the static backend don't
// pull the flagd SDK into their import graph (same pattern as queueprovider).
type Builder func(cfg Config) (Provider, error)

var builders = map[string]Builder{}

// Register adds a Builder under name. Called from each backend package's
// init(). Idempotent — a second registration with the same name overwrites the
// first (used in tests to inject a fake).
func Register(name string, b Builder) {
	builders[NormalizeBackend(name)] = b
}

func lookupBuilder(name string) (Builder, bool) {
	b, ok := builders[name]
	return b, ok
}

// ListRegistered returns the names of every backend currently registered. Used
// by the registry-iterating contract test (CLAUDE.md rule 18).
func ListRegistered() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, k)
	}
	return out
}

// newStatic is the in-package fallback constructor used by Factory's degrade
// paths. It does NOT depend on the static subpackage being imported, so the
// fail-closed fallback works even in a build that never imported any backend.
// It builds the same concrete provider the static subpackage registers.
func newStatic(cfg Config) (Provider, error) {
	return buildStatic(cfg)
}

// NewStaticBuilder returns the [Builder] for the in-memory static backend. The
// featureflag/static subpackage calls this from its init() to register the
// backend under [BackendStatic]. Exported (rather than registering from the
// root package directly) so a slim build that never imports the static
// subpackage keeps an empty registry, while Factory's degrade path still has a
// hard fallback via buildStatic.
func NewStaticBuilder() Builder {
	return buildStatic
}
