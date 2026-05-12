// Package buildinfo exposes compile-time build metadata for every Go
// binary in instanode.dev (api / worker / provisioner / cli).
//
// The three vars are wired in at link time via the Go linker's
// `-X` flag:
//
//	go build -ldflags "-X instant.dev/common/buildinfo.GitSHA=abc1234 \
//	                   -X instant.dev/common/buildinfo.BuildTime=2026-05-12T16:00:00Z \
//	                   -X instant.dev/common/buildinfo.Version=v3.6.0" ./...
//
// Defaults are sentinel strings (`dev` / `unknown`) so an un-flagged
// `go build` still produces a runnable binary — useful for local
// `make run` and `go test ./...`. CI and the Dockerfiles always pass
// real values via `--build-arg`.
//
// Consumers (slog handlers, /healthz, /api/v1/buildinfo, the worker's
// startup log, NR custom attributes) read these vars directly. The
// package has zero deps so it is safe to import from any other
// package without creating cycles.
package buildinfo

// GitSHA is the short Git SHA of the commit the binary was built from.
// Set at link time via -ldflags. Defaults to "dev" for un-flagged
// local builds.
var GitSHA = "dev"

// BuildTime is the RFC-3339 UTC timestamp the binary was built at.
// Set at link time via -ldflags. Defaults to "unknown" for un-flagged
// local builds.
var BuildTime = "unknown"

// Version is the semver / release tag the binary was built from.
// Set at link time via -ldflags. Defaults to "dev" for un-flagged
// local builds.
var Version = "dev"
