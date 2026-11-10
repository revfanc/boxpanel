// Package certs creates self-signed TLS certificates for the panel and Hysteria2.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"
)

func Panel(ip netip.Addr, now time.Time) (certificate, privateKey []byte, err error) {
	if !ip.IsValid() || ip.Zone() != "" {
		return nil, nil, fmt.Errorf("generate panel certificate: provide a valid IP address without a zone")
	}
	return selfSigned("boxpanel", nil, []net.IP{net.IP(ip.AsSlice())}, now)
}

// PanelHost creates a fallback for a changed public_host without overwriting
// the original installation certificate. The caller validates DNS names.
func PanelHost(name string, now time.Time) (certificate, privateKey []byte, err error) {
	if ip, err := netip.ParseAddr(name); err == nil {
		return Panel(ip, now)
	}
	return selfSigned("boxpanel", []string{name}, nil, now)
}

func selfSigned(name string, dnsNames []string, ips []net.IP, now time.Time) (certificate, privateKey []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	serial.Add(serial, big.NewInt(1))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, DNSNames: dnsNames, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create self-signed certificate: %w", err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode TLS key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), nil
}
