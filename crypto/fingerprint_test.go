package crypto_test

import (
	"net"
	"testing"

	"instant.dev/common/crypto"
)

func TestFingerprint_IPv4(t *testing.T) {
	ip := net.ParseIP("198.51.100.42")
	fp := crypto.Fingerprint(ip, 12345)
	if len(fp) != 32 {
		t.Errorf("expected 32-char hex, got %d", len(fp))
	}

	// Same /24 subnet → same fingerprint
	other := net.ParseIP("198.51.100.7")
	fp2 := crypto.Fingerprint(other, 12345)
	if fp != fp2 {
		t.Errorf("same /24 should yield identical fingerprint, got %q vs %q", fp, fp2)
	}

	// Different ASN → different fingerprint
	fp3 := crypto.Fingerprint(ip, 99999)
	if fp == fp3 {
		t.Errorf("different ASN should differ, both %q", fp)
	}
}

func TestFingerprint_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	fp := crypto.Fingerprint(ip, 100)
	if len(fp) != 32 {
		t.Errorf("expected 32-char hex, got %d", len(fp))
	}
	other := net.ParseIP("2001:db8::ff")
	fp2 := crypto.Fingerprint(other, 100)
	if fp != fp2 {
		t.Errorf("same /48 IPv6 should yield identical fingerprint")
	}
}

func TestParseIP(t *testing.T) {
	if ip := crypto.ParseIP("10.0.0.1"); ip == nil {
		t.Error("ParseIP returned nil for valid IP")
	}
	if ip := crypto.ParseIP("not-an-ip"); ip != nil {
		t.Errorf("ParseIP returned non-nil for garbage: %v", ip)
	}
}

func TestFingerprintIP(t *testing.T) {
	fp, err := crypto.FingerprintIP("198.51.100.42", "AS12345")
	if err != nil {
		t.Fatalf("FingerprintIP: %v", err)
	}
	if len(fp) != 32 {
		t.Errorf("fp length = %d", len(fp))
	}

	// lowercase "as" prefix should also be stripped
	fp2, err := crypto.FingerprintIP("198.51.100.42", "as12345")
	if err != nil {
		t.Fatalf("FingerprintIP lowercase: %v", err)
	}
	if fp != fp2 {
		t.Errorf("AS vs as: should be equal, %q vs %q", fp, fp2)
	}

	// Empty ASN works too
	fp3, err := crypto.FingerprintIP("198.51.100.42", "")
	if err != nil {
		t.Fatalf("FingerprintIP empty asn: %v", err)
	}
	// fp3 may differ from fp because ASN differs; just check it's well-formed.
	if len(fp3) != 32 {
		t.Errorf("fp3 length = %d", len(fp3))
	}

	// Invalid IP -> error
	if _, err := crypto.FingerprintIP("garbage", ""); err == nil {
		t.Error("expected error for invalid IP")
	}

	// Plain number ASN (no prefix)
	fp4, err := crypto.FingerprintIP("10.0.0.1", "12345")
	if err != nil {
		t.Fatalf("plain asn: %v", err)
	}
	if len(fp4) != 32 {
		t.Errorf("fp4 length = %d", len(fp4))
	}
}
