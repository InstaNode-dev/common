package readiness

import "time"

// ScrubForTest exposes the package-internal scrub() to external tests.
// Lives in *_test.go so it never ships in the binary — there is no way
// for production code to import an _test.go symbol.
//
// Why expose it: the security contract for scrub() is "redact before
// truncate". Tests need to assert on the post-scrub string directly;
// piping fake errors through PingDB / PingRedis works for the two
// callers but obscures the per-pattern assertions and would couple
// every test to a fake sql.DB / Pinger.
func ScrubForTest(msg string) string {
	return scrub(msg)
}

// FormatTimeoutForTest exposes the package-internal formatTimeout()
// helper to external tests. The symbol is intentionally kept private
// in production (no caller references it today) but lives in the
// package so a future timeout-formatting site has a stable helper.
// Tests still need to lock down its shape so the next consumer doesn't
// hand-roll its own.
func FormatTimeoutForTest(d time.Duration) string {
	return formatTimeout(d)
}
