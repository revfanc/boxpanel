package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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
			// Certificate search engines must not be able to list panels by subject.
			if leaf.Subject.CommonName != address || leaf.Subject.CommonName == LegacyCommonName {
				t.Fatalf("panel subject %q reveals the product", leaf.Subject.CommonName)
			}
		})
	}
	if _, _, err := Panel(netip.Addr{}, time.Now()); err == nil {
		t.Fatal("accepted invalid SAN")
	}
}

func TestSignPanelHostSubjectIsTheHost(t *testing.T) {
	key, err := GenerateTLSKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"panel.example.com", "203.0.113.7"} {
		certificate, err := SignPanelHost(host, key, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(certificate)
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil || leaf.Subject.CommonName != host {
			t.Fatalf("subject for %s = %q, %v", host, leaf.Subject.CommonName, err)
		}
	}
}
