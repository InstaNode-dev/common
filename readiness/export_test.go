package readiness

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
