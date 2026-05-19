package resourcestatus_test

import (
	"testing"
	"time"

	"instant.dev/common/resourcestatus"
)

// TestStatusPredicates_ExhaustiveOverEnum is the exhaustive table test
// over every ResourceStatus value. It asserts the expected truth value
// of every status predicate for every status. The `seen` map is checked
// against AllStatuses() at the end: a Status constant added to the
// package without a row here fails the build.
func TestStatusPredicates_ExhaustiveOverEnum(t *testing.T) {
	type want struct {
		valid     bool
		active    bool
		paused    bool
		suspended bool
		expired   bool
		deleted   bool
		terminal  bool
		reapable  bool
	}

	cases := map[resourcestatus.Status]want{
		resourcestatus.StatusActive: {
			valid: true, active: true, reapable: true,
		},
		resourcestatus.StatusPaused: {
			valid: true, paused: true, reapable: true,
		},
		resourcestatus.StatusSuspended: {
			valid: true, suspended: true, reapable: true,
		},
		resourcestatus.StatusExpired: {
			valid: true, expired: true, terminal: true,
		},
		resourcestatus.StatusDeleted: {
			valid: true, deleted: true, terminal: true,
		},
	}

	for _, s := range resourcestatus.AllStatuses() {
		w, ok := cases[s]
		if !ok {
			t.Fatalf("status %q has no expectation row — add it to the cases map "+
				"(this is the exhaustiveness guard for ResourceStatus)", s)
		}
		if got := s.Valid(); got != w.valid {
			t.Errorf("%q.Valid() = %v, want %v", s, got, w.valid)
		}
		if got := s.IsActive(); got != w.active {
			t.Errorf("%q.IsActive() = %v, want %v", s, got, w.active)
		}
		if got := s.IsPaused(); got != w.paused {
			t.Errorf("%q.IsPaused() = %v, want %v", s, got, w.paused)
		}
		if got := s.IsSuspended(); got != w.suspended {
			t.Errorf("%q.IsSuspended() = %v, want %v", s, got, w.suspended)
		}
		if got := s.IsExpired(); got != w.expired {
			t.Errorf("%q.IsExpired() = %v, want %v", s, got, w.expired)
		}
		if got := s.IsDeleted(); got != w.deleted {
			t.Errorf("%q.IsDeleted() = %v, want %v", s, got, w.deleted)
		}
		if got := s.IsTerminal(); got != w.terminal {
			t.Errorf("%q.IsTerminal() = %v, want %v", s, got, w.terminal)
		}
		if got := s.IsReapable(); got != w.reapable {
			t.Errorf("%q.IsReapable() = %v, want %v", s, got, w.reapable)
		}
		if s.String() != string(s) {
			t.Errorf("%q.String() mismatch", s)
		}
	}

	// Cross-check: cases must not contain a key that AllStatuses omits.
	if len(cases) != len(resourcestatus.AllStatuses()) {
		t.Fatalf("cases has %d rows but AllStatuses() has %d — they must match",
			len(cases), len(resourcestatus.AllStatuses()))
	}
}

func TestParse(t *testing.T) {
	for _, s := range resourcestatus.AllStatuses() {
		got, ok := resourcestatus.Parse(string(s))
		if !ok || got != s {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, true)", s, got, ok, s)
		}
	}
	if got, ok := resourcestatus.Parse("nonsense"); ok || got.Valid() {
		t.Errorf("Parse(\"nonsense\") = (%q, %v), want (_, false)", got, ok)
	}
	if _, ok := resourcestatus.Parse(""); ok {
		t.Errorf("Parse(\"\") should be invalid")
	}
}

