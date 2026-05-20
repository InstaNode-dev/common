package storageprovider

import (
	"net/url"
	"strings"
)

// SanitiseTenantKey returns the tenant-supplied object key with leading
// slashes stripped, "." and ".." path components dropped, NUL bytes
// removed, Windows-style separators collapsed to forward slashes, and any
// URL-percent-encoded segments decoded BEFORE component evaluation.
//
// Tenant keys flow through this helper before they're handed to S3 / minio-go
// for signing. Anything that survives sanitisation MUST live entirely under
// the resource's prefix — that invariant is the only thing standing between a
// leaked broker-mode token and cross-tenant reads/writes.
//
// Why each layer matters:
//
//   - Raw `..` components — the obvious traversal attempt. Dropped.
//   - URL-encoded `..` (`%2E%2E`, `..%2F`, `%2e%2e/`, mixed-case) —
//     percent-encoded by an attacker hoping the sanitiser runs before url
//     decoding rather than after. We decode FIRST so the post-decode
//     components are what gets evaluated.
//   - NUL bytes — some object stores' policy engines truncate at NUL
//     while their on-disk implementation does not, letting an attacker
//     sign a URL for "tenant-a/safe\x00../tenant-b/secret" that the policy
//     engine reads as "tenant-a/safe" but the storage reads past. Drop NUL.
//   - Windows `\\` separators — minio-go treats `\` as a literal key
//     character, not a path component separator, so `..\..\etc\passwd`
//     would otherwise survive the `/`-splitter. Normalise `\` to `/`
//     pre-split.
//   - Mixed Unicode dots — only ASCII `.` is treated as a path component.
//     A homoglyph like `‥` (U+2025) or `．．` (U+FF0E twice) is treated
//     as a regular key segment because the underlying object store does
//     not collapse them either. Documented here so a future regression
//     adding "lookalike .." rejection doesn't break legitimate keys.
//
// Empty input returns an empty string. The output never starts with `/`
// and never contains `..`, `.`, NUL, or `\\` components.
//
// This helper exists in `common/storageprovider` so api + worker share
// one implementation (CLAUDE.md rule 16: single emitter per contract).
// api/internal/handlers/storage_presign.go's local `sanitisePresignKey`
// is the legacy emitter; callers should migrate to this one and delete it.
func SanitiseTenantKey(in string) string {
	if in == "" {
		return ""
	}
	// Strip NUL bytes anywhere in the string. Do this before percent-decoding
	// so a literal NUL in a percent-encoded segment can't slip past — and
	// before splitting so a NUL doesn't survive as part of a component.
	if strings.ContainsRune(in, 0) {
		in = strings.ReplaceAll(in, "\x00", "")
	}
	// Percent-decode. PathUnescape errors only on malformed escapes (`%ZZ`);
	// in that case leave the input as-is rather than rejecting — the
	// downstream component-split still drops literal `..` / `.` and leading
	// slashes, so a malformed escape can't escape the prefix on its own.
	if decoded, err := url.PathUnescape(in); err == nil {
		in = decoded
	}
	// Strip any NUL bytes that the decode might have surfaced (`%00`).
	if strings.ContainsRune(in, 0) {
		in = strings.ReplaceAll(in, "\x00", "")
	}
	// Normalise Windows-style separators to forward slashes so the split
	// below correctly identifies traversal components.
	in = strings.ReplaceAll(in, "\\", "/")
	// Strip leading slashes so absolute-looking paths don't escape the
	// prefix when re-joined.
	in = strings.TrimLeft(in, "/")

	parts := strings.Split(in, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "/")
}
