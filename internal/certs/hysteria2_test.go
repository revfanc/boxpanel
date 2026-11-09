package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestHysteria2Certificate(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"www.bing.com", "node.example.com", "203.0.113.7", "2001:db8::7"} {
		t.Run(name, func(t *testing.T) {
			material, err := Hysteria2(name, now)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(material.Certificate, material.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			pool := x509.NewCertPool()
			pool.AddCert(leaf)
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool, CurrentTime: now}); err != nil {
				t.Fatal(err)
			}
			if leaf.Subject.CommonName != name || leaf.IsCA || leaf.SerialNumber.Sign() <= 0 ||
				!leaf.NotBefore.Equal(now.Add(-5*time.Minute)) || !leaf.NotAfter.Equal(now.AddDate(1, 0, 0)) {
				t.Fatal("incorrect certificate identity, lifetime or constraints")
			}
			if leaf.VerifyHostname("wrong.example.org") == nil {
				t.Fatal("accepted wrong hostname")
			}
			if leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
				t.Fatal("incorrect certificate usage")
			}
			key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
			if !ok || key.Curve != elliptic.P256() {
				t.Fatal("expected ECDSA P-256")
			}
			digest := sha256.Sum256(leaf.Raw)
			if material.Fingerprint != hex.EncodeToString(digest[:]) {
				t.Fatal("fingerprint must hash leaf DER")
			}
			// PEM whitespace must not change a pin; hashing PEM or SPKI would fail.
			fp, err := Fingerprint(append([]byte("\n"), material.Certificate...))
			if err != nil || fp != material.Fingerprint {
				t.Fatal("fingerprint depends on PEM formatting")
			}
			other, err := Hysteria2(name, now)
			if err != nil || other.Fingerprint == material.Fingerprint {
				t.Fatal("certificate generation did not use fresh randomness")
			}
		})
	}
}

func TestInvalidHysteria2CertificateInputs(t *testing.T) {
	for _, name := range []string{"", "https://example.com", "example.com:443", "*.example.com", "bad name", "-bad.example", "bad..example", strings.Repeat("a", 64) + ".example", "fe80::1%en0"} {
		if _, err := Hysteria2(name, time.Now()); err == nil {
			t.Fatal("accepted invalid server name")
		}
	}
	if _, err := Hysteria2("node.example", time.Time{}); err == nil {
		t.Fatal("accepted zero time")
	}
	for _, data := range [][]byte{nil, []byte("not PEM"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("secret")})} {
		if _, err := Fingerprint(data); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("accepted invalid certificate or leaked contents")
		}
	}
}
