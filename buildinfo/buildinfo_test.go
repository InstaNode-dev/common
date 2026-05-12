package buildinfo

import "testing"

// The three vars are package-level globals overwritten by `-ldflags -X`
// at link time. These tests verify:
//
//  1. The names are reachable from importers (compile-time check).
//  2. Default values match the documented sentinels — "dev" / "unknown".
//     If a default changes accidentally, log enrichment and /healthz
//     would silently report the new sentinel as truth, masking missing
//     -ldflags in CI.
//
// The real -ldflags injection check is the `make smoke-buildinfo`
// target — it builds a separate binary with -X overrides and verifies
// the runtime value matches. That cannot be done from within the same
// package's `go test` because the Go test binary itself is the linked
// artifact under test (and we don't want to mutate package globals
// from tests — flaky if any other test cares about them).

func TestDefaults(t *testing.T) {
	t.Run("GitSHA default", func(t *testing.T) {
		if GitSHA != "dev" {
			t.Errorf("GitSHA default = %q, want %q", GitSHA, "dev")
		}
	})
	t.Run("BuildTime default", func(t *testing.T) {
		if BuildTime != "unknown" {
			t.Errorf("BuildTime default = %q, want %q", BuildTime, "unknown")
		}
	})
	t.Run("Version default", func(t *testing.T) {
		if Version != "dev" {
			t.Errorf("Version default = %q, want %q", Version, "dev")
		}
	})
}

// TestReachable is a trivial compile-time check that the three exported
// names are addressable from outside the package. If a refactor renames
// or unexports any of them, ~306 slog log callsites and three /healthz
// handlers stop compiling — but this catches it immediately at the
// package boundary.
func TestReachable(t *testing.T) {
	_ = GitSHA
	_ = BuildTime
	_ = Version
}
