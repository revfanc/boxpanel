// Package reality generates the credentials used by Reality inbounds.
package reality

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// KeyPair contains secrets and must never be logged or returned by preview APIs.
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair uses the same raw URL-safe base64 format as sing-box.
func GenerateKeyPair() (KeyPair, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate Reality key pair: %w", err)
	}
	// sing-box exports the clamped X25519 scalar, not the original random bytes.
	raw := key.Bytes()
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	return KeyPair{
		PrivateKey: base64.RawURLEncoding.EncodeToString(raw),
		PublicKey:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
	}, nil
}

// GenerateShortID returns eight random bytes encoded as 16 lowercase hex digits.
func GenerateShortID() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate Reality short ID: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// PublicKey derives the public half without including the private value in errors.
func PublicKey(privateKey string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(privateKey)
	if err != nil || len(raw) != 32 || len(privateKey) != 43 {
		return "", fmt.Errorf("derive Reality public key: use a 32-byte unpadded base64url private key; regenerate the key pair")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("derive Reality public key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}
