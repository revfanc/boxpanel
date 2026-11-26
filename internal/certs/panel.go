// Package certs creates self-signed TLS certificates for the panel and Hysteria2.
package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"
)

const PanelFallbackKeyFile = "panel-fallback.key"

// LegacyCommonName was the subject of panel certificates before v0.5.1. It let
// certificate search engines list every panel, so it is no longer issued and
// stored leaves carrying it are reissued with the same key.
const LegacyCommonName = "boxpanel"

func Panel(ip netip.Addr, now time.Time) (certificate, privateKey []byte, err error) {
	if !ip.IsValid() || ip.Zone() != "" {
		return nil, nil, fmt.Errorf("generate panel certificate: provide a valid IP address without a zone")
	}
	// The subject names only the host the client already dialled.
	return selfSigned(ip.String(), nil, []net.IP{net.IP(ip.AsSlice())}, now)
}

// SignPanelHost issues a self-signed leaf using the existing fallback key.
// The caller validates the host.
func SignPanelHost(name string, key crypto.Signer, now time.Time) ([]byte, error) {
	if ip, err := netip.ParseAddr(name); err == nil {
		return signSelfSigned(ip.String(), nil, []net.IP{net.IP(ip.AsSlice())}, key, now)
	}
	return signSelfSigned(name, []string{name}, nil, key, now)
}

func GenerateTLSKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate TLS key: %w", err)
	}
	return key, nil
}

func EncodeTLSKey(key crypto.Signer) ([]byte, error) {
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode TLS key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), nil
}

func ParsePanelFallbackKey(data []byte) (crypto.Signer, string, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, "", fmt.Errorf("invalid panel fallback key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("parse panel fallback key: %w", err)
	}
	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, "", fmt.Errorf("panel fallback key cannot sign certificates")
	}
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, "", fmt.Errorf("encode panel fallback public key: %w", err)
	}
	return key, SPKIPin(spki), nil
}

func SPKIPin(spki []byte) string {
	hash := sha256.Sum256(spki)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func selfSigned(name string, dnsNames []string, ips []net.IP, now time.Time) (certificate, privateKey []byte, err error) {
	key, err := GenerateTLSKey()
	if err != nil {
		return nil, nil, err
	}
	certificate, err = signSelfSigned(name, dnsNames, ips, key, now)
	if err != nil {
		return nil, nil, err
	}
	privateKey, err = EncodeTLSKey(key)
	return certificate, privateKey, err
}

func signSelfSigned(name string, dnsNames []string, ips []net.IP, key crypto.Signer, now time.Time) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	serial.Add(serial, big.NewInt(1))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, DNSNames: dnsNames, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("create self-signed certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
