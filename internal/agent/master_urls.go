package agent

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"slices"
)

const MaxMasterURLs = 4

func masterURLMessage(urls []string, nonce string) ([]byte, error) {
	if len(urls) == 0 || len(urls) > MaxMasterURLs || decodeBytes(nonce, 16) == nil {
		return nil, fmt.Errorf("invalid master addresses or heartbeat nonce")
	}
	for i, address := range urls {
		if normalized, err := NormalizeMasterURL(address); err != nil || normalized != address || slices.Contains(urls[:i], address) {
			return nil, fmt.Errorf("invalid or duplicate master address")
		}
	}
	data, err := json.Marshal(urls)
	if err != nil {
		return nil, fmt.Errorf("encode master addresses: %w", err)
	}
	return append([]byte("boxpanel/master-urls/v1\n"+nonce+"\n"), data...), nil
}

func SignMasterURLs(reply *HeartbeatReply, urls []string, nonce string, signer crypto.Signer) error {
	if signer == nil {
		return fmt.Errorf("master fallback signing key is unavailable")
	}
	message, err := masterURLMessage(urls, nonce)
	if err != nil {
		return err
	}
	key, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return fmt.Errorf("encode master signing key: %w", err)
	}
	var opts crypto.SignerOpts = crypto.Hash(0)
	switch signer.Public().(type) {
	case *ecdsa.PublicKey, *rsa.PublicKey:
		digest := sha256.Sum256(message)
		message, opts = digest[:], crypto.SHA256
	case ed25519.PublicKey:
	default:
		return fmt.Errorf("unsupported master signing key")
	}
	signature, err := signer.Sign(rand.Reader, message, opts)
	if err != nil {
		return fmt.Errorf("sign master addresses: %w", err)
	}
	reply.MasterURLs = urls
	reply.MasterURLsKey = encoding.EncodeToString(key)
	reply.MasterURLsSignature = encoding.EncodeToString(signature)
	return nil
}

func VerifyMasterURLs(reply HeartbeatReply, pin, nonce string) bool {
	message, err := masterURLMessage(reply.MasterURLs, nonce)
	if err != nil {
		return false
	}
	spki, err := encoding.DecodeString(reply.MasterURLsKey)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(spki)
	if subtle.ConstantTimeCompare(decodeBytes(pin, sha256.Size), digest[:]) != 1 {
		return false
	}
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return false
	}
	signature, err := encoding.DecodeString(reply.MasterURLsSignature)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(message)
	switch pub := key.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(pub, hash[:], signature)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], signature) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(pub, message, signature)
	default:
		return false
	}
}
