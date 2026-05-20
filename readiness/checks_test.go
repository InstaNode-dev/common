package readiness_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"instant.dev/common/readiness"
)

// TestHTTPHeadCheck_2xxIsOK — the canonical happy path. Brevo /v3/account
// returns 200 → ok.
func TestHTTPHeadCheck_2xxIsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	fn := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)
	res := fn(context.Background())
	if res.Status != readiness.StatusOK {
		t.Fatalf("want ok, got %q (err=%q)", res.Status, res.LastError)
	}
}

// TestHTTPHeadCheck_401IsDegraded — the Brevo silent-rejection shape.
// The endpoint is reachable but credentials are broken. Degraded, not
// failed — a Brevo auth blip should NOT pull api pods from rotation.
func TestHTTPHeadCheck_401IsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	fn := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)
	res := fn(context.Background())
	if res.Status != readiness.StatusDegraded {
		t.Fatalf("want degraded for 401, got %q", res.Status)
	}
	if res.LastError == "" {
		t.Fatalf("want LastError populated on 401")
	}
}

// TestHTTPHeadCheck_5xxIsFailed — upstream malfunction. Still
// non-critical for Brevo/Razorpay, so overall=degraded, but the per-
// check Status is failed so the NR alert fires.
func TestHTTPHeadCheck_5xxIsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer srv.Close()

	fn := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)
	res := fn(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for 502, got %q", res.Status)
	}
}

// TestHTTPHeadCheck_TimeoutIsFailed — the upstream is hanging. Failed
// + LastError="timeout" so the operator can distinguish a slow upstream
// from a wrong-status upstream in the wire output.
func TestHTTPHeadCheck_TimeoutIsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	fn := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, 30*time.Millisecond)
	res := fn(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed on timeout, got %q (err=%q)", res.Status, res.LastError)
	}
	if res.LastError != "timeout" {
		t.Fatalf("want LastError=timeout, got %q", res.LastError)
	}
}

// TestHTTPHeadCheck_AppliesHeaders — auth headers reach the upstream.
// Without this, a Brevo probe without api-key would always be 401-
// degraded and the dashboard would say "broken auth forever".
func TestHTTPHeadCheck_AppliesHeaders(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("api-key")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	fn := readiness.HTTPHeadCheck(nil, "GET", srv.URL, map[string]string{"api-key": "xkeysib-test"}, time.Second)
	_ = fn(context.Background())
	if seen != "xkeysib-test" {
		t.Fatalf("want header propagated, got %q", seen)
	}
}

// TestPingRedis_OKAndFailure — happy + sad paths against a fake Pinger.
func TestPingRedis_OKAndFailure(t *testing.T) {
	okp := fakePinger{err: nil}
	res := readiness.PingRedis(okp, time.Second)(context.Background())
	if res.Status != readiness.StatusOK {
		t.Fatalf("want ok, got %q", res.Status)
	}

	badp := fakePinger{err: errors.New("connection refused")}
	res = readiness.PingRedis(badp, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
}

// TestPingRedis_NilPingerIsFailed — defensive: the worker config can
// leave Redis empty; the check should fail-with-explanation rather than
// panic.
func TestPingRedis_NilPingerIsFailed(t *testing.T) {
	res := readiness.PingRedis(nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
	if res.LastError != "redis_not_configured" {
		t.Fatalf("want redis_not_configured, got %q", res.LastError)
	}
}

// TestGRPCHealth_OKAndFailure — the provisioner gRPC check.
func TestGRPCHealth_OKAndFailure(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: nil}, time.Second)(context.Background())
	if res.Status != readiness.StatusOK {
		t.Fatalf("want ok, got %q", res.Status)
	}
	res = readiness.GRPCHealth(fakeGRPC{err: errors.New("rpc error: code = Unavailable")}, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
}

// TestStatic — feature-disabled check returns a stable shape.
func TestStatic(t *testing.T) {
	res := readiness.Static(readiness.StatusOK, "")(context.Background())
	if res.Status != readiness.StatusOK {
		t.Fatalf("static ok malformed: %q", res.Status)
	}
}

// TestMustDuration — env parsing fallback.
func TestMustDuration(t *testing.T) {
	if got := readiness.MustDuration("", time.Second); got != time.Second {
		t.Fatalf("empty env should return fallback, got %v", got)
	}
	if got := readiness.MustDuration("garbage", time.Second); got != time.Second {
		t.Fatalf("bad env should return fallback, got %v", got)
	}
	if got := readiness.MustDuration("250ms", time.Second); got != 250*time.Millisecond {
		t.Fatalf("good env should parse, got %v", got)
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) readiness.PingResult { return fakeResult{f.err} }

type fakeResult struct{ err error }

func (f fakeResult) Err() error { return f.err }

type fakeGRPC struct{ err error }

func (f fakeGRPC) HealthCheck(ctx context.Context) error { return f.err }
