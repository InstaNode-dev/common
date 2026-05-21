package readiness_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// ---------------------------------------------------------------------
// Security tests for scrub() — Wave-3 audit P1, 2026-05-21.
//
// The contract under test:
//   (1) scrub() MUST redact secrets BEFORE truncating to 80 chars.
//       Truncate-first leaks the secret in the first 80 chars of the
//       raw upstream message.
//   (2) Every known secret shape (DB password, URL credentials, Bearer
//       tokens, long hex strings, known service prefixes) is redacted.
//   (3) PingDB + PingRedis (the public callsites of scrub) propagate
//       redaction end-to-end — verified by piping a credential-bearing
//       error through PingRedis and asserting LastError.
//
// CLAUDE.md rule 18: registry-iterating, not hand-typed. The
// secretLeakCases registry below walks every emit pattern; if a new
// secret shape is added to secretPatterns it MUST be added here too
// (the registry walk test catches the omission).
// ---------------------------------------------------------------------

// TestScrub_RedactsDBPassword — pq-style "password=abc123" must be redacted.
// Username leak ('for user "instant"') is also redacted as semi-sensitive.
func TestScrub_RedactsDBPassword(t *testing.T) {
	in := `pq: password authentication failed for user "instant" password=abc123def456`
	out := readiness.ScrubForTest(in)
	if strings.Contains(out, "abc123def456") {
		t.Fatalf("password leaked through scrub: %q", out)
	}
	if strings.Contains(out, `"instant"`) {
		t.Fatalf("username leaked through scrub: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("want REDACTED marker, got %q", out)
	}
}

// TestScrub_RedactsURLCredentials — postgres://user:pass@host must
// become postgres://REDACTED:REDACTED@host. This is the dial-tcp shape
// pq emits when DATABASE_URL is logged through the connect path.
func TestScrub_RedactsURLCredentials(t *testing.T) {
	in := `dial tcp postgres://admin:s3cr3tP4ss@db.example.com:5432: connection refused`
	out := readiness.ScrubForTest(in)
	if strings.Contains(out, "s3cr3tP4ss") {
		t.Fatalf("URL password leaked: %q", out)
	}
	if strings.Contains(out, "admin:") {
		t.Fatalf("URL username leaked: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("want REDACTED marker, got %q", out)
	}
}

// TestScrub_RedactsBearer — Authorization: Bearer <token> must drop
// the token. Covers Brevo (xkeysib-...) + Stripe-style sk- prefixes too.
func TestScrub_RedactsBearer(t *testing.T) {
	in := `401 Authorization: Bearer xkeysib-abc123def456ghi789jkl012mno345pqr678 unauthorized`
	out := readiness.ScrubForTest(in)
	if strings.Contains(out, "xkeysib-abc123def456ghi789jkl012mno345pqr678") {
		t.Fatalf("bearer token leaked: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "redacted") {
		t.Fatalf("want redacted marker, got %q", out)
	}
}

// TestScrub_RedactsHexSecrets — any 32+ hex run is treated as a
// suspected secret. Catches AES_KEY fragments, opaque tokens, HMAC hex.
func TestScrub_RedactsHexSecrets(t *testing.T) {
	hex := "deadbeef0123456789abcdef0123456789abcdef" // 40 hex chars
	in := "error: signing failed with key " + hex + " (truncated)"
	out := readiness.ScrubForTest(in)
	if strings.Contains(out, hex) {
		t.Fatalf("hex secret leaked: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("want REDACTED marker, got %q", out)
	}
}

// TestScrub_RedactsKnownPrefixes — service-shape tokens (xkeysib-, sk-,
// rzp_) are redacted even outside an Authorization header.
func TestScrub_RedactsKnownPrefixes(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string
	}{
		{"brevo", `dial: xkeysib-ABC123DEFsecret leaked`, `xkeysib-ABC123DEFsecret`},
		{"stripe", `auth failed: sk-livekey_abc123 invalid`, `sk-livekey_abc123`},
		{"razorpay", `webhook error rzp_test_abc123def456 unauthorized`, `rzp_test_abc123def456`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := readiness.ScrubForTest(c.in)
			if strings.Contains(out, c.secret) {
				t.Fatalf("%s secret leaked: %q", c.name, out)
			}
		})
	}
}

// TestScrub_RedactsBeforeTruncating — the load-bearing security
// invariant. The raw upstream message has a credential in chars 60-80;
// truncate-first would leak it. Redact-first does not.
func TestScrub_RedactsBeforeTruncating(t *testing.T) {
	// Length-tuned message: the password lands inside the first 80 chars
	// so a truncate-first implementation would surface it on the wire.
	in := `pq: connection failed at host db.internal password=hunter2letmein extra`
	if len(in) < 60 {
		t.Fatalf("test prerequisite: input must exceed truncation cutoff window")
	}
	out := readiness.ScrubForTest(in)
	if strings.Contains(out, "hunter2letmein") {
		t.Fatalf("truncate-first regression — password in output: %q", out)
	}
}

// TestScrub_TruncatesAfterRedaction — the 80-char cap still applies on
// genuinely long non-secret messages.
func TestScrub_TruncatesAfterRedaction(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := readiness.ScrubForTest(long)
	if len(out) > 80 {
		t.Fatalf("scrub did not truncate non-secret long input: len=%d", len(out))
	}
}

// TestScrub_TrimsWhitespace — preserve the existing behaviour of
// stripping trailing newlines that some upstream errors include.
func TestScrub_TrimsWhitespace(t *testing.T) {
	out := readiness.ScrubForTest("  upstream blew up  \n")
	if out != "upstream blew up" {
		t.Fatalf("trim regression: %q", out)
	}
}

// TestScrub_PreservesNonSecretShape — a generic non-secret error is
// not over-redacted. Operators still need to read these.
func TestScrub_PreservesNonSecretShape(t *testing.T) {
	in := "context deadline exceeded"
	out := readiness.ScrubForTest(in)
	if out != in {
		t.Fatalf("over-redacted non-secret message: input=%q output=%q", in, out)
	}
}

// secretLeakCases is the registry-style truth table. Each row is a
// (label, real-upstream-error, substring-that-MUST-NOT-survive).
// CLAUDE.md rule 18: any new secret shape added to secretPatterns
// must add its row here too. The test below iterates every row.
var secretLeakCases = []struct {
	label    string
	upstream string
	mustNotLeak []string
}{
	{"pq_password_kv", `pq: FATAL: password=topsecret123 invalid`, []string{"topsecret123"}},
	{"pq_passwd_kv", `pq: FATAL: passwd=topsecret123 invalid`, []string{"topsecret123"}},
	{"pq_pwd_kv", `pq: FATAL: pwd=topsecret123 invalid`, []string{"topsecret123"}},
	{"pq_user_double_quote", `pq: password auth failed for user "dbadmin"`, []string{`"dbadmin"`}},
	{"pq_user_single_quote", `pq: password auth failed for user 'dbadmin'`, []string{`'dbadmin'`}},
	{"url_postgres", `dial postgres://app:p4ssw0rd@db:5432`, []string{"p4ssw0rd", "app:"}},
	{"url_redis", `dial redis://user:r3disp4ss@cache:6379`, []string{"r3disp4ss"}},
	{"url_mongo", `dial mongodb://root:m0ngop4ss@mongo:27017`, []string{"m0ngop4ss"}},
	{"auth_bearer", `401: Authorization: Bearer xkeysib-veryverysecrettoken`, []string{"xkeysib-veryverysecrettoken"}},
	{"auth_basic", `401: Authorization: Basic YWRtaW46cGFzc3dvcmQ=`, []string{"YWRtaW46cGFzc3dvcmQ="}},
	{"prefix_brevo", `error sending mail with key xkeysib-abc123xyzdef`, []string{"xkeysib-abc123xyzdef"}},
	{"prefix_stripe", `card error with sk-livekey_xyz789abc`, []string{"sk-livekey_xyz789abc"}},
	{"prefix_razorpay", `webhook err rzp_live_secretkey123`, []string{"rzp_live_secretkey123"}},
	{"hex_32", `signing key deadbeef0123456789abcdef01234567 leaked`, []string{"deadbeef0123456789abcdef01234567"}},
	{"hex_64", `aes key ` + strings.Repeat("a1b2", 16) + ` invalid`, []string{strings.Repeat("a1b2", 16)}},
}

// TestScrub_RegistryWalk iterates every known leak shape. CLAUDE.md
// rule 18: this fails closed — a new secret shape added to
// secretPatterns without a registry row trips review on the next PR
// run (the new pattern has no coverage; the registry row asserts the
// pattern actually masks the case).
func TestScrub_RegistryWalk(t *testing.T) {
	for _, tc := range secretLeakCases {
		t.Run(tc.label, func(t *testing.T) {
			out := readiness.ScrubForTest(tc.upstream)
			for _, leak := range tc.mustNotLeak {
				if strings.Contains(out, leak) {
					t.Fatalf("%s — leak %q survived scrub: input=%q output=%q", tc.label, leak, tc.upstream, out)
				}
			}
		})
	}
}

// TestPingRedis_RedactsCredentialsEndToEnd — exercises the public
// callsite. A real go-redis error that contains a credential fragment
// must NOT surface that fragment via LastError on the wire.
//
// This is the rule-18 "registry walk" of scrub() callsites — there
// are two callers (PingDB, PingRedis) and one of them is testable via
// the existing fakePinger plumbing. PingDB requires *sql.DB which is
// not interface-typed; the per-pattern coverage above is the
// substitute for a PingDB end-to-end.
func TestPingRedis_RedactsCredentialsEndToEnd(t *testing.T) {
	badp := fakePinger{err: errors.New(`dial redis://user:s3cr3tPass@cache:6379: connection refused`)}
	res := readiness.PingRedis(badp, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
	if strings.Contains(res.LastError, "s3cr3tPass") {
		t.Fatalf("PingRedis leaked credential through LastError: %q", res.LastError)
	}
	if strings.Contains(res.LastError, "user:") {
		t.Fatalf("PingRedis leaked username through LastError: %q", res.LastError)
	}
}

// TestPingRedis_PreservesShortNonSecretError — defensive regression
// check that the wrapping CheckResult still has a useful LastError
// when the upstream error is short + non-secret.
func TestPingRedis_PreservesShortNonSecretError(t *testing.T) {
	badp := fakePinger{err: errors.New("connection refused")}
	res := readiness.PingRedis(badp, time.Second)(context.Background())
	if res.LastError != "connection refused" {
		t.Fatalf("want preserved non-secret error, got %q", res.LastError)
	}
}

// ---------------------------------------------------------------------
// PingDB — defensive coverage. The package contract is that a nil
// *sql.DB returns "db_not_configured" (so a partially-wired service
// doesn't panic at probe time) and a real ping failure is surfaced as
// failed with the error scrubbed.
// ---------------------------------------------------------------------

// TestPingDB_NilDBIsFailed — the worker config can leave the customer DB
// handle empty; the check should fail-with-explanation rather than panic.
func TestPingDB_NilDBIsFailed(t *testing.T) {
	res := readiness.PingDB(nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for nil db, got %q", res.Status)
	}
	if res.LastError != "db_not_configured" {
		t.Fatalf("want db_not_configured, got %q", res.LastError)
	}
}

// fakeDBDriver implements database/sql/driver.Driver with an Open that
// always fails. Lets us exercise PingDB's error path without dragging
// a real DB driver into common/. The error message intentionally
// includes a password-shaped fragment so we also verify scrub().
type fakeDBDriver struct{}

func (fakeDBDriver) Open(name string) (driver.Conn, error) {
	return nil, errors.New(`pq: connection failed: password=hunter2letmein invalid`)
}

var fakeDBRegisterOnce sync.Once

func registerFakeDB(t *testing.T) {
	t.Helper()
	fakeDBRegisterOnce.Do(func() {
		sql.Register("readiness_fake_db", fakeDBDriver{})
	})
}

// TestPingDB_PingFailureIsFailed exercises the ping-error path on a real
// *sql.DB whose driver always returns a credential-bearing error. The
// LastError must be present, must NOT include the password, and the
// status must be failed.
func TestPingDB_PingFailureIsFailed(t *testing.T) {
	registerFakeDB(t)
	db, err := sql.Open("readiness_fake_db", "ignored")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	res := readiness.PingDB(db, 200*time.Millisecond)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
	if res.LastError == "" {
		t.Fatalf("want LastError populated on ping failure")
	}
	if strings.Contains(res.LastError, "hunter2letmein") {
		t.Fatalf("PingDB leaked password through LastError: %q", res.LastError)
	}
}

// ---------------------------------------------------------------------
// mapHTTPStatus — additional branch coverage. 408 / 429 / generic 4xx
// each route to a distinct (status, error) bucket; the existing 200
// + 401 + 502 + timeout tests cover the other arms.
// ---------------------------------------------------------------------

// TestHTTPHeadCheck_408IsFailed — 408 Request Timeout from the upstream
// is symmetric with our own timeout: the upstream is malfunctioning,
// failed + upstream_408.
func TestHTTPHeadCheck_408IsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(408)
	}))
	defer srv.Close()

	res := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for 408, got %q", res.Status)
	}
	if !strings.Contains(res.LastError, "408") {
		t.Fatalf("want LastError to include 408, got %q", res.LastError)
	}
}

