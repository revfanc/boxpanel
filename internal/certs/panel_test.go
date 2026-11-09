package certs

import (
	"crypto/tls"
	"crypto/x509"
	"net/netip"
	"testing"
	"time"
)

func TestPanelCertificate(t *testing.T) {
	for _, address := range []string{"203.0.113.7", "2001:db8::7", "127.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			now := time.Now()
			certificate, key, err := Panel(netip.MustParseAddr(address), now)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(certificate, key)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			pool := x509.NewCertPool()
			pool.AddCert(leaf)
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: address, Roots: pool, CurrentTime: now}); err != nil {
				t.Fatal("SAN or self-signature invalid", err)
			}
			if leaf.VerifyHostname("192.0.2.99") == nil || leaf.IsCA || leaf.SerialNumber.Sign() <= 0 {
				t.Fatal("invalid leaf certificate constraints")
			}
		})
	}
	if _, _, err := Panel(netip.Addr{}, time.Now()); err == nil {
		t.Fatal("accepted invalid SAN")
	}
}
