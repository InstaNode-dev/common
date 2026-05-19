// Package resourcestatus is the single source of truth for the lifecycle
// status of a provisioned resource and for the time-to-expiry "stage"
// derivation used by the expiry-warning jobs.
//
// # WHY THIS PACKAGE EXISTS
//
// Before this package, `api` and `worker` each carried their own
// hand-written predicates for "is this resource active / suspended /
// expired" and for "which expiry-warning stage is this resource in".
// The two copies drifted: a fix in one repo did not reach the other, and
// the BugBash flagged the "expiry-stage predicate divergence" as a class
// of latent bugs (e.g. a status check that included `paused` in one repo
// and excluded it in the other).
//
// Every status comparison and every expires_at-vs-now derivation now
// routes through the functions here. `api` and `worker` consume this
// package via a `replace instant.dev/common => ../common` directive, so
// a change here is a true cross-repo contract change (CLAUDE.md rule 22).
//
// The exhaustive test in resourcestatus_test.go iterates AllStatuses() and
// every expiry-stage boundary; adding a Status without handling it in the
// derivation switches fails the build.
package resourcestatus

import "time"

// Status is the canonical lifecycle status of a row in the `resources`
// table. The string values are the EXACT values persisted in the
// `resources.status` column — do not change them without a migration.
type Status string

const (
	// StatusActive — the resource is provisioned and serving traffic.
	// Connection URLs work. This is the only status for which the public
	// service paths (webhook receive, log streaming, family-twin roots)
	// treat the resource as usable.
	StatusActive Status = "active"

	// StatusPaused — the resource is intentionally paused by the owner
	// (Pro+ pause/resume feature). On-disk data is preserved; new
	// connections are refused until resume. Distinct from suspended:
	// paused is owner-initiated and reversible by the owner.
	StatusPaused Status = "paused"

	// StatusSuspended — the resource was suspended by the platform
	// (typically a quota-wall breach). Data is preserved; the customer
	// must resolve the quota condition (upgrade / free space) to resume.
	StatusSuspended Status = "suspended"

	// StatusExpired — a deployment-style terminal status set when an
	// auto-expiry sweep flips a row whose TTL elapsed but whose physical
	// teardown is deferred. Resources proper move straight to deleted;
	// this value exists so callers that share the enum (deployments) and
	// any future deferred-teardown path have a canonical name.
	StatusExpired Status = "expired"

	// StatusDeleted — terminal. The row is soft-deleted; the physical
	// backing infra has been (or is being) torn down. Never transitions
	// out of this state.
	StatusDeleted Status = "deleted"
)

// AllStatuses returns every canonical Status value, ordered from most
// "live" to terminal. The exhaustive test iterates this slice; a Status
// constant added above without being appended here fails that test.
func AllStatuses() []Status {
	return []Status{
		StatusActive,
		StatusPaused,
		StatusSuspended,
		StatusExpired,
		StatusDeleted,
	}
}

// Valid reports whether s is one of the canonical Status values.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusPaused, StatusSuspended, StatusExpired, StatusDeleted:
		return true
	default:
		return false
	}
}

// String returns the persisted string value of the status.
func (s Status) String() string { return string(s) }

// Parse converts a raw status string (e.g. read from the DB) into a
// Status. The second return is false for an unrecognised value; callers
// that want a best-effort value can ignore it (the returned Status is
// still the raw string typed as Status, but Valid() will report false).
func Parse(raw string) (Status, bool) {
	s := Status(raw)
	return s, s.Valid()
}

// IsActive reports whether the resource is live and serving. This is the
// predicate the public service paths gate on (webhook receive/list, log
// streaming, family-twin root selection): only an active resource has
// live backing infra.
func (s Status) IsActive() bool { return s == StatusActive }

// IsPaused reports whether the resource is owner-paused.
func (s Status) IsPaused() bool { return s == StatusPaused }

// IsSuspended reports whether the resource is platform-suspended.
func (s Status) IsSuspended() bool { return s == StatusSuspended }

// IsDeleted reports whether the resource is soft-deleted (terminal).
func (s Status) IsDeleted() bool { return s == StatusDeleted }

// IsExpired reports whether the resource carries the deferred-expiry
// terminal status. Note: this is the STATUS-COLUMN predicate, distinct
// from IsPastTTL which derives expiry from expires_at vs now.
func (s Status) IsExpired() bool { return s == StatusExpired }

// IsTerminal reports whether the resource is in a state it can never
// transition out of. A terminal resource has no live backing infra and
// must not be re-activated.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusExpired, StatusDeleted:
		return true
	default:
		return false
	}
}

// IsReapable reports whether a TTL-expiry sweep is allowed to act on a
// resource in this status. The worker's anonymous/free reaper deprovisions
// and marks deleted only rows in a non-terminal status — a paused or
// suspended resource whose TTL has elapsed is still reapable (TTL wins
// over lifecycle state), but an already-deleted/expired row is not.
func (s Status) IsReapable() bool {
	switch s {
	case StatusActive, StatusPaused, StatusSuspended:
		return true
	default:
		return false
	}
}

// ReapableStatuses returns the statuses IsReapable accepts, as raw
// strings, ready to splice into a SQL `status IN (...)` clause. Keeping
// the SQL filter derived from the same enum prevents the SQL predicate
// and the Go predicate from drifting.
func ReapableStatuses() []string {
	out := make([]string, 0, 3)
	for _, s := range AllStatuses() {
		if s.IsReapable() {
			out = append(out, s.String())
		}
	}
	return out
}

// IsPastTTL reports whether a resource with the given expires_at value
// is past its TTL relative to now. A zero expiresAt (no TTL — a permanent
// claimed resource) is never past TTL.
//
// This is the canonical "is this resource expired by the clock" predicate.
// It is deliberately separate from Status.IsExpired (the status-column
// predicate): an anonymous resource can be status='active' AND past its
// TTL in the window between TTL elapse and the next reaper tick.
func IsPastTTL(expiresAt time.Time, now time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !now.Before(expiresAt)
}
