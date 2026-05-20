package storageprovider_test

// sanitise_test.go — closes B17-STORAGE-P2-14: the api-side path-traversal
// sanitiser tests covered `..`, `.`, `/`, `//` but missed URL-encoded
// variants, NUL bytes, and Windows-style separators. Defense-in-depth.
//
// Coverage block per CLAUDE.md rule 17:
//   Symptom:        leaked broker-mode token reads/writes outside its prefix
//                   via traversal that the sanitiser misses
//   Enumeration:    grep -rn sanitisePresignKey api/ + grep -rn SanitiseTenantKey common/
//   Sites found:    2 sanitisers — api/internal/handlers/storage_presign.go
//                   (legacy) and common/storageprovider/sanitise.go (canonical)
//   Sites touched:  1 — added canonical helper in common with hardened
//                   coverage; legacy sanitiser kept for now (callsite-level
//                   migration is a separate slice).
//   Coverage test:  TestSanitiseTenantKey_DefenseInDepth iterates 25+ traversal
//                   shapes the audit called out; any future regression
//                   adding a new "lookalike" surfaces here.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"instant.dev/common/storageprovider"
)

// TestSanitiseTenantKey_DefenseInDepth covers the path-traversal shapes the
// B17 audit flagged as missing from the api-side sanitiser:
//   - URL-encoded `..` (%2e%2e, %2E%2E, ..%2f, .%2e, mixed-case)
//   - NUL bytes (\x00, %00) anywhere in the key
//   - Windows-style `\\` separators
//   - Plus regression cases from the legacy sanitiser so we know the new
//     one is a strict super-set.
func TestSanitiseTenantKey_DefenseInDepth(t *testing.T) {
	cases := map[string]string{
		// Empty / trivial passthrough
		"":                 "",
		"file.txt":         "file.txt",
		"a/b/c.txt":        "a/b/c.txt",
		"path/with spaces": "path/with spaces",
		"valid-key.bin":    "valid-key.bin",

		// Legacy sanitiser baseline — verified to still pass
		"/file.txt":     "file.txt",
		"//file.txt":    "file.txt",
		"dir/file.txt":  "dir/file.txt",
		"dir//file.txt": "dir/file.txt",
		"../etc/passwd": "etc/passwd",
		"./file.txt":    "file.txt",
		"a/./b/../c":    "a/b/c",
		"../../escape":  "escape",

		// URL-encoded `..` — the percent-decode runs BEFORE the component
		// split so these collapse exactly like the raw `..` cases.
		"%2e%2e/etc/passwd":   "etc/passwd",
		"%2E%2E/etc/passwd":   "etc/passwd",
		"..%2fetc/passwd":     "etc/passwd",
		"..%2Fetc/passwd":     "etc/passwd",
		"%2e%2e%2fetc/passwd": "etc/passwd",
		"a/%2e/b":             "a/b",
		"%2E/file.txt":        "file.txt",
		// Double encoding (an attacker hoping for a second decode pass) —
		// we only decode ONCE so the result keeps the inner `%2e%2e` as
		// a literal segment, NOT collapsed. Documents the policy: one
		// decode pass, then a strict component split.
		"%252e%252e/etc": "%2e%2e/etc",

		// NUL bytes — raw, then percent-encoded.
		"safe\x00../etc/passwd":  "safe../etc/passwd", // NUL stripped, but `..` is now a literal char-sequence inside a single segment, not a path component (no slash). The segment is `safe..`, then `etc`, `passwd` — wait, let's recheck. After NUL strip: "safe../etc/passwd". Split on `/`: ["safe..", "etc", "passwd"]. None are exactly `..`, so all survive. That's the expected "NUL doesn't help traversal" outcome.
		"file%00.txt":            "file.txt",
		"a/%00../b":              "a/b", // %00 decodes to NUL, NUL stripped, "..", dropped
		"\x00\x00file":           "file",

		// Windows-style backslashes
		"..\\etc\\passwd":      "etc/passwd",
		"a\\b\\c.txt":          "a/b/c.txt",
		"..\\..\\\\\\..\\file": "file",
		"\\file.txt":           "file.txt",

		// Mixed Unicode "dots" — documented as NOT collapsed. A homoglyph
		// like U+2025 (‥) is a regular key segment.
		"‥/file.txt":     "‥/file.txt",
		"．．/escape": "．．/escape",

		// Tricky combos
		"%2e%2e%5cetc%5cpasswd":   "etc/passwd",          // ..\etc\passwd encoded
		"//\\//../a":              "a",
		"./%2e/file": "file",
		// `..` components are DROPPED, not resolved (path.Clean would pop
		// the preceding segment; we don't, because that's strictly more
		// conservative — there is no way for a `..` to climb out of the
		// prefix if it's simply discarded).
		"a/%2e%2e/%2e%2e/c": "a/c",
	}
	for in, want := range cases {
		got := storageprovider.SanitiseTenantKey(in)
		assert.Equal(t, want, got, "SanitiseTenantKey(%q)", in)
	}
}

// TestSanitiseTenantKey_NoLeadingSlash is the invariant the rest of the
// sign pipeline relies on: the returned key never starts with `/` so when
// minio-go joins it onto a bucket name we never produce `//double-slash`.
func TestSanitiseTenantKey_NoLeadingSlash(t *testing.T) {
	for _, in := range []string{
		"////file",
		"\\\\\\file",
		"%2f%2f%2ffile",
		"/%2f/file",
	} {
		got := storageprovider.SanitiseTenantKey(in)
		assert.Falsef(t, strings.HasPrefix(got, "/"),
			"SanitiseTenantKey(%q) = %q must not start with /", in, got)
	}
}

// TestSanitiseTenantKey_NoTraversalComponentSurvives belt-and-suspenders:
// no matter how exotic the input, no component of the output is exactly
// `.` or `..`. (The other guards in this file already imply this, but a
// dedicated invariant makes future regressions trivially diagnosable.)
func TestSanitiseTenantKey_NoTraversalComponentSurvives(t *testing.T) {
	inputs := []string{
		"..",
		"./../..",
		"%2e%2e/%2e%2e",
		"a/%2e/b/%2e%2e/c",
		"\\..\\..\\file",
		"\x00..\x00",
		"%00%2e%2e%00",
	}
	for _, in := range inputs {
		got := storageprovider.SanitiseTenantKey(in)
		for _, part := range strings.Split(got, "/") {
			assert.NotEqualf(t, ".", part, "SanitiseTenantKey(%q) leaked `.` component (got %q)", in, got)
			assert.NotEqualf(t, "..", part, "SanitiseTenantKey(%q) leaked `..` component (got %q)", in, got)
		}
	}
}

// TestSanitiseTenantKey_StripsNUL asserts NUL bytes never survive,
// regardless of where they appear or how they were encoded.
func TestSanitiseTenantKey_StripsNUL(t *testing.T) {
	inputs := []string{
		"safe\x00key",
		"a/\x00/b",
		"%00file",
		"file%00",
		"a/%00%00/b",
		"\x00\x00\x00",
	}
	for _, in := range inputs {
		got := storageprovider.SanitiseTenantKey(in)
		assert.NotContainsf(t, got, "\x00",
			"SanitiseTenantKey(%q) = %q must not contain NUL", in, got)
	}
}
