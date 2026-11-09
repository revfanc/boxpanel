// Package confgen builds sing-box server JSON without I/O or service changes.
package confgen

import (
	"encoding/json"
	"time"
)

// Snapshot contains only the database fields needed for server generation.
// The caller must obtain it from one consistent transaction. It contains secrets.
type Snapshot struct {
	Inbounds        []Inbound
	Users           []User
	BlockBittorrent bool
}

type Inbound struct {
	ID       int64
	Tag      string
	Protocol string
	Port     int
	Enabled  bool
	Options  json.RawMessage
}

type User struct {
	ID           int64
	Name         string
	UUID         string
	Password     string
	Enabled      bool
	TrafficLimit int64
	TrafficUp    int64
	TrafficDown  int64
	ExpireAt     int64
}

// Options supplies stable runtime inputs. Build never invents paths, secrets,
// ports or a clock, and never reads certificates. File checks belong to apply.
type Options struct {
	Now            time.Time
	Hysteria2TLS   map[int64]CertificateFiles // keyed by inbound ID
	ClashAPIPort   int                        // binds only to 127.0.0.1
	ClashAPISecret string
}

type CertificateFiles struct {
	CertificatePath string
	KeyPath         string
}

type RealityOptions struct {
	ServerName      string   `json:"server_name"`
	HandshakeServer string   `json:"handshake_server"`
	HandshakePort   int      `json:"handshake_port"`
	PrivateKey      string   `json:"private_key"`
	PublicKey       string   `json:"public_key"`
	ShortIDs        []string `json:"short_ids"`
	Fingerprint     string   `json:"fingerprint"` // client metadata, never emitted here
}

type Hysteria2Options struct {
	SNI          string      `json:"sni"`
	CertMode     string      `json:"cert_mode"`
	ObfsPassword string      `json:"obfs_password"`
	UpMbps       int         `json:"up_mbps"`
	DownMbps     int         `json:"down_mbps"`
	PortHopping  PortHopping `json:"port_hopping"` // applied by T2.3, not a sing-box inbound field
}

type PortHopping struct {
	Enabled bool `json:"enabled"`
	Start   int  `json:"start"`
	End     int  `json:"end"`
}
