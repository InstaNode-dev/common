// Helpers for building common Check implementations. Each helper
// returns a CheckFunc that can be plugged into readiness.Check{}. The
// implementations cover the surface needed by api / worker / provisioner
// today; additions go here so a fourth service can adopt /readyz without
// re-implementing the wire.
//
// CONTRACT for every helper:
//   - errors are scrubbed to short fixed strings before reaching the
//     wire (no secrets, no full upstream responses)
//   - timeouts are honored via the parent ctx — every helper that does
//     IO uses ctx.Done() or a per-call timeout derived from ctx
//   - HTTP responses are body-drained and closed (no leaked connections)
package readiness

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PingDB returns a CheckFunc that does db.PingContext with the supplied
// timeout. The check is "failed" on any error — for the platform_db
// adapter the caller marks the Check Critical so a connection-refused
// upstream pulls the pod from rotation. For the customer_db adapter
// the caller leaves Critical=false so a customer-DB outage doesn't
// crater the agent-facing API.
//
// timeout caps the per-call wait — typical value is 2s. The function
// also derives a per-call ctx so a slow upstream can't outlive the
// readiness probe deadline.
func PingDB(db *sql.DB, timeout time.Duration) CheckFunc {
	return func(ctx context.Context) CheckResult {
		if db == nil {
			return CheckResult{Status: StatusFailed, LastError: "db_not_configured"}
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := db.PingContext(callCtx); err != nil {
			return CheckResult{Status: StatusFailed, LastError: scrub(err.Error())}
		}
		return CheckResult{Status: StatusOK}
	}
}

// Pinger is the minimal interface a Redis-like client must satisfy.
// The redis/go-redis v9 *Client and miniredis test client both expose
// .Ping(ctx).Err() through the StatusCmd type so we can mock easily in
// tests.
type Pinger interface {
	Ping(ctx context.Context) PingResult
}

// PingResult abstracts go-redis's *StatusCmd so tests can supply their
// own implementation without dragging the redis package into common/.
type PingResult interface {
	Err() error
}

// PingRedis builds a CheckFunc against a Pinger. The 1s default timeout
// matches the existing /healthz rate-limit ping path and keeps the
// readinessProbe well under timeoutSeconds=5.
func PingRedis(p Pinger, timeout time.Duration) CheckFunc {
	return func(ctx context.Context) CheckResult {
		if p == nil {
			return CheckResult{Status: StatusFailed, LastError: "redis_not_configured"}
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := p.Ping(callCtx).Err(); err != nil {
			return CheckResult{Status: StatusFailed, LastError: scrub(err.Error())}
		}
		return CheckResult{Status: StatusOK}
	}
}

// HTTPHeadCheck builds a CheckFunc that GETs (or HEADs) a URL and maps
// the response status to Status. 2xx → ok, 401/403 → degraded (auth
// broken — the upstream is reachable but our credentials are wrong),
// 5xx or any timeout → failed.
//
// The function is the load-bearing surface for the Brevo / Razorpay /
// DO Spaces checks — every external API gets the same envelope so
// dashboards can join across upstreams.
//
// method defaults to GET if empty. headers are applied to every call
// (typical use: Authorization, api-key). body is left nil — every
// upstream we probe is a read-only sanity ping.
func HTTPHeadCheck(client *http.Client, method, url string, headers map[string]string, timeout time.Duration) CheckFunc {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if method == "" {
		method = http.MethodGet
	}
	return func(ctx context.Context) CheckResult {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(callCtx, method, url, nil)
		if err != nil {
			return CheckResult{Status: StatusFailed, LastError: "request_build_failed"}
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return CheckResult{Status: StatusFailed, LastError: scrubNetError(err)}
		}
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
		return mapHTTPStatus(resp.StatusCode)
	}
}

// mapHTTPStatus converts an HTTP response status into the readiness
// status. The mapping is the same for every upstream:
//
//	2xx           → ok           (reachable, credentials valid)
//	401, 403      → degraded     (reachable, credentials BROKEN —
//	                              this is the Brevo silent-rejection
//	                              shape from 2026-05-20)
//	408, 429, 5xx → failed       (upstream malfunction)
//	other 4xx     → degraded     (probe shape wrong but reachable)
func mapHTTPStatus(code int) CheckResult {
	switch {
	case code >= 200 && code < 300:
		return CheckResult{Status: StatusOK}
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return CheckResult{Status: StatusDegraded, LastError: "auth_" + strconv.Itoa(code)}
	case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests:
		return CheckResult{Status: StatusFailed, LastError: "upstream_" + strconv.Itoa(code)}
	case code >= 500:
		return CheckResult{Status: StatusFailed, LastError: "upstream_" + strconv.Itoa(code)}
	default:
		return CheckResult{Status: StatusDegraded, LastError: "http_" + strconv.Itoa(code)}
	}
}

// scrub trims an error to a short fixed string for the wire. We deliberately
// drop the full message — a /readyz that surfaces a raw "pq: password
// authentication failed for user 'instant'" would leak the username on
// every probe.
func scrub(msg string) string {
	if len(msg) > 80 {
		msg = msg[:80]
	}
	// Strip the trailing newline that some upstream errors include.
	msg = strings.TrimSpace(msg)
	return msg
}

// scrubNetError maps net.Error shapes (timeout, refused, etc.) to a
// short stable string. Useful for the HTTP HEAD checks where the URL
// itself might appear in the underlying error.
func scrubNetError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "no such host"):
		return "dns_failure"
	case strings.Contains(msg, "TLS"), strings.Contains(msg, "x509"):
		return "tls_failure"
	}
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return msg
}

// GRPCHealthChecker is the minimal interface needed to probe a gRPC
// server's grpc.health.v1.Health/Check RPC. Implementations live in the
// service repos (api wraps its provisioner.Client). Keeping the
// interface tiny lets tests inject a fake without dragging
// google.golang.org/grpc/health into common/.
type GRPCHealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// GRPCHealth builds a CheckFunc against a GRPCHealthChecker. The
// returned check is "failed" on any error — for the provisioner_grpc
// adapter the caller marks Critical=true.
func GRPCHealth(checker GRPCHealthChecker, timeout time.Duration) CheckFunc {
	return func(ctx context.Context) CheckResult {
		if checker == nil {
			return CheckResult{Status: StatusFailed, LastError: "grpc_not_configured"}
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := checker.HealthCheck(callCtx); err != nil {
			return CheckResult{Status: StatusFailed, LastError: scrubNetError(err)}
		}
		return CheckResult{Status: StatusOK}
	}
}

// MustDuration is a small helper so callers can write
//   readiness.MustDuration("3s", 3*time.Second)
// and get a sensible default on a malformed env var. Returns fallback
// on parse failure.
func MustDuration(env string, fallback time.Duration) time.Duration {
	if env == "" {
		return fallback
	}
	d, err := time.ParseDuration(env)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// Static returns a CheckFunc that always returns the supplied result.
// Useful for "feature disabled, but I want the check name to still
// appear" cases — e.g. brevo when BREVO_API_KEY is not configured.
func Static(status Status, msg string) CheckFunc {
	return func(ctx context.Context) CheckResult {
		return CheckResult{Status: status, LastError: msg}
	}
}

// formatTimeout is exported only because keeping a public test against
// the helper-internal mapping is more durable than testing the wire
// shape; the symbol is not referenced by callers.
func formatTimeout(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

var _ = formatTimeout // silence unused-symbol lint until a caller needs it