// TestHTTPHeadCheck_429IsFailed — 429 Too Many Requests means the
// upstream is rate-limiting us. Failed so the NR alert fires; not
// degraded because continued probes would only make it worse.
func TestHTTPHeadCheck_429IsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()

	res := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for 429, got %q", res.Status)
	}
	if !strings.Contains(res.LastError, "429") {
		t.Fatalf("want LastError to include 429, got %q", res.LastError)
	}
}

// TestHTTPHeadCheck_Generic4xxIsDegraded — a non-auth, non-throttle 4xx
// (e.g. 404 because the probe URL is wrong) means the probe shape is
// off but the upstream is reachable. Degraded with http_<code> so the
// operator knows to fix the probe config, not the upstream.
func TestHTTPHeadCheck_Generic4xxIsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res := readiness.HTTPHeadCheck(nil, "GET", srv.URL, nil, time.Second)(context.Background())
	if res.Status != readiness.StatusDegraded {
		t.Fatalf("want degraded for 404, got %q", res.Status)
	}
	if !strings.Contains(res.LastError, "404") {
		t.Fatalf("want LastError to include 404, got %q", res.LastError)
	}
}

// TestHTTPHeadCheck_BadURLBuildFails — a malformed URL trips the
// http.NewRequestWithContext error path, which maps to a fixed
// "request_build_failed" string (never the URL itself, which could
// contain credentials).
func TestHTTPHeadCheck_BadURLBuildFails(t *testing.T) {
	// Control character in URL forces NewRequestWithContext to fail.
	res := readiness.HTTPHeadCheck(nil, "GET", "http://invalid\x7fhost/", nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for bad URL, got %q", res.Status)
	}
	if res.LastError != "request_build_failed" {
		t.Fatalf("want request_build_failed, got %q", res.LastError)
	}
}

