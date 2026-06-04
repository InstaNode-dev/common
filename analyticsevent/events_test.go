package analyticsevent

import (
	"context"
	"testing"
)

func TestFlowTest_Attrs(t *testing.T) {
	f := FlowTest{
		Flow:           "db_new",
		Actor:          ActorAgentCurl,
		Tier:           "anonymous",
		Layer:          "api",
		Result:         ResultPass,
		LatencyMs:      142,
		CommitID:       "abc1234",
		SyntheticRunID: "run-uuid",
	}
	a := f.Attrs()
	if a[AttrFlow] != "db_new" || a[AttrActor] != ActorAgentCurl ||
		a[AttrTier] != "anonymous" || a[AttrLayer] != "api" ||
		a[AttrResult] != ResultPass || a[AttrLatencyMs] != int64(142) ||
		a[AttrCommitID] != "abc1234" || a[AttrSyntheticRunID] != "run-uuid" {
		t.Fatalf("Attrs mismatch: %v", a)
	}
	// Cohort is always synthetic for flow tests.
	if a[AttrCohort] != CohortSynthetic {
		t.Fatalf("cohort = %v, want %q", a[AttrCohort], CohortSynthetic)
	}
	// Empty Reason on pass is omitted (no "" placeholder).
	if _, ok := a[AttrReason]; ok {
		t.Errorf("empty Reason should be omitted, got %v", a[AttrReason])
	}
}

func TestFlowTest_Attrs_FailIncludesReason(t *testing.T) {
	f := FlowTest{Flow: "deploy_new", Result: ResultFail, Reason: "503 from build pod"}
	a := f.Attrs()
	if a[AttrReason] != "503 from build pod" {
		t.Fatalf("Reason not included on fail: %v", a)
	}
}

func TestFlowTest_Attrs_AllAllowlisted(t *testing.T) {
	// A populated FlowTest must produce only allowlisted (PII-safe) keys.
	f := FlowTest{Flow: "x", Actor: "a", Tier: "t", Layer: "l", Result: "r",
		Reason: "why", CommitID: "c", SyntheticRunID: "s", LatencyMs: 1}
	for k := range f.Attrs() {
		if _, ok := AllowedAttributes[k]; !ok {
			t.Errorf("FlowTest emits non-allowlisted key %q", k)
		}
	}
}

func TestRecordFlowTest(t *testing.T) {
	rec := &recorder{}
	e := Wrap(rec)
	RecordFlowTest(context.Background(), e, FlowTest{Flow: "cache_new", Result: ResultPass})
	got := rec.last()
	if got.eventType != EventFlowTest {
		t.Fatalf("eventType = %q, want %q", got.eventType, EventFlowTest)
	}
	if got.attrs[AttrFlow] != "cache_new" {
		t.Fatalf("flow not recorded: %v", got.attrs)
	}
}

func TestRecordFlowTest_NilEmitterSafe(t *testing.T) {
	// Must not panic with a nil emitter.
	RecordFlowTest(context.Background(), nil, FlowTest{Flow: "x"})
}

func TestNowUnixMilli(t *testing.T) {
	// Retained single time source — exercise it so it stays live.
	if nowUnixMilli() <= 0 {
		t.Fatal("nowUnixMilli returned non-positive")
	}
}

func TestEventAndAttrConstantsStable(t *testing.T) {
	// Guard against accidental rename of the wire contract (dashboards FACET on
	// these exact strings).
	if EventFunnel != "InstantFunnel" || EventFlowTest != "InstantFlowTest" ||
		EventChurnSignal != "InstantChurnSignal" || EventAbuseSignal != "InstantAbuseSignal" {
		t.Error("event-type wire contract changed")
	}
}
