// Package readiness implements the deep, component-by-component
// readiness check shared across the api / worker / provisioner services.
//
// Why this exists (RETRO-2026-05-15 + Brevo silent-rejection, 2026-05-20):
//   /healthz is the Kubernetes livenessProbe — its job is "should this
//   pod be SIGKILLed and restarted". Deep upstream checks (Brevo,
//   Razorpay, DO Spaces, etc.) MUST NOT be wired to liveness — a Brevo
//   brownout would otherwise cycle every api pod into a restart loop.
//
//   /readyz is wired to the readinessProbe and answers a different
//   question: "should this pod be in the Service endpoints right now".
//   A pod that can't reach its critical dependencies (platform_db,
//   provisioner gRPC) is degraded and should be pulled out of rotation
//   so traffic shifts to a healthy pod; a pod that can't reach a
//   non-critical upstream (Brevo) stays in rotation but surfaces
//   "degraded" so the NR alert fires for the operator.
//
// The package is intentionally framework-free — the HTTP handler is a
// vanilla net/http.HandlerFunc so it can be mounted on Fiber (api),
// net/http.ServeMux (worker, provisioner sidecar), or any other router
// without dragging Fiber into common/.
//
// CONTRACT — every check returns CheckResult with:
//   - Name           the wire-stable label (e.g. "platform_db")
//   - Status         one of "ok", "degraded", "failed"
//   - LatencyMS      duration of the most recent execution
//   - LastError      empty when Status=="ok"; populated otherwise
//   - LastCheckAt    RFC3339 timestamp of the most recent execution
//   - Critical       true if a failed status should fail the overall probe
//
// Overall status derivation lives in DeriveOverall — any "failed" on a
// Critical check returns "failed"+503, any non-critical "failed" or any
// "degraded" returns "degraded"+200, otherwise "ok"+200.
//
// SECRETS — check implementations MUST NOT include secret material in
// LastError (e.g. the Brevo API key in a probe URL). The shared scrub()
// helper in checks.go redacts known secret shapes (DB passwords, URL
// credentials, Bearer tokens, hex strings >=32, xkeysib-/sk-/rzp_
// prefixes) BEFORE truncating to 80 chars. Truncate-first leaks the
// secret in the first 80 chars of the upstream message — Wave-3 audit
// 2026-05-21. See the adapters in api/internal/handlers/readyz.go for
// the canonical pattern.
package readiness

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"instant.dev/common/buildinfo"
)

// Status is the wire-stable enum returned by each check.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
)

// CheckResult is the per-component answer surfaced to the operator.
// It is also the value stored in the cache between probe ticks.
type CheckResult struct {
	Name        string    `json:"name"`
	Status      Status    `json:"status"`
	LatencyMS   int64     `json:"latency_ms"`
	LastError   string    `json:"last_error,omitempty"`
	LastCheckAt time.Time `json:"last_check_at"`
	// Critical is excluded from JSON — it's a configuration property of
	// the check, not part of the wire shape. Operators read the overall
	// status field if they want "is this fatal" semantics.
	Critical bool `json:"-"`
}

// CheckFunc is the function each component supplies. It returns a
// CheckResult populated with at minimum Status + LastError; the runner
// fills in Name / LatencyMS / LastCheckAt / Critical from the Check
// metadata. The runner enforces a per-check timeout via ctx.
type CheckFunc func(ctx context.Context) CheckResult

// Check is one registered component. Name and Critical are static;
// Fn is invoked on each cache-miss tick.
type Check struct {
	Name     string
	Critical bool
	Fn       CheckFunc
}

// Response is the JSON envelope returned by GET /readyz. Field order
// matches the operator's read order: overall status, who's serving,
// what commit, then the per-component breakdown.
type Response struct {
	Overall  Status        `json:"overall"`
	Service  string        `json:"service"`
	CommitID string        `json:"commit_id"`
	Checks   []CheckResult `json:"checks"`
}

// MetricsSink is the optional Prometheus hook. The handler calls
// Observe(name, status) for every check on every successful probe so
// the gauge series stays fresh. Wiring is optional — a nil sink is
// fine for tests and local dev.
type MetricsSink interface {
	Observe(name string, status Status)
}

// Runner owns the registered checks + the per-check cache. One Runner
// per process (per HTTP server). It is safe for concurrent use — every
// probe request that arrives within a check's TTL is served from cache
// without re-hitting the upstream.
type Runner struct {
	service    string
	checks     []Check
	cache      sync.Map // name → *cachedResult
	ttl        time.Duration
	overallTO  time.Duration
	now        func() time.Time
	metrics    MetricsSink
	criticalBy map[string]bool
}

type cachedResult struct {
	mu     sync.Mutex
	result CheckResult
	expiry time.Time
}

// Config tunes per-process behaviour. Default values are conservative —
// 10s cache TTL keeps upstream calls at ~6/min/pod under k8s default
// probe periodSeconds=10 (one cache fill, nine hits), and the 3s overall
// timeout lets each check race in parallel without any one stalling
// /readyz past the readinessProbe timeoutSeconds=5 default.
type Config struct {
	Service        string
	CacheTTL       time.Duration // per-check cache window; default 10s
	OverallTimeout time.Duration // wall-clock budget for one probe; default 3s
	Metrics        MetricsSink   // optional
	Now            func() time.Time
}

