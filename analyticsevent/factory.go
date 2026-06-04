package analyticsevent

import (
	"fmt"
	"strings"
)

// NormalizeBackend maps the operator-facing ANALYTICS_BACKEND value (with
// historical aliases) onto one of the canonical backend strings. An empty value
// defaults to [BackendNoop] (analytics off, never errors). An unrecognised
// non-empty value returns "" so Factory can surface an advisory error while
// still degrading to noop.
func NormalizeBackend(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "noop", "off", "disabled", "none":
		return BackendNoop
	case "newrelic", "new-relic", "nr", "insights":
		return BackendNewRelic
	default:
		return ""
	}
}

// Factory selects and constructs the right [Emitter] for cfg, ALREADY WRAPPED by
// [Wrap] so the returned emitter is fail-open and PII-sanitizing.
//
// Unlike storageprovider/queueprovider — where an unknown backend hard-fails so
// a service never silently ships to a less-secure store — analyticsevent
// degrades DELIBERATELY: analytics is best-effort, so Factory ALWAYS returns a
// usable emitter and never an unrecoverable error. A returned non-nil error is
// purely ADVISORY (so the caller can log/alert that analytics degraded). The
// degrade ladder:
//
//   - Config.Override set        -> wrap and return it (how services inject the
//     NR sink without the root package importing the NR Go agent).
//   - Backend == newrelic, but its builder wasn't registered (the nr subpackage
//     wasn't imported / no Override given) -> noop + advisory error.
//   - Backend unknown            -> noop + advisory ErrUnknownBackend.
//   - Backend noop / construct ok -> the selected emitter, wrapped.
func Factory(cfg Config) (Emitter, error) {
	if cfg.Override != nil {
		return Wrap(cfg.Override), nil
	}

	name := NormalizeBackend(cfg.Backend)
	if name == "" {
		return Wrap(NewNoop()), fmt.Errorf("%w: %q (degraded to noop, analytics off)", ErrUnknownBackend, cfg.Backend)
	}

	if name == BackendNoop {
		return Wrap(NewNoop()), nil
	}

	// name == BackendNewRelic. The NR sink requires a *newrelic.Application,
	// which only a service holds — it is wired via Config.Override (constructed
	// from analyticsevent/nr). A bare ANALYTICS_BACKEND=newrelic with no Override
	// and no registered builder degrades to noop rather than failing the service.
	ctor, ok := lookupBuilder(name)
	if !ok {
		return Wrap(NewNoop()), fmt.Errorf(
			"analyticsevent: backend %q selected but no app provided (set Config.Override from analyticsevent/nr) — degraded to noop", name)
	}
	e, err := ctor(cfg)
	if err != nil || e == nil {
		return Wrap(NewNoop()), fmt.Errorf("analyticsevent: backend %q failed to construct (%v) — degraded to noop", name, err)
	}
	return Wrap(e), nil
}

// Builder is the constructor signature a backend MAY register via [Register]
// from its package init(). Today the NR sink is wired via Config.Override rather
// than a builder (it needs a live *newrelic.Application), so the registry is an
// extension seam: a future credential-free analytics backend could register
// here and be selected purely by ANALYTICS_BACKEND.
type Builder func(cfg Config) (Emitter, error)

var builders = map[string]Builder{}

// Register adds a Builder under name. Idempotent — a second registration with
// the same name overwrites the first (used in tests to inject a fake).
func Register(name string, b Builder) {
	builders[NormalizeBackend(name)] = b
}

func lookupBuilder(name string) (Builder, bool) {
	b, ok := builders[name]
	return b, ok
}

// ListRegistered returns the names of every backend currently registered via
// [Register]. Used by the registry-iterating contract test (CLAUDE.md rule 18).
func ListRegistered() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, k)
	}
	return out
}
