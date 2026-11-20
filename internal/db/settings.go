package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// SettingKey binds a persisted key to its Go value type. Use the predefined keys;
// the zero value is invalid. Business validation belongs to the calling service.
type SettingKey[T any] struct{ name string }

var (
	WebPort               = SettingKey[int]{"web_port"}
	WebPath               = SettingKey[string]{"web_path"}
	SubPath               = SettingKey[string]{"sub_path"}
	NodeRole              = SettingKey[string]{"node_role"}
	AgentMaster           = SettingKey[AgentConnection]{"agent_master"}
	AgentLastHeartbeat    = SettingKey[AgentHeartbeatResult]{"agent_last_heartbeat"}
	PublicHost            = SettingKey[string]{"public_host"}
	PanelTLSMode          = SettingKey[string]{"panel_tls_mode"}
	SetupCompleted        = SettingKey[bool]{"setup_completed"}
	SetupPreferences      = SettingKey[SetupChoices]{"setup_preferences"}
	SetupStep             = SettingKey[string]{"setup_step"}
	Rules                 = SettingKey[RulesSettings]{"rules"}
	SingboxVersion        = SettingKey[string]{"singbox_version"}
	BlockBittorrent       = SettingKey[bool]{"block_bittorrent"}
	PanelName             = SettingKey[string]{"panel_name"}
	TrafficBatch          = SettingKey[string]{"traffic_batch"}
	AppliedUsers          = SettingKey[[]int64]{"applied_users"}
	ApplyGeneration       = SettingKey[string]{"apply_generation"}
	ClashAPIPort          = SettingKey[int]{"clash_api_port"}
	ClashAPISecret        = SettingKey[string]{"clash_api_secret"}
	AppliedFirewall       = SettingKey[json.RawMessage]{"applied_firewall"}
	PanelFirewallPorts    = SettingKey[[]int]{"panel_firewall_ports"}
	AlertChannels         = SettingKey[map[string]AlertChannel]{"alert_channels"}
	AlertCore             = SettingKey[CoreAlertState]{"alert_core"}
	PanelCertificate      = SettingKey[json.RawMessage]{"panel_certificate"}
	PanelFallbackPin      = SettingKey[string]{"panel_fallback_pin"}
	Hysteria2Fingerprints = SettingKey[map[string]string]{"hysteria2_fingerprints"}
)

var AgentAppliedGeneration = SettingKey[int64]{"agent_applied_generation"}

type AgentConnection struct {
	MasterURL string `json:"master_url"`
	Pin       string `json:"pin,omitempty"`
	ServerID  int64  `json:"server_id"`
	PublicKey string `json:"public_key"`
}

type AgentHeartbeatResult struct {
	At                   int64  `json:"at"`
	Status               string `json:"status"`
	ClockSkewSeconds     int64  `json:"clock_skew_seconds,omitempty"`
	RepeatedUnauthorized bool   `json:"repeated_unauthorized,omitempty"`
}

// AlertConfig stores local billing settings and per-event switches.
type AlertConfig struct {
	Interface string          `json:"interface"`
	QuotaGB   int64           `json:"quota_gb"`
	Day       int             `json:"billing_day"`
	Mode      string          `json:"billing_mode"`
	Events    map[string]bool `json:"events"`
}

// AlertChannel contains secrets and must never be serialized into API responses.
type AlertChannel struct {
	Enabled    bool   `json:"enabled"`
	URL        string `json:"url,omitempty"`
	Credential string `json:"credential,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
}

type CoreAlertState struct {
	Failures int
	Incident int64
	Alerted  bool
}

// RulesSettings is the JSON storage shape defined in docs/CONFIG.md section 3.
type RulesSettings struct {
	Preset         string      `json:"preset"`
	BlockAds       bool        `json:"block_ads"`
	CustomDirect   []string    `json:"custom_direct"`
	CustomProxy    []string    `json:"custom_proxy"`
	Source         string      `json:"source"`
	Mirror         string      `json:"mirror"`
	UpdateInterval string      `json:"update_interval"`
	DNS            DNSSettings `json:"dns"`
	IPv6           bool        `json:"ipv6"`
}

type DNSSettings struct {
	Remote string `json:"remote"`
	Local  string `json:"local"`
	FakeIP bool   `json:"fakeip"`
}

// SettingsDB is implemented by *sql.DB and *sql.Tx. Within a transaction, pass
// the transaction itself so settings changes commit or roll back with other data.
type SettingsDB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// GetSetting reads a typed JSON value. Missing keys wrap sql.ErrNoRows; it does
// not silently invent defaults or treat malformed JSON as a missing setting.
func GetSetting[T any](ctx context.Context, conn SettingsDB, key SettingKey[T]) (T, error) {
	var value T
	if key.name == "" {
		return value, fmt.Errorf("read setting: use a predefined setting key")
	}
	var raw string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key.name).Scan(&raw); err != nil {
		return value, fmt.Errorf("read setting %q: %w", key.name, err)
	}
	// Unmarshal otherwise accepts null for scalars and leaves a misleading zero.
	if strings.TrimSpace(raw) == "null" {
		return value, fmt.Errorf("decode setting %q: null is not a typed value; repair or restore this setting", key.name)
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		var zero T
		return zero, fmt.Errorf("decode setting %q (repair or restore this setting): %w", key.name, err)
	}
	return value, nil
}

// SetSetting inserts or replaces a JSON value without changing other settings.
func SetSetting[T any](ctx context.Context, conn SettingsDB, key SettingKey[T], value T) error {
	if key.name == "" {
		return fmt.Errorf("write setting: use a predefined setting key")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode setting %q: %w", key.name, err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key.name, string(encoded)); err != nil {
		return fmt.Errorf("write setting %q: %w", key.name, err)
	}
	return nil
}

// SetupChoices contains only onboarding preferences, never credentials.
type SetupChoices struct {
	Domain    string `json:"domain"`
	Hysteria2 bool   `json:"hysteria2"`
}
