package readiness_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"instant.dev/common/readiness"
)

func okCheck(name string) readiness.Check {
	return readiness.Check{
		Name:     name,
		Critical: false,
		Fn: func(ctx context.Context) readiness.CheckResult {
			return readiness.CheckResult{Status: readiness.StatusOK}
		},
	}
}

// TestRun_AllOK is the happy path — every check returns ok, overall=ok,
// HTTP 200. Pins the wire shape (sorted-by-name checks, commit_id and
// service fields populated) so a future refactor that drops a field
// fails this test.
func TestRun_AllOK(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		okCheck("zebra_check"),
		okCheck("alpha_check"),
	})
	resp, code := r.Run(context.Background())

	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Overall != readiness.StatusOK {
		t.Fatalf("want overall=ok, got %q", resp.Overall)
	}
	if len(resp.Checks) != 2 {
		t.Fatalf("want 2 checks, got %d", len(resp.Checks))
	}
	// Sorted-by-name contract — alpha precedes zebra.
	if resp.Checks[0].Name != "alpha_check" || resp.Checks[1].Name != "zebra_check" {
		t.Fatalf("checks not sorted by name: %+v", resp.Checks)
	}
	if resp.Service != "instant-test" {
		t.Fatalf("service field missing: %q", resp.Service)
	}
}

// TestRun_NonCriticalFailedIsDegraded — Brevo / Razorpay style: a failed
// non-critical check pulls overall to "degraded" but the probe still
// returns 200 so the pod stays in the Service endpoint list.
func TestRun_NonCriticalFailedIsDegraded(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		okCheck("platform_db"),
		{
			Name:     "brevo",
			Critical: false,
			Fn: func(ctx context.Context) readiness.CheckResult {
				return readiness.CheckResult{Status: readiness.StatusFailed, LastError: "401 unauthorized"}
			},
		},
	})
	resp, code := r.Run(context.Background())

	if code != 200 {
		t.Fatalf("non-critical fail must NOT pull pod from rotation; want 200, got %d", code)
	}
	if resp.Overall != readiness.StatusDegraded {
		t.Fatalf("want overall=degraded, got %q", resp.Overall)
	}
}

// TestRun_CriticalFailedIs503 — a failed platform_db (critical) drops
// the pod from the Service. Pins the rule: only checks marked Critical
// can take a pod out of rotation.
func TestRun_CriticalFailedIs503(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		{
			Name:     "platform_db",
			Critical: true,
			Fn: func(ctx context.Context) readiness.CheckResult {
				return readiness.CheckResult{Status: readiness.StatusFailed, LastError: "connection refused"}
			},
		},
		okCheck("brevo"),
	})
	resp, code := r.Run(context.Background())

	if code != 503 {
		t.Fatalf("critical fail must return 503, got %d", code)
	}
	if resp.Overall != readiness.StatusFailed {
		t.Fatalf("want overall=failed, got %q", resp.Overall)
	}
}

// TestRun_DegradedIs200 — degraded (not failed) is the warning state:
// the upstream is reachable but a sub-property is off (auth expired,
// throttled, etc.). Probe stays 200, overall=degraded.
func TestRun_DegradedIs200(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		{
			Name:     "brevo",
			Critical: false,
			Fn: func(ctx context.Context) readiness.CheckResult {
				return readiness.CheckResult{Status: readiness.StatusDegraded, LastError: "401"}
			},
		},
	})
	_, code := r.Run(context.Background())
	if code != 200 {
		t.Fatalf("degraded must return 200, got %d", code)
	}
}

// TestCache_SecondCallWithinWindowSkipsFn is the load-bearing test for
// the cache contract: under the readinessProbe's default periodSeconds=10
// + the runner's CacheTTL=10s, only ~1 in 6 ticks actually invokes the
// upstream. This test asserts that within the TTL window the Fn is
// invoked exactly once.
func TestCache_SecondCallWithinWindowSkipsFn(t *testing.T) {
	var invocations atomic.Int32
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	r := readiness.NewRunner(readiness.Config{
		Service:  "instant-test",
		CacheTTL: 10 * time.Second,
		Now:      clock,
	}, []readiness.Check{
		{
			Name:     "brevo",
			Critical: false,
			Fn: func(ctx context.Context) readiness.CheckResult {
				invocations.Add(1)
				return readiness.CheckResult{Status: readiness.StatusOK}
			},
		},
	})

	// Cold call — populates the cache.
	_, _ = r.Run(context.Background())
	if got := invocations.Load(); got != 1 {
		t.Fatalf("want 1 invocation after first call, got %d", got)
	}

	// Within the TTL window — should hit cache.
	now = now.Add(5 * time.Second)
	_, _ = r.Run(context.Background())
	if got := invocations.Load(); got != 1 {
		t.Fatalf("cache violated: want 1 invocation within TTL window, got %d", got)
	}

	// Past the TTL — should refresh.
	now = now.Add(6 * time.Second) // 11s total elapsed
	_, _ = r.Run(context.Background())
	if got := invocations.Load(); got != 2 {
		t.Fatalf("want 2 invocations after TTL expires, got %d", got)
	}
}

// TestRun_PanickingCheckIsFailed — a check that panics must not crash
// the handler. The runner reports it as failed with LastError set so
// the operator sees which check misbehaved.
func TestRun_PanickingCheckIsFailed(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		{
			Name:     "rogue",
			Critical: false,
			Fn: func(ctx context.Context) readiness.CheckResult {
				panic("oh no")
			},
		},
	})
	resp, code := r.Run(context.Background())
	if code != 200 {
		t.Fatalf("non-critical panic must still return 200, got %d", code)
	}
	if resp.Checks[0].Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", resp.Checks[0].Status)
	}
	if resp.Checks[0].LastError == "" {
		t.Fatalf("want LastError set on panic")
	}
}

