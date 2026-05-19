package resourcestatus

import "time"

// ExpiryStage names the time-to-expiry bucket a TTL-bearing resource is
// currently in. It is the canonical replacement for the worker's
// hand-rolled `reminderStage` / `selectStage` logic, so the api and the
// worker agree on exactly which window a resource falls in.
//
// The buckets follow the 3-stage reminder cadence (12h / 6h / 1h) the
// expiry-warning jobs send. Index values are 1-based and flow into the
// reminder email as `reminder_index`; the string values are stable and
// flow into the email as `stage_label`.
type ExpiryStage int

const (
	// ExpiryStageNone — the resource is either permanent (no TTL) or its
	// TTL is further out than the widest reminder window. No warning due.
	ExpiryStageNone ExpiryStage = 0

	// ExpiryStage12h — the resource expires within the 12h window but
	// more than 6h out. First reminder ("12h to go").
	ExpiryStage12h ExpiryStage = 1

	// ExpiryStage6h — the resource expires within 6h but more than 1h
	// out. Second reminder ("6h to go").
	ExpiryStage6h ExpiryStage = 2

	// ExpiryStage1h — the resource expires within 1h (but is not yet
	// past TTL). Final reminder ("1h to go").
	ExpiryStage1h ExpiryStage = 3

	// ExpiryStagePastTTL — the resource's TTL has already elapsed. The
	// reaper, not the reminder job, owns this state.
	ExpiryStagePastTTL ExpiryStage = 4
)

// expiryStageWindow12h / 6h / 1h are the canonical reminder thresholds.
// A resource fires a stage when its expires_at is within the named window
// of now. Exported as named durations so callers never re-type "12h".
const (
	ExpiryWindow12h = 12 * time.Hour
	ExpiryWindow6h  = 6 * time.Hour
	ExpiryWindow1h  = 1 * time.Hour
)

// expiryStageDef pairs a stage with the window it fires in. Ordered
// most-distant → most-imminent so DeriveExpiryStage's "last match wins"
// scan picks the tightest window the resource currently sits in.
type expiryStageDef struct {
	stage  ExpiryStage
	within time.Duration
	label  string
}

// expirySchedule is the canonical stage table. This is the single
// definition the worker's reminder job iterates — there is no second
// copy of the 12h/6h/1h thresholds anywhere.
var expirySchedule = []expiryStageDef{
	{stage: ExpiryStage12h, within: ExpiryWindow12h, label: "stage_12h"},
	{stage: ExpiryStage6h, within: ExpiryWindow6h, label: "stage_6h"},
	{stage: ExpiryStage1h, within: ExpiryWindow1h, label: "stage_1h"},
}

// AllExpiryStages returns every ExpiryStage value, ordered none → past
// TTL. The exhaustive test iterates this slice so a stage added above
// without being handled in Index/Label/AllExpiryStages fails the build.
func AllExpiryStages() []ExpiryStage {
	return []ExpiryStage{
		ExpiryStageNone,
		ExpiryStage12h,
		ExpiryStage6h,
		ExpiryStage1h,
		ExpiryStagePastTTL,
	}
}

// Index returns the 1-based reminder index for a warning stage (the value
// stamped into resources.reminders_sent and the email's reminder_index).
// ExpiryStageNone and ExpiryStagePastTTL return 0 — neither sends a
// numbered reminder.
func (s ExpiryStage) Index() int {
	switch s {
	case ExpiryStage12h:
		return 1
	case ExpiryStage6h:
		return 2
	case ExpiryStage1h:
		return 3
	default:
		return 0
	}
}

// Label returns the stable string label for a stage, used as the
// `stage_label` field in the expiry-warning email and in log lines.
func (s ExpiryStage) Label() string {
	switch s {
	case ExpiryStageNone:
		return "none"
	case ExpiryStage12h:
		return "stage_12h"
	case ExpiryStage6h:
		return "stage_6h"
	case ExpiryStage1h:
		return "stage_1h"
	case ExpiryStagePastTTL:
		return "past_ttl"
	default:
		return "unknown"
	}
}

// IsWarning reports whether the stage is one of the three warning stages
// (12h / 6h / 1h) — i.e. a reminder email is due. ExpiryStageNone and
// ExpiryStagePastTTL are not warning stages.
func (s ExpiryStage) IsWarning() bool {
	switch s {
	case ExpiryStage12h, ExpiryStage6h, ExpiryStage1h:
		return true
	default:
		return false
	}
}

// DeriveExpiryStage classifies a resource by its expires_at relative to
// now. A zero expiresAt (a permanent claimed resource with no TTL) always
// returns ExpiryStageNone.
//
// The classification picks the MOST IMMINENT window the time-to-expiry
// falls in (schedule is ordered most-distant → most-imminent, last match
// wins). This is the fix the worker's selectStage already carries
// (P2-12, BugBash 2026-05-18): a short-TTL resource created less than 6h
// before its TTL must report stage_6h / stage_1h, never a mislabelled
// stage_12h. Centralising it here means api and worker can never disagree
// on the bucket.
func DeriveExpiryStage(expiresAt time.Time, now time.Time) ExpiryStage {
	if expiresAt.IsZero() {
		return ExpiryStageNone
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return ExpiryStagePastTTL
	}
	stage := ExpiryStageNone
	for _, def := range expirySchedule {
		if remaining <= def.within {
			// Later matches overwrite earlier ones — the final match is
			// the tightest window the resource currently sits in.
			stage = def.stage
		}
	}
	return stage
}

// HoursUntilExpiry rounds the gap between now and expiresAt up to whole
// hours, with a floor of 1 so a warning email never says "0 hours". A
// zero expiresAt or a past-TTL resource returns 0.
//
// This replaces the worker's private hoursLeft helper — the floor-of-1
// behaviour is identical, kept so the email copy never regresses.
func HoursUntilExpiry(expiresAt time.Time, now time.Time) int {
	if expiresAt.IsZero() {
		return 0
	}
	delta := expiresAt.Sub(now)
	if delta <= 0 {
		return 0
	}
	if delta <= time.Hour {
		return 1
	}
	hours := int(delta.Hours())
	if delta-time.Duration(hours)*time.Hour > 0 {
		hours++
	}
	if hours < 1 {
		hours = 1
	}
	return hours
}