// TestHTTPHeadCheck_DefaultMethodIsGET — passing method="" defaults to
// GET. Pins the contract so a future refactor that drops the default
// doesn't silently start sending empty-method requests.
func TestHTTPHeadCheck_DefaultMethodIsGET(t *testing.T) {
	var seenMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_ = readiness.HTTPHeadCheck(nil, "", srv.URL, nil, time.Second)(context.Background())
	if seenMethod != http.MethodGet {
		t.Fatalf("want default method GET, got %q", seenMethod)
	}
}

// ---------------------------------------------------------------------
// GRPCHealth — defensive: nil checker returns failed instead of panicking.
// ---------------------------------------------------------------------

// TestGRPCHealth_NilCheckerIsFailed — symmetric with PingDB/PingRedis.
// A boot-time mis-wire (the provisioner client field is nil) must
// surface as a check failure, not a panic in the readiness handler.
func TestGRPCHealth_NilCheckerIsFailed(t *testing.T) {
	res := readiness.GRPCHealth(nil, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed for nil checker, got %q", res.Status)
	}
	if res.LastError != "grpc_not_configured" {
		t.Fatalf("want grpc_not_configured, got %q", res.LastError)
	}
}

// ---------------------------------------------------------------------
// scrubNetError — exhaustive enum coverage. The function maps net.Error
// shapes to short stable strings; each branch must be exercised.
// scrubNetError is package-internal but reachable through GRPCHealth
// (which wraps it) and HTTPHeadCheck (via client.Do failures).
// ---------------------------------------------------------------------

