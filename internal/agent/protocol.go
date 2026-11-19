// Package agent implements node registration and authenticated heartbeats.
package agent

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
)

const (
	Prefix            = "/api/agent/v1"
	HeaderServer      = "X-Boxpanel-Server"
	HeaderTimestamp   = "X-Boxpanel-Timestamp"
	HeaderNonce       = "X-Boxpanel-Nonce"
	HeaderSignature   = "X-Boxpanel-Signature"
	HeartbeatInterval = 30 * time.Second
	OfflineAfter      = 90 * time.Second
	nonceLifetime     = 5 * time.Minute
	maxClockSkew      = 120
)

type Registration struct {
	Token     string `json:"token"`
	PublicKey string `json:"public_key"`
	Hostname  string `json:"hostname"`
	PublicIP  string `json:"public_ip"`
	Version   string `json:"version"`
}

type Identity struct {
	ServerID int64 `json:"server_id"`
}

type Heartbeat struct {
	Version       string `json:"version"`
	SingboxActive bool   `json:"singbox_active"`
	Uptime        int64  `json:"uptime"`
}

var encoding = base64.RawURLEncoding.Strict()

func decodeBytes(value string, size int) []byte {
	if len(value) != encoding.EncodedLen(size) {
		return nil
	}
	b, err := encoding.DecodeString(value)
	if err != nil || len(b) != size {
		return nil
	}
	return b
}

func message(method, path, timestamp, nonce string, body []byte) []byte {
	return fmt.Appendf(nil, "%s\n%s\n%s\n%s\n%x", method, path, timestamp, nonce, sha256.Sum256(body))
}

func sign(r *http.Request, id int64, key ed25519.PrivateKey, now time.Time, body []byte) error {
	nonce, err := auth.RandomToken(16)
	if err != nil {
		return fmt.Errorf("generate request nonce: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	r.Header.Set(HeaderServer, strconv.FormatInt(id, 10))
	r.Header.Set(HeaderTimestamp, timestamp)
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderSignature, encoding.EncodeToString(ed25519.Sign(key, message(r.Method, r.URL.EscapedPath(), timestamp, nonce, body))))
	return nil
}
