// Package agent implements node registration and authenticated state synchronization.
package agent

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	SyncInterval      = 20 * time.Second
	OfflineAfter      = 90 * time.Second
	nonceLifetime     = 5 * time.Minute
	maxClockSkew      = 120
	TrafficPath       = Prefix + "/traffic"
	MaxTrafficBody    = 1 << 20
	MaxTrafficUsers   = 10_000
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
	Version           string `json:"version"`
	SingboxActive     bool   `json:"singbox_active"`
	Uptime            int64  `json:"uptime"`
	AppliedGeneration *int64 `json:"applied_generation,omitempty"`
	SyncError         string `json:"sync_error,omitempty"`
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

// Desired contains credentials and is only sent over the authenticated node channel.
type Desired struct {
	Generation int64            `json:"generation"`
	Inbounds   []DesiredInbound `json:"inbounds"`
	Users      []DesiredUser    `json:"users"`
	Route      DesiredRoute     `json:"route"`
}

type DesiredInbound struct {
	ID       int64           `json:"id"`
	KeyID    string          `json:"key_id"`
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Port     int             `json:"port"`
	Enabled  bool            `json:"enabled"`
	Remark   string          `json:"remark"`
	Options  json.RawMessage `json:"options"`
}

type DesiredUser struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	UUID     string `json:"uuid"`
	Password string `json:"password"`
}

type DesiredRoute struct {
	BlockBittorrent bool `json:"block_bittorrent"`
}

type InboundKeys struct {
	InboundID  int64  `json:"inbound_id"`
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"reality_public_key,omitempty"`
	ShortID    string `json:"short_id,omitempty"`
	CertSHA256 string `json:"hy2_cert_sha256,omitempty"`
	CertPEM    string `json:"hy2_cert_pem,omitempty"`
}

type HeartbeatReply struct {
	DesiredGeneration int64 `json:"desired_generation"`
}

type TrafficReport struct {
	Counters []TrafficCounter `json:"counters"`
}

type TrafficCounter struct {
	UserID int64 `json:"user_id"`
	Epoch  int64 `json:"epoch"`
	Up     int64 `json:"up"`
	Down   int64 `json:"down"`
}

const (
	SyncCheckFailed = "check_failed"
	SyncPortInUse   = "port_in_use"
	SyncRestart     = "restart_failed"
	SyncFirewall    = "firewall_failed"
	SyncTimeout     = "apply_timeout"
	SyncRollback    = "rollback_failed"
	SyncApply       = "apply_failed"
	SyncKeys        = "key_publication_failed"
)

func ValidSyncError(code string) bool {
	switch code {
	case "", SyncCheckFailed, SyncPortInUse, SyncRestart, SyncFirewall, SyncTimeout, SyncRollback, SyncApply, SyncKeys:
		return true
	default:
		return false
	}
}
