// Package static registers the in-memory ("static") feature-flag backend with
// the parent featureflag registry.
//
// The static backend is the DEFAULT and the fail-closed fallback: it reads a
// flag definition map (Config.StaticFlags) and/or a flagd-format JSON file
// (Config.StaticFilePath), evaluates entirely in-process with NO network, and
// returns the caller default for any flag it does not know. Tests and CI use it
// so no running flagd is required.
//
// The concrete implementation lives in the parent package (so Factory's
// degrade path can construct it without importing any subpackage); this package
// is the thin side-effect registration shim, mirroring the
// queueprovider/legacyopen and storageprovider/dospaces convention. Import it
// for its side effect to make "static" resolvable through the registry:
//
//	import _ "instant.dev/common/featureflag/static"
package static

import "instant.dev/common/featureflag"

func init() {
	featureflag.Register(featureflag.BackendStatic, featureflag.NewStaticBuilder())
}
