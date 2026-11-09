package certs

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Material is returned in memory. The caller owns secure storage and lifecycle.
type Material struct {
	Certificate []byte
	PrivateKey  []byte
	Fingerprint string // SHA-256 of the leaf DER certificate, lowercase hex (not SPKI).
}

// Hysteria2 creates a one-year ECDSA P-256 server certificate for a DNS name or IP.
// It neither writes files nor changes any running service.
func Hysteria2(serverName string, now time.Time) (Material, error) {
	var dnsNames []string
	var ips []net.IP
	if ip, err := netip.ParseAddr(serverName); err == nil && ip.Zone() == "" {
		ips = []net.IP{net.IP(ip.AsSlice())}
	} else if validDNSName(serverName) {
		dnsNames = []string{serverName}
	} else {
		return Material{}, fmt.Errorf("generate Hysteria2 certificate: provide a DNS name or IP without a port, wildcard or zone")
	}
	if now.IsZero() {
		return Material{}, fmt.Errorf("generate Hysteria2 certificate: provide the current time")
	}
	cert, key, err := selfSigned(serverName, dnsNames, ips, now)
	if err != nil {
		return Material{}, fmt.Errorf("generate Hysteria2 certificate: %w", err)
	}
	fingerprint, err := Fingerprint(cert)
	if err != nil {
		return Material{}, fmt.Errorf("generate Hysteria2 certificate: %w", err)
	}
	return Material{Certificate: cert, PrivateKey: key, Fingerprint: fingerprint}, nil
}

// Fingerprint hashes the first certificate's DER, not its PEM text or public key.
func Fingerprint(certificate []byte) (string, error) {
	block, _ := pem.Decode(certificate)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("fingerprint certificate: provide a PEM certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", fmt.Errorf("fingerprint certificate: invalid certificate; regenerate or restore it")
	}
	digest := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(digest[:]), nil
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