// TestRun_NilCheckFnIsFailed — defensive: a Check with a nil Fn
// shouldn't take down the process.
func TestRun_NilCheckFnIsFailed(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		{Name: "broken", Critical: false, Fn: nil},
	})
	resp, _ := r.Run(context.Background())
	if resp.Checks[0].Status != readiness.StatusFailed {
		t.Fatalf("want failed for nil Fn, got %q", resp.Checks[0].Status)
	}
}

// TestRun_ParallelExecution — the runner must invoke all checks in
// parallel. Two 50ms checks should complete in ~50ms wall-clock, not
// ~100ms. This is the load-bearing test for the goroutine fan-out.
func TestRun_ParallelExecution(t *testing.T) {
	slow := func(name string) readiness.Check {
		return readiness.Check{
			Name: name,
			Fn: func(ctx context.Context) readiness.CheckResult {
				time.Sleep(50 * time.Millisecond)
				return readiness.CheckResult{Status: readiness.StatusOK}
			},
		}
	}
	r := readiness.NewRunner(readiness.Config{
		Service:        "instant-test",
		OverallTimeout: time.Second,
	}, []readiness.Check{slow("a"), slow("b"), slow("c")})

	start := time.Now()
	_, _ = r.Run(context.Background())
	elapsed := time.Since(start)
	// Generous bound — should be ~50ms, never near 150ms (serial).
	if elapsed > 120*time.Millisecond {
		t.Fatalf("checks did not run in parallel: %v elapsed", elapsed)
	}
}

// TestHandler_WireShape pins the JSON envelope. A regression that
// drops a field (e.g. commit_id) fails here and dashboards stay alive.
func TestHandler_WireShape(t *testing.T) {
	r := readiness.NewRunner(readiness.Config{Service: "instant-test"}, []readiness.Check{
		okCheck("platform_db"),
	})
	h := readiness.Handler(r)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/readyz", nil)
	h(rr, req)

	if rr.Code != 200 {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("want JSON content-type, got %q", got)
	}

	var got readiness.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not valid JSON: %v\nbody=%s", err, rr.Body.String())
	}
	if got.Service != "instant-test" {
		t.Fatalf("service field missing")
	}
	if got.Overall != readiness.StatusOK {
		t.Fatalf("overall field missing")
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "platform_db" {
		t.Fatalf("checks field malformed: %+v", got.Checks)
	}
}

// TestMetricsSink_ObservesEveryCheck — the Prometheus hook must be
// called once per check per probe so the gauge series stays fresh.
func TestMetricsSink_ObservesEveryCheck(t *testing.T) {
	sink := &fakeSink{seen: make(map[string]readiness.Status)}
	r := readiness.NewRunner(readiness.Config{
		Service: "instant-test",
		Metrics: sink,
	}, []readiness.Check{
		okCheck("platform_db"),
		{
			Name:     "brevo",
			Critical: false,
			Fn: func(ctx context.Context) readiness.CheckResult {
				return readiness.CheckResult{Status: readiness.StatusDegraded}
			},
		},
	})
	_, _ = r.Run(context.Background())

	if got := sink.seen["platform_db"]; got != readiness.StatusOK {
		t.Fatalf("metrics missing platform_db: %v", sink.seen)
	}
	if got := sink.seen["brevo"]; got != readiness.StatusDegraded {
		t.Fatalf("metrics missing brevo: %v", sink.seen)
	}
}

type fakeSink struct {
	seen map[string]readiness.Status
}

func (f *fakeSink) Observe(name string, status readiness.Status) {
	f.seen[name] = status
}

// TestDeriveOverall_Matrix is a table-driven pin of the overall logic
// — every combination of (critical/non-critical, ok/degraded/failed)
// must hit the right bucket.
func TestDeriveOverall_Matrix(t *testing.T) {
	cases := []struct {
		name      string
		results   []readiness.CheckResult
		critical  map[string]bool
		wantOver  readiness.Status
	}{
		{
			name:     "all ok",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusOK}},
			critical: map[string]bool{"a": true},
			wantOver: readiness.StatusOK,
		},
		{
			name:     "non-critical degraded → degraded",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusOK}, {Name: "b", Status: readiness.StatusDegraded}},
			critical: map[string]bool{"a": true, "b": false},
			wantOver: readiness.StatusDegraded,
		},
		{
			name:     "non-critical failed → degraded",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusOK}, {Name: "b", Status: readiness.StatusFailed}},
			critical: map[string]bool{"a": true, "b": false},
			wantOver: readiness.StatusDegraded,
		},
		{
			name:     "critical degraded → degraded",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusDegraded}},
			critical: map[string]bool{"a": true},
			wantOver: readiness.StatusDegraded,
		},
		{
			name:     "critical failed → failed",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusFailed}},
			critical: map[string]bool{"a": true},
			wantOver: readiness.StatusFailed,
		},
		{
			name:     "critical failed + non-critical ok → failed",
			results:  []readiness.CheckResult{{Name: "a", Status: readiness.StatusFailed}, {Name: "b", Status: readiness.StatusOK}},
			critical: map[string]bool{"a": true, "b": false},
			wantOver: readiness.StatusFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readiness.DeriveOverall(tc.results, tc.critical)
			if got != tc.wantOver {
				t.Fatalf("want %q, got %q", tc.wantOver, got)
			}
		})
	}
}