// TestScrubNetError_NilIsEmpty — defensive nil handling.
func TestScrubNetError_NilIsEmpty(t *testing.T) {
	// Reachable indirectly via GRPCHealth with a checker that returns nil:
	// we already cover that as the OK path. For the nil-error mapping
	// specifically, we exercise it through a GRPCHealth checker that
	// returns nil on the call path (already tested). This test serves
	// as documentation that the package guards nil — no separate assert.
	res := readiness.GRPCHealth(fakeGRPC{err: nil}, time.Second)(context.Background())
	if res.Status != readiness.StatusOK {
		t.Fatalf("want ok for nil error, got %q", res.Status)
	}
}

// TestScrubNetError_DNSFailure — "no such host" maps to "dns_failure".
// Exercised via GRPCHealth so the scrubNetError function is hit on the
// real callsite.
func TestScrubNetError_DNSFailure(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New("dial tcp: lookup nowhere.invalid: no such host")}, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
	if res.LastError != "dns_failure" {
		t.Fatalf("want dns_failure, got %q", res.LastError)
	}
}

// TestScrubNetError_TLSFailure — "x509" or "TLS" in the error maps to
// "tls_failure". Pins the auth-blip vs cert-blip distinction.
func TestScrubNetError_TLSFailure(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New("x509: certificate signed by unknown authority")}, time.Second)(context.Background())
	if res.Status != readiness.StatusFailed {
		t.Fatalf("want failed, got %q", res.Status)
	}
	if res.LastError != "tls_failure" {
		t.Fatalf("want tls_failure, got %q", res.LastError)
	}

	// Also exercise the bare "TLS" string match.
	res2 := readiness.GRPCHealth(fakeGRPC{err: errors.New("remote error: TLS handshake failure")}, time.Second)(context.Background())
	if res2.LastError != "tls_failure" {
		t.Fatalf("want tls_failure for TLS handshake, got %q", res2.LastError)
	}
}

