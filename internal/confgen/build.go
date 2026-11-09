package confgen

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/reality"
)

// Build returns indented JSON with a trailing newline. Output contains live
// credentials: do not log it or expose it through an API without redaction.
// It does not mutate the snapshot, write configuration, or execute sing-box.
func Build(snapshot Snapshot, opts Options) ([]byte, error) {
	if opts.Now.IsZero() {
		return nil, fmt.Errorf("build config: provide a snapshot evaluation time")
	}
	if !validPort(opts.ClashAPIPort) || strings.TrimSpace(opts.ClashAPISecret) == "" {
		return nil, fmt.Errorf("build config: supply a Clash API port (1–65535) and a stored random secret")
	}
	users, err := eligibleUsers(snapshot.Users, opts.Now.Unix())
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}
	cfg := config{
		Log:       logConfig{Level: "warn", Timestamp: true},
		DNS:       dnsConfig{Servers: []dnsServer{{Type: "local", Tag: "dns-local"}}, Final: "dns-local"},
		Inbounds:  []inboundConfig{},
		Outbounds: []outboundConfig{{Type: "direct", Tag: "direct"}},
		Route:     routeConfig{Rules: []routeRule{{Action: "sniff"}}, Final: "direct", DefaultDomainResolver: "dns-local"},
		Experimental: experimentalConfig{ClashAPI: clashAPIConfig{
			ExternalController: "127.0.0.1:" + strconv.Itoa(opts.ClashAPIPort), Secret: opts.ClashAPISecret,
		}},
	}
	if snapshot.BlockBittorrent {
		cfg.Route.Rules = append(cfg.Route.Rules, routeRule{Protocol: "bittorrent", Action: "reject"})
	}
	// Resolve domain destinations before the IP rule so domains cannot bypass it.
	cfg.Route.Rules = append(cfg.Route.Rules,
		routeRule{Action: "resolve", Server: "dns-local"},
		routeRule{IPIsPrivate: true, Action: "reject"},
	)
	inbounds := slices.Clone(snapshot.Inbounds)
	slices.SortFunc(inbounds, func(a, b Inbound) int { return compareID(a.ID, b.ID) })
	ids, tags, ports := map[int64]bool{}, map[string]bool{}, map[int]bool{}
	for _, in := range inbounds {
		if !in.Enabled {
			continue
		}
		if in.ID <= 0 || ids[in.ID] || strings.TrimSpace(in.Tag) == "" || tags[in.Tag] || !validPort(in.Port) || ports[in.Port] {
			return nil, fmt.Errorf("build config: inbound %d: use a positive unique ID, a nonempty unique tag and a unique port (1–65535)", in.ID)
		}
		if in.Protocol == "vless_reality" && in.Port == opts.ClashAPIPort {
			return nil, fmt.Errorf("build config: inbound %d: choose a TCP port different from the Clash API port", in.ID)
		}
		ids[in.ID], tags[in.Tag], ports[in.Port] = true, true, true
		out, err := buildInbound(in, users, opts)
		if err != nil {
			return nil, fmt.Errorf("build config: inbound %d: %w", in.ID, err)
		}
		cfg.Inbounds = append(cfg.Inbounds, out)
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode server config: %w", err)
	}
	return append(encoded, '\n'), nil
}

func eligibleUsers(input []User, now int64) ([]User, error) {
	users := make([]User, 0, len(input))
	ids, names := map[int64]bool{}, map[string]bool{}
	for _, user := range input {
		if !user.Enabled {
			continue
		}
		if user.TrafficLimit < 0 || user.TrafficUp < 0 || user.TrafficDown < 0 || user.ExpireAt < 0 {
			return nil, fmt.Errorf("user %d: restore nonnegative traffic and expiry values", user.ID)
		}
		// Subtraction avoids int64 overflow even with very large traffic counters.
		if user.ExpireAt > 0 && user.ExpireAt <= now ||
			user.TrafficLimit > 0 && (user.TrafficUp >= user.TrafficLimit || user.TrafficDown >= user.TrafficLimit-user.TrafficUp) {
			continue
		}
		if user.ID <= 0 || ids[user.ID] || !validUserName(user.Name) || names[user.Name] {
			return nil, fmt.Errorf("user %d: use a positive unique ID and a unique 1–32 character name containing letters, digits, _ or -", user.ID)
		}
		ids[user.ID], names[user.Name] = true, true
		users = append(users, user)
	}
	slices.SortFunc(users, func(a, b User) int { return compareID(a.ID, b.ID) })
	return users, nil
}

