package featureflag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// StaticFlag is one flag definition for the in-memory static backend. It is a
// deliberately small subset of the flagd flag schema: a default variant plus an
// optional team-id allowlist whose members resolve to On instead of Default.
//
// The static backend is the DEFAULT and is used by every test + CI run (no
// network, no flagd container). Its semantics intentionally mirror flagd's so a
// flag behaves the same whether evaluated against the static map or a real
// flagd: an empty/unknown targeting context gets Default; an allowlisted
// TargetingKey or TeamID gets On.
type StaticFlag struct {
	// Off is the value returned when the eval context does not match any
	// targeting rule (the fail-closed variant for a gating flag — OFF).
	Off any `json:"off"`

	// On is the value returned when the eval context matches a targeting rule
	// (e.g. the team is in AllowTeamIDs).
	On any `json:"on"`

	// AllowTeamIDs is the integration-cohort allowlist. An EvalContext whose
	// TargetingKey OR TeamID is in this set resolves to On; everyone else gets
	// Off. Empty allowlist => every context gets Off (fully fail-closed). This
	// is the P1 targeting model from the ADR ("allowlist now, attribute next").
	AllowTeamIDs []string `json:"allowTeamIDs,omitempty"`
}

// matches reports whether ec is targeted by this flag's allowlist.
func (f StaticFlag) matches(ec EvalContext) bool {
	if len(f.AllowTeamIDs) == 0 {
		return false
	}
	for _, id := range f.AllowTeamIDs {
		if id == "" {
			continue
		}
		if id == ec.TargetingKey || id == ec.TeamID {
			return true
		}
	}
	return false
}

// resolve returns the variant value (On if targeted, else Off).
func (f StaticFlag) resolve(ec EvalContext) any {
	if f.matches(ec) {
		return f.On
	}
	return f.Off
}

// staticProvider is the pure in-memory backend. It holds no network handle, so
// Close is a no-op and every method is allocation-light and lock-free (the flag
// map is immutable after construction). This is the provider returned by
// Factory when FEATURE_FLAG_BACKEND is unset/"static" or when a richer backend
// fails to construct (the fail-closed degrade path).
type staticProvider struct {
	flags map[string]StaticFlag
}

// buildStatic constructs the static provider from cfg. StaticFlags (in-memory)
// is merged over StaticFilePath (file-loaded), so an explicit map always wins.
// A missing/unreadable file is NOT fatal — it yields an empty flag set, which
// is the most fail-closed state (every flag => caller default).
func buildStatic(cfg Config) (Provider, error) {
	flags := map[string]StaticFlag{}

	if cfg.StaticFilePath != "" {
		if loaded, err := loadStaticFile(cfg.StaticFilePath); err == nil {
			for k, v := range loaded {
				flags[k] = v
			}
		}
		// On error we intentionally keep going with whatever loaded (none):
		// a bad flag file must never block boot — features just read default.
	}
	for k, v := range cfg.StaticFlags {
		flags[k] = v // explicit map wins over file
	}
	return &staticProvider{flags: flags}, nil
}

// loadStaticFile reads a JSON object of {flagKey: StaticFlag} from path.
func loadStaticFile(path string) (map[string]StaticFlag, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path, not user input
	if err != nil {
		return nil, fmt.Errorf("featureflag: read static flag file: %w", err)
	}
	var out map[string]StaticFlag
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("featureflag: parse static flag file: %w", err)
	}
	return out, nil
}

func (*staticProvider) Name() string { return BackendStatic }
func (*staticProvider) Close() error { return nil }

func (s *staticProvider) BoolVariant(_ context.Context, key string, def bool, ec EvalContext) (bool, error) {
	f, ok := s.flags[key]
	if !ok {
		return def, ErrFlagNotFound
	}
	v, ok := s.resolveTyped(f, ec).(bool)
	if !ok {
		return def, ErrTypeMismatch
	}
	return v, nil
}

func (s *staticProvider) StringVariant(_ context.Context, key, def string, ec EvalContext) (string, error) {
	f, ok := s.flags[key]
	if !ok {
		return def, ErrFlagNotFound
	}
	v, ok := s.resolveTyped(f, ec).(string)
	if !ok {
		return def, ErrTypeMismatch
	}
	return v, nil
}

func (s *staticProvider) IntVariant(_ context.Context, key string, def int64, ec EvalContext) (int64, error) {
	f, ok := s.flags[key]
	if !ok {
		return def, ErrFlagNotFound
	}
	switch v := s.resolveTyped(f, ec).(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64: // JSON numbers decode to float64
		return int64(v), nil
	default:
		return def, ErrTypeMismatch
	}
}

// resolveTyped picks the On/Off variant for ec. Split out so all three typed
// accessors share one targeting code path.
func (s *staticProvider) resolveTyped(f StaticFlag, ec EvalContext) any {
	return f.resolve(ec)
}