// NewRunner wires up the runner with the supplied checks. The checks
// slice is not mutated — the runner stores its own copy keyed by name
// for cache lookups.
func NewRunner(cfg Config, checks []Check) *Runner {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 10 * time.Second
	}
	if cfg.OverallTimeout <= 0 {
		cfg.OverallTimeout = 3 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Runner{
		service:    cfg.Service,
		checks:     append([]Check(nil), checks...),
		ttl:        cfg.CacheTTL,
		overallTO:  cfg.OverallTimeout,
		now:        cfg.Now,
		metrics:    cfg.Metrics,
		criticalBy: make(map[string]bool, len(checks)),
	}
	for _, c := range checks {
		r.criticalBy[c.Name] = c.Critical
	}
	return r
}

// Run executes every check (in parallel, bounded by OverallTimeout),
// honoring the per-check cache. Returns a fully populated Response and
// the HTTP status code the handler should write.
//
// The cache is keyed by Check.Name. Within a TTL window, every probe
// arrival reuses the last result without re-invoking Fn. Outside the
// TTL the next probe re-invokes Fn under a per-check mutex so that
// concurrent probes don't dogpile the upstream. This is critical for
// the Brevo / Razorpay checks where every extra HTTP roundtrip costs
// budget against the upstream's own rate limit.
func (r *Runner) Run(ctx context.Context) (Response, int) {
	ctx, cancel := context.WithTimeout(ctx, r.overallTO)
	defer cancel()

	results := make([]CheckResult, len(r.checks))
	var wg sync.WaitGroup
	for i, c := range r.checks {
		wg.Add(1)
		go func(idx int, chk Check) {
			defer wg.Done()
			results[idx] = r.runOne(ctx, chk)
		}(i, c)
	}
	wg.Wait()

	// Sort by name so the wire shape is stable for snapshot tests.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	if r.metrics != nil {
		for _, res := range results {
			r.metrics.Observe(res.Name, res.Status)
		}
	}

	overall := DeriveOverall(results, r.criticalBy)
	code := http.StatusOK
	if overall == StatusFailed {
		code = http.StatusServiceUnavailable
	}
	return Response{
		Overall:  overall,
		Service:  r.service,
		CommitID: buildinfo.GitSHA,
		Checks:   results,
	}, code
}

// runOne runs a single check honoring the cache. The first call within
// a TTL window populates the cache from Fn; subsequent calls within the
// window return the cached value without touching Fn.
func (r *Runner) runOne(ctx context.Context, c Check) CheckResult {
	v, _ := r.cache.LoadOrStore(c.Name, &cachedResult{})
	cr := v.(*cachedResult)

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if !cr.result.LastCheckAt.IsZero() && r.now().Before(cr.expiry) {
		// Cache hit — return the stored result. Critical is taken
		// from the runner's static map so a check renamed Critical
		// at boot is honored even on a cache hit.
		cached := cr.result
		cached.Critical = c.Critical
		return cached
	}

	start := r.now()
	res := safeInvoke(ctx, c.Fn)
	res.Name = c.Name
	res.Critical = c.Critical
	res.LastCheckAt = start
	res.LatencyMS = r.now().Sub(start).Milliseconds()

	cr.result = res
	cr.expiry = r.now().Add(r.ttl)
	return res
}

// safeInvoke calls fn with panic recovery. A check that panics is
// surfaced as "failed" with a generic error string — we never want a
// rogue check to take down /readyz itself.
func safeInvoke(ctx context.Context, fn CheckFunc) (res CheckResult) {
	defer func() {
		if rec := recover(); rec != nil {
			res = CheckResult{
				Status:    StatusFailed,
				LastError: "check_panic",
			}
		}
	}()
	if fn == nil {
		return CheckResult{
			Status:    StatusFailed,
			LastError: "check_nil",
		}
	}
	return fn(ctx)
}

// DeriveOverall implements the per-service rule:
//   - any Critical failed → overall=failed (HTTP 503)
//   - any other failed or any degraded → overall=degraded (HTTP 200)
//   - all ok → overall=ok (HTTP 200)
//
// criticalBy maps check name → whether that check is critical for this
// service. A check not in the map is treated as non-critical.
func DeriveOverall(results []CheckResult, criticalBy map[string]bool) Status {
	overall := StatusOK
	for _, r := range results {
		if r.Status == StatusFailed && criticalBy[r.Name] {
			return StatusFailed
		}
		if r.Status == StatusFailed || r.Status == StatusDegraded {
			overall = StatusDegraded
		}
	}
	return overall
}

// Handler returns a net/http.HandlerFunc that serves the readiness probe.
// The handler is read-only: it never mutates the Runner. Mount it on any
// router that speaks net/http.
//
// On a runner-wide timeout (every check exceeded OverallTimeout) the
// handler still returns 200/503 based on whatever per-check results
// landed before the deadline; checks that didn't return are surfaced as
// "failed" with LastError="timeout" by the per-adapter check function.
func Handler(r *Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		resp, code := r.Run(req.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
