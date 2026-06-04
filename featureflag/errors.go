package featureflag

import "fmt"

// errPanic wraps a recovered panic value as an error so the fail-closed
// wrapper can return it alongside the caller default. The value is never
// re-panicked — a misbehaving provider must NEVER crash a request path; the
// feature simply reads as its default (OFF).
func errPanic(r any) error {
	if err, ok := r.(error); ok {
		return fmt.Errorf("featureflag: provider panicked: %w", err)
	}
	return fmt.Errorf("featureflag: provider panicked: %v", r)
}