func buildInbound(in Inbound, users []User, opts Options) (inboundConfig, error) {
	out := inboundConfig{Tag: in.Tag, Listen: "::", ListenPort: in.Port, Users: []serverUser{}, TLS: tlsConfig{Enabled: true}}
	switch in.Protocol {
	case "vless_reality":
		var options RealityOptions
		if err := decodeOptions(in.Options, &options); err != nil {
			return out, err
		}
		if !validHost(options.ServerName) || !validHost(options.HandshakeServer) || !validPort(options.HandshakePort) {
			return out, fmt.Errorf("Reality: supply server_name, handshake_server (DNS name or IP) and handshake_port (1–65535)")
		}
		public, err := reality.PublicKey(options.PrivateKey)
		if err != nil {
			return out, fmt.Errorf("Reality: %w", err)
		}
		if public != options.PublicKey {
			return out, fmt.Errorf("Reality: public_key does not match private_key; regenerate the key pair")
		}
		if len(options.ShortIDs) == 0 {
			return out, fmt.Errorf("Reality: generate at least one short ID")
		}
		for _, id := range options.ShortIDs {
			decoded, err := hex.DecodeString(id)
			if err != nil || len(decoded) < 4 || len(decoded) > 8 {
				return out, fmt.Errorf("Reality: use even-length short IDs of 8–16 hexadecimal characters")
			}
		}
		out.Type, out.TLS.ServerName = "vless", options.ServerName
		out.TLS.Reality = &realityConfig{
			Enabled: true, Handshake: handshakeConfig{Server: options.HandshakeServer, ServerPort: options.HandshakePort},
			PrivateKey: options.PrivateKey, ShortID: options.ShortIDs,
		}
		seen := map[string]bool{}
		for _, user := range users {
			uuid := strings.ToLower(user.UUID)
			if !validUUID(uuid) || seen[uuid] {
				return out, fmt.Errorf("user %d: regenerate a valid unique UUID", user.ID)
			}
			seen[uuid] = true
			out.Users = append(out.Users, serverUser{Name: user.Name, UUID: uuid, Flow: "xtls-rprx-vision"})
		}
	case "hysteria2":
		var options Hysteria2Options
		if err := decodeOptions(in.Options, &options); err != nil {
			return out, err
		}
		if !validHost(options.SNI) || options.CertMode != "self_signed" {
			return out, fmt.Errorf("Hysteria2: supply a valid SNI and use cert_mode self_signed; ACME is not supported yet")
		}
		if options.UpMbps < 0 || options.DownMbps < 0 {
			return out, fmt.Errorf("Hysteria2: use nonnegative bandwidth limits in Mbps (0 means unlimited)")
		}
		if h := options.PortHopping; h.Enabled && (!validPort(h.Start) || !validPort(h.End) || h.Start > h.End) {
			return out, fmt.Errorf("Hysteria2: set an ordered port hopping range within 1–65535")
		}
		files := opts.Hysteria2TLS[in.ID]
		if !validPath(files.CertificatePath) || !validPath(files.KeyPath) || filepath.Clean(files.CertificatePath) == filepath.Clean(files.KeyPath) {
			return out, fmt.Errorf("Hysteria2: supply distinct absolute certificate and private key paths")
		}
		out.Type, out.TLS.ALPN = "hysteria2", []string{"h3"}
		out.TLS.CertificatePath, out.TLS.KeyPath = files.CertificatePath, files.KeyPath
		out.UpMbps, out.DownMbps = options.UpMbps, options.DownMbps
		out.IgnoreClientBandwidth = options.UpMbps == 0 && options.DownMbps == 0
		if options.ObfsPassword != "" {
			out.Obfs = &obfsConfig{Type: "salamander", Password: options.ObfsPassword}
		}
		seen := map[string]bool{}
		for _, user := range users {
			if user.Password == "" || seen[user.Password] {
				return out, fmt.Errorf("user %d: regenerate a nonempty unique Hysteria2 password", user.ID)
			}
			seen[user.Password] = true
			out.Users = append(out.Users, serverUser{Name: user.Name, Password: user.Password})
		}
	default:
		return out, fmt.Errorf("unsupported protocol; choose vless_reality or hysteria2")
	}
	return out, nil
}

func decodeOptions(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || decoder.Decode(target) != nil {
		return fmt.Errorf("decode options: restore a JSON object with the documented protocol fields and types")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("decode options: supply exactly one JSON object")
	}
	return nil
}

func compareID(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func validPort(port int) bool { return port > 0 && port <= 65535 }

func validPath(path string) bool { return filepath.IsAbs(path) && !strings.ContainsRune(path, 0) }

func validUUID(uuid string) bool {
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	return err == nil && len(raw) == 16
}

func validUserName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
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