func TestReapableStatuses(t *testing.T) {
	got := resourcestatus.ReapableStatuses()
	want := []string{"active", "paused", "suspended"}
	if len(got) != len(want) {
		t.Fatalf("ReapableStatuses() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ReapableStatuses()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Every returned status must actually be reapable, and every reapable
	// status must be present — derived from the same enum, no drift.
	for _, s := range resourcestatus.AllStatuses() {
		inList := false
		for _, r := range got {
			if r == string(s) {
				inList = true
			}
		}
		if inList != s.IsReapable() {
			t.Errorf("status %q: in ReapableStatuses()=%v but IsReapable()=%v",
				s, inList, s.IsReapable())
		}
	}
}

func TestIsPastTTL(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"zero expiresAt is never past TTL", time.Time{}, false},
		{"1h in the future", now.Add(time.Hour), false},
		{"exactly now is past TTL", now, true},
		{"1ns in the past", now.Add(-time.Nanosecond), true},
		{"1h in the past", now.Add(-time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourcestatus.IsPastTTL(tc.expiresAt, now); got != tc.want {
				t.Errorf("IsPastTTL = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDeriveExpiryStage_ExhaustiveOverStagesAndBoundaries covers every
// ExpiryStage value and every window boundary (12h / 6h / 1h / 0h),
// checking both sides of each boundary. The seen map at the end is
// checked against AllExpiryStages(): a stage added without a boundary
// case fails the build.
func TestDeriveExpiryStage_ExhaustiveOverStagesAndBoundaries(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		expiresAt time.Time
		want      resourcestatus.ExpiryStage
	}{
		{"zero expiresAt → None", time.Time{}, resourcestatus.ExpiryStageNone},
		{"24h out → None (beyond widest window)", now.Add(24 * time.Hour), resourcestatus.ExpiryStageNone},
		{"just over 12h → None", now.Add(12*time.Hour + time.Minute), resourcestatus.ExpiryStageNone},
		{"exactly 12h → Stage12h (inclusive)", now.Add(12 * time.Hour), resourcestatus.ExpiryStage12h},
		{"10h out → Stage12h", now.Add(10 * time.Hour), resourcestatus.ExpiryStage12h},
		{"just over 6h → Stage12h", now.Add(6*time.Hour + time.Minute), resourcestatus.ExpiryStage12h},
		{"exactly 6h → Stage6h (inclusive, tighter window wins)", now.Add(6 * time.Hour), resourcestatus.ExpiryStage6h},
		{"4h out → Stage6h", now.Add(4 * time.Hour), resourcestatus.ExpiryStage6h},
		{"just over 1h → Stage6h", now.Add(time.Hour + time.Minute), resourcestatus.ExpiryStage6h},
		{"exactly 1h → Stage1h (inclusive)", now.Add(time.Hour), resourcestatus.ExpiryStage1h},
		{"40m out → Stage1h", now.Add(40 * time.Minute), resourcestatus.ExpiryStage1h},
		{"1ns out → Stage1h", now.Add(time.Nanosecond), resourcestatus.ExpiryStage1h},
		{"exactly now → PastTTL", now, resourcestatus.ExpiryStagePastTTL},
		{"1h in the past → PastTTL", now.Add(-time.Hour), resourcestatus.ExpiryStagePastTTL},
	}

	seen := map[resourcestatus.ExpiryStage]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourcestatus.DeriveExpiryStage(tc.expiresAt, now)
			if got != tc.want {
				t.Errorf("DeriveExpiryStage(%v) = %v, want %v", tc.expiresAt, got, tc.want)
			}
		})
		seen[tc.want] = true
	}

	for _, stage := range resourcestatus.AllExpiryStages() {
		if !seen[stage] {
			t.Errorf("ExpiryStage %v (%q) has no boundary case — add one "+
				"(this is the exhaustiveness guard for ExpiryStage)",
				stage, stage.Label())
		}
	}
}

// TestExpiryStageMetadata_ExhaustiveOverEnum asserts Index, Label, and
// IsWarning for every ExpiryStage value.
func TestExpiryStageMetadata_ExhaustiveOverEnum(t *testing.T) {
	type want struct {
		index   int
		label   string
		warning bool
	}
	cases := map[resourcestatus.ExpiryStage]want{
		resourcestatus.ExpiryStageNone:    {index: 0, label: "none", warning: false},
		resourcestatus.ExpiryStage12h:     {index: 1, label: "stage_12h", warning: true},
		resourcestatus.ExpiryStage6h:      {index: 2, label: "stage_6h", warning: true},
		resourcestatus.ExpiryStage1h:      {index: 3, label: "stage_1h", warning: true},
		resourcestatus.ExpiryStagePastTTL: {index: 0, label: "past_ttl", warning: false},
	}
	for _, stage := range resourcestatus.AllExpiryStages() {
		w, ok := cases[stage]
		if !ok {
			t.Fatalf("ExpiryStage %v has no expectation row — add it to the cases map", stage)
		}
		if got := stage.Index(); got != w.index {
			t.Errorf("%v.Index() = %d, want %d", stage, got, w.index)
		}
		if got := stage.Label(); got != w.label {
			t.Errorf("%v.Label() = %q, want %q", stage, got, w.label)
		}
		if got := stage.IsWarning(); got != w.warning {
			t.Errorf("%v.IsWarning() = %v, want %v", stage, got, w.warning)
		}
	}
	if len(cases) != len(resourcestatus.AllExpiryStages()) {
		t.Fatalf("cases has %d rows but AllExpiryStages() has %d — they must match",
			len(cases), len(resourcestatus.AllExpiryStages()))
	}
}

func TestHoursUntilExpiry(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		expiresAt time.Time
		want      int
	}{
		{"zero expiresAt → 0", time.Time{}, 0},
		{"past TTL → 0", now.Add(-time.Hour), 0},
		{"exactly now → 0", now, 0},
		{"30m out → 1 (floor)", now.Add(30 * time.Minute), 1},
		{"exactly 1h → 1", now.Add(time.Hour), 1},
		{"61m out → 2 (rounds up)", now.Add(61 * time.Minute), 2},
		{"exactly 2h → 2", now.Add(2 * time.Hour), 2},
		{"10h out → 10", now.Add(10 * time.Hour), 10},
		{"10h30m out → 11 (rounds up)", now.Add(10*time.Hour + 30*time.Minute), 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourcestatus.HoursUntilExpiry(tc.expiresAt, now); got != tc.want {
				t.Errorf("HoursUntilExpiry = %d, want %d", got, tc.want)
			}
		})
	}
}