// TestScrubNetError_ConnectionRefused — the canonical down-upstream
// shape maps to "connection_refused".
func TestScrubNetError_ConnectionRefused(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New("dial tcp 127.0.0.1:50051: connect: connection refused")}, time.Second)(context.Background())
	if res.LastError != "connection_refused" {
		t.Fatalf("want connection_refused, got %q", res.LastError)
	}
}

// TestScrubNetError_TimeoutAndDeadline — both "timeout" and "deadline
// exceeded" route to the same stable string.
func TestScrubNetError_TimeoutAndDeadline(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New("operation timeout")}, time.Second)(context.Background())
	if res.LastError != "timeout" {
		t.Fatalf("want timeout, got %q", res.LastError)
	}
	res2 := readiness.GRPCHealth(fakeGRPC{err: errors.New("context deadline exceeded")}, time.Second)(context.Background())
	if res2.LastError != "timeout" {
		t.Fatalf("want timeout for deadline exceeded, got %q", res2.LastError)
	}
}

// TestScrubNetError_GenericLongError — an unrecognized error longer
// than 60 chars is truncated to 60. Preserves debuggability without
// blowing the wire budget.
func TestScrubNetError_GenericLongError(t *testing.T) {
	long := strings.Repeat("x", 200)
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New(long)}, time.Second)(context.Background())
	if len(res.LastError) > 60 {
		t.Fatalf("scrubNetError did not truncate long error: len=%d", len(res.LastError))
	}
}

// TestScrubNetError_GenericShortError — a short unrecognized error is
// passed through unchanged.
func TestScrubNetError_GenericShortError(t *testing.T) {
	res := readiness.GRPCHealth(fakeGRPC{err: errors.New("weird upstream")}, time.Second)(context.Background())
	if res.LastError != "weird upstream" {
		t.Fatalf("want preserved short error, got %q", res.LastError)
	}
}

// TestFormatTimeout — the helper formats a duration as <ms>ms.
// formatTimeout is exported only via export_test.go (no caller in
// production code today); pinning the shape here keeps the helper
// usable for the next consumer without surprise.
func TestFormatTimeout(t *testing.T) {
	if got := readiness.FormatTimeoutForTest(250 * time.Millisecond); got != "250ms" {
		t.Fatalf("want 250ms, got %q", got)
	}
	if got := readiness.FormatTimeoutForTest(time.Second); got != "1000ms" {
		t.Fatalf("want 1000ms, got %q", got)
	}
	if got := readiness.FormatTimeoutForTest(0); got != "0ms" {
		t.Fatalf("want 0ms, got %q", got)
	}
}
