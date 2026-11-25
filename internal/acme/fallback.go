package acme

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/filelock"
)

type fallbackKey struct {
	key crypto.Signer
	pem []byte
	pin string
}

func (m *Manager) FallbackPin() (string, error) {
	if m.fallback.pin == "" {
		return "", fmt.Errorf("panel fallback key is unavailable")
	}
	return m.fallback.pin, nil
}

func (m *Manager) FallbackSigner() crypto.Signer { return m.fallback.key }

// FallbackCertificate serves pinned clients connecting directly to the new
// host's IP while a migrated domain still points at the old master.
func (m *Manager) FallbackCertificate(name string) (*tls.Certificate, error) {
	certificate, err := certs.SignPanelHost(name, m.fallback.key, m.opts.Now())
	if err != nil {
		return nil, err
	}
	return parseMaterial(Material{Certificate: certificate, Key: m.fallback.pem}, name, m.opts.Now())
}

func (m *Manager) loadFallback(ctx context.Context, state savedState) (fallbackKey, error) {
	unlock, err := filelock.Acquire(ctx, m.opts.Dir+".lock")
	if err != nil {
		return fallbackKey{}, err
	}
	defer unlock()
	expected, err := db.GetSetting(ctx, m.db, db.PanelFallbackPin)
	hasPin := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fallbackKey{}, err
	}
	name := filepath.Join(m.opts.Dir, certs.PanelFallbackKeyFile)
	info, err := os.Lstat(name)
	var data []byte
	switch {
	case err == nil:
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fallbackKey{}, fmt.Errorf("invalid panel fallback key permissions; restore the original 0600 key")
		}
		data, err = os.ReadFile(name)
		if err != nil {
			return fallbackKey{}, fmt.Errorf("read panel fallback key: %w", err)
		}
	case errors.Is(err, os.ErrNotExist) && !hasPin:
		key := m.legacyFallback(state)
		if key == nil {
			key, err = certs.GenerateTLSKey()
			if err != nil {
				return fallbackKey{}, err
			}
		}
		data, err = certs.EncodeTLSKey(key)
		if err != nil {
			return fallbackKey{}, err
		}
		if err := privateFile(name, data); err != nil {
			return fallbackKey{}, err
		}
	default:
		return fallbackKey{}, fmt.Errorf("load panel fallback key; restore the original key from backup: %w", err)
	}
	key, pin, err := certs.ParsePanelFallbackKey(data)
	if err != nil {
		return fallbackKey{}, fmt.Errorf("restore the original panel fallback key: %w", err)
	}
	if hasPin && expected != pin {
		return fallbackKey{}, fmt.Errorf("panel fallback key changed; restore the original key from backup")
	}
	if !hasPin {
		if err := db.SetSetting(ctx, m.db, db.PanelFallbackPin, pin); err != nil {
			return fallbackKey{}, err
		}
	}
	return fallbackKey{key: key, pem: data, pin: pin}, nil
}

func (m *Manager) legacyFallback(state savedState) crypto.Signer {
	// Prefer the most recent self-signed certificate, including after a switch
	// to ACME, so existing nodes can keep using their pins after the upgrade.
	for _, name := range []string{state.File, state.PreviousFile} {
		if validFile(name) {
			path := filepath.Join(m.opts.Dir, name)
			if key := legacySelfSignedKey(path, path); key != nil {
				return key
			}
		}
	}
	return legacySelfSignedKey(m.opts.CertFile, m.opts.KeyFile)
}

func legacySelfSignedKey(certFile, keyFile string) crypto.Signer {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !selfSigned(leaf) {
		return nil
	}
	key, _ := pair.PrivateKey.(crypto.Signer)
	return key
}

func selfSigned(leaf *x509.Certificate) bool {
	return bytes.Equal(leaf.RawSubject, leaf.RawIssuer) &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}
