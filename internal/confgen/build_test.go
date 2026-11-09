package confgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
)

var update = flag.Bool("update", false, "update configuration golden files")

func jsonOptions(t *testing.T, options any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func fixture(t *testing.T) (Snapshot, Options) {
	t.Helper()
	// Public, deterministic TEST credentials, never use in a real deployment.
	raw := bytes.Repeat([]byte{0x11}, 32)
	raw[0], raw[31] = 0x10, 0x51
	private := base64.RawURLEncoding.EncodeToString(raw)
	public, err := reality.PublicKey(private)
	if err != nil {
		t.Fatal(err)
	}
	r := RealityOptions{ServerName: "www.example.com", HandshakeServer: "www.example.com", HandshakePort: 443,
		PrivateKey: private, PublicKey: public, ShortIDs: []string{"0123456789abcdef", "12345678"}, Fingerprint: "chrome"}
	h := Hysteria2Options{SNI: "www.bing.com", CertMode: "self_signed", PortHopping: PortHopping{Start: 20000, End: 30000}}
	snapshot := Snapshot{
		BlockBittorrent: true,
		Inbounds: []Inbound{
			{ID: 2, Tag: "hy2-8443", Protocol: "hysteria2", Port: 8443, Enabled: true, Options: jsonOptions(t, h)},
			{ID: 1, Tag: "vless-reality-443", Protocol: "vless_reality", Port: 443, Enabled: true, Options: jsonOptions(t, r)},
			{ID: 3, Tag: "disabled", Protocol: "future", Port: 0, Enabled: false, Options: json.RawMessage(`null`)},
		},
		Users: []User{
			{ID: 2, Name: "bob", UUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Password: "test-only-bob-password", Enabled: true},
			{ID: 1, Name: "alice", UUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Password: "test-only-alice-password", Enabled: true},
		},
	}
	opts := Options{Now: time.Unix(1804766400, 0), ClashAPIPort: 19090, ClashAPISecret: "test-only-clash-secret",
		Hysteria2TLS: map[int64]CertificateFiles{2: {CertificatePath: "/etc/boxpanel/certs/hy2.crt", KeyPath: "/etc/boxpanel/certs/hy2.key"}}}
	return snapshot, opts
}

func TestServerGolden(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Snapshot, *Options)
	}{
		{"vless", func(s *Snapshot, _ *Options) { s.Inbounds = s.Inbounds[1:2] }},
		{"hysteria2", func(s *Snapshot, _ *Options) { s.Inbounds = s.Inbounds[:1] }},
		{"dual", func(_ *Snapshot, _ *Options) {}},
		{"no_users", func(s *Snapshot, _ *Options) { s.Users = nil }},
		{"filtered_users", func(s *Snapshot, o *Options) {
			s.Users[0].TrafficLimit, s.Users[0].TrafficUp, s.Users[0].TrafficDown = 100, 40, 59
			s.Users[0].ExpireAt = o.Now.Unix() + 1
			s.Users[1].TrafficUp, s.Users[1].TrafficDown = math.MaxInt64, math.MaxInt64 // unlimited
			s.Users = append(s.Users,
				User{ID: 3, Name: "disabled", Enabled: false},
				User{ID: 4, Name: "at_limit", Enabled: true, TrafficLimit: 100, TrafficUp: 40, TrafficDown: 60},
				User{ID: 5, Name: "over_limit", Enabled: true, TrafficLimit: 100, TrafficUp: 101},
				User{ID: 6, Name: "overflow", Enabled: true, TrafficLimit: math.MaxInt64, TrafficUp: math.MaxInt64 - 1, TrafficDown: 10},
				User{ID: 7, Name: "at_expiry", Enabled: true, ExpireAt: o.Now.Unix()},
				User{ID: 8, Name: "expired", Enabled: true, ExpireAt: o.Now.Unix() - 1},
			)
		}},
		{"bt_off", func(s *Snapshot, _ *Options) { s.BlockBittorrent = false }},
		{"hy2_options", func(s *Snapshot, _ *Options) {
			s.Inbounds = s.Inbounds[:1]
			s.Inbounds[0].Options = jsonOptions(t, Hysteria2Options{SNI: "www.bing.com", CertMode: "self_signed",
				UpMbps: 100, DownMbps: 200, ObfsPassword: "test-only-obfs-password", PortHopping: PortHopping{Enabled: true, Start: 20000, End: 30000}})
		}},
		{"no_inbounds", func(s *Snapshot, _ *Options) { s.Inbounds = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, opts := fixture(t)
			tc.change(&snapshot, &opts)
			before, _ := json.Marshal(snapshot)
			actual, err := Build(snapshot, opts)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(snapshot)
			if !bytes.Equal(before, after) {
				t.Fatal("Build mutated input")
			}
			// A different query order must not change the serialized output.
			slices.Reverse(snapshot.Inbounds)
			slices.Reverse(snapshot.Users)
			again, err := Build(snapshot, opts)
			if err != nil || !bytes.Equal(actual, again) {
				t.Fatal("unstable output after reordering input")
			}
			path := filepath.Join("testdata", tc.name+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, actual, 0644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err, "; regenerate with go test ./internal/confgen -update")
			}
			if !bytes.Equal(actual, golden) {
				t.Fatal("golden mismatch; review change before running go test ./internal/confgen -update")
			}
			t.Run("sing_box_check", func(t *testing.T) { checkGolden(t, golden) })
		})
	}
}

func checkGolden(t *testing.T, golden []byte) {
	t.Helper()
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not in PATH; run make tools and make test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "sing-box version "+singbox.PinnedVersion+"\n") {
		t.Fatal("golden validation requires the pinned sing-box binary")
	}
	dir := t.TempDir()
	material, err := certs.Hysteria2("www.bing.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "hy2.crt"), filepath.Join(dir, "hy2.key")
	for path, data := range map[string][]byte{certPath: material.Certificate, keyPath: material.PrivateKey} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Check the actual golden bytes, replacing only deployment certificate paths.
	for old, path := range map[string]string{"/etc/boxpanel/certs/hy2.crt": certPath, "/etc/boxpanel/certs/hy2.key": keyPath} {
		from, _ := json.Marshal(old)
		to, _ := json.Marshal(path)
		golden = bytes.ReplaceAll(golden, from, to)
	}
	configPath := filepath.Join(dir, "sing-box.json")
	if err := os.WriteFile(configPath, golden, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "check", "-c", configPath)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sing-box check: %v\n%s", err, output)
	}
}

func TestFilteringAndServerFields(t *testing.T) {
	snapshot, opts := fixture(t)
	snapshot.Users[0].Enabled = false
	snapshot.Users[1].ExpireAt = opts.Now.Unix()
	output, err := Build(snapshot, opts)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(output, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in.Users == nil || len(in.Users) != 0 {
			t.Fatal("all filtered users must produce users:[]")
		}
	}
	for _, forbidden := range []string{"public_key", "fingerprint", "port_hopping", "cert_mode", "test-only-bob-password", "test-only-alice-password"} {
		if bytes.Contains(output, []byte(forbidden)) {
			t.Fatalf("unexpected server field or credential: %s", forbidden)
		}
	}
	want := []routeRule{{Action: "sniff"}, {Protocol: "bittorrent", Action: "reject"}, {Action: "resolve", Server: "dns-local"}, {IPIsPrivate: true, Action: "reject"}}
	if !reflect.DeepEqual(cfg.Route.Rules, want) {
		t.Fatal("private destination guard or rule order changed")
	}
	if cfg.Inbounds[0].Type != "vless" || cfg.Inbounds[1].Type != "hysteria2" || !cfg.Inbounds[1].IgnoreClientBandwidth {
		t.Fatal("incorrect inbound order or unlimited bandwidth policy")
	}
	if cfg.Experimental.ClashAPI.ExternalController != "127.0.0.1:19090" {
		t.Fatal("Clash API must bind to loopback")
	}
}

func TestInvalidSnapshots(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Snapshot, *Options)
	}{
		{"clock", func(_ *Snapshot, o *Options) { o.Now = time.Time{} }},
		{"controller_port", func(_ *Snapshot, o *Options) { o.ClashAPIPort = 65536 }},
		{"controller_secret", func(_ *Snapshot, o *Options) { o.ClashAPISecret = "" }},
		{"controller_collision", func(_ *Snapshot, o *Options) { o.ClashAPIPort = 443 }},
		{"cert_paths", func(_ *Snapshot, o *Options) { o.Hysteria2TLS = nil }},
		{"same_cert_path", func(_ *Snapshot, o *Options) { o.Hysteria2TLS[2] = CertificateFiles{"/tmp/same", "/tmp/same"} }},
		{"relative_cert_path", func(_ *Snapshot, o *Options) { o.Hysteria2TLS[2] = CertificateFiles{"cert.pem", "key.pem"} }},
		{"inbound_id", func(s *Snapshot, _ *Options) { s.Inbounds[0].ID = 0 }},
		{"duplicate_inbound_id", func(s *Snapshot, _ *Options) { s.Inbounds[0].ID = 1 }},
		{"duplicate_port", func(s *Snapshot, _ *Options) { s.Inbounds[0].Port = 443 }},
		{"duplicate_tag", func(s *Snapshot, _ *Options) { s.Inbounds[0].Tag = s.Inbounds[1].Tag }},
		{"port", func(s *Snapshot, _ *Options) { s.Inbounds[0].Port = 65536 }},
		{"protocol", func(s *Snapshot, _ *Options) { s.Inbounds[0].Protocol = "future" }},
		{"user_id", func(s *Snapshot, _ *Options) { s.Users[0].ID = 0 }},
		{"duplicate_user_id", func(s *Snapshot, _ *Options) { s.Users[0].ID = 1 }},
		{"duplicate_user_name", func(s *Snapshot, _ *Options) { s.Users[0].Name = "alice" }},
		{"user_name", func(s *Snapshot, _ *Options) { s.Users[0].Name = "bad name" }},
		{"uuid", func(s *Snapshot, _ *Options) { s.Users[0].UUID = "secret-invalid-uuid" }},
		{"duplicate_uuid", func(s *Snapshot, _ *Options) { s.Users[0].UUID = strings.ToUpper(s.Users[1].UUID) }},
		{"password", func(s *Snapshot, _ *Options) { s.Users[0].Password = "" }},
		{"duplicate_password", func(s *Snapshot, _ *Options) { s.Users[0].Password = s.Users[1].Password }},
		{"negative_limit", func(s *Snapshot, _ *Options) { s.Users[0].TrafficLimit = -1 }},
		{"negative_up", func(s *Snapshot, _ *Options) { s.Users[0].TrafficUp = -1 }},
		{"negative_down", func(s *Snapshot, _ *Options) { s.Users[0].TrafficDown = -1 }},
		{"negative_expiry", func(s *Snapshot, _ *Options) { s.Users[0].ExpireAt = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, o := fixture(t)
			tc.change(&s, &o)
			out, err := Build(s, o)
			if err == nil || out != nil {
				t.Fatal("accepted invalid snapshot")
			}
			if strings.Contains(err.Error(), "secret-invalid-uuid") || strings.Contains(err.Error(), "test-only") {
				t.Fatal("error leaked credentials")
			}
		})
	}
}

func TestInvalidProtocolOptions(t *testing.T) {
	cases := []struct {
		name  string
		index int
		field string
		value any
	}{
		{"reality_name", 1, "server_name", "https://example.com"},
		{"reality_handshake", 1, "handshake_server", ""},
		{"reality_port", 1, "handshake_port", 0},
		{"reality_private", 1, "private_key", "secret-private-key"},
		{"reality_public", 1, "public_key", "secret-public-key"},
		{"no_short_ids", 1, "short_ids", []string{}},
		{"short_id_hex", 1, "short_ids", []string{"secret-short-id"}},
		{"short_id_odd", 1, "short_ids", []string{"123456789"}},
		{"short_id_short", 1, "short_ids", []string{"123456"}},
		{"short_id_long", 1, "short_ids", []string{"0123456789abcdef00"}},
		{"sni", 0, "sni", "fe80::1%en0"},
		{"acme", 0, "cert_mode", "acme"},
		{"up_mbps", 0, "up_mbps", -1},
		{"down_mbps", 0, "down_mbps", -1},
		{"port_hopping", 0, "port_hopping", PortHopping{Enabled: true, Start: 30000, End: 20000}},
		{"unknown_field", 0, "secret-unknown-field", "secret-value"},
		{"wrong_type", 0, "up_mbps", "secret-wrong-type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, o := fixture(t)
			var fields map[string]any
			if err := json.Unmarshal(s.Inbounds[tc.index].Options, &fields); err != nil {
				t.Fatal(err)
			}
			fields[tc.field] = tc.value
			s.Inbounds[tc.index].Options = jsonOptions(t, fields)
			out, err := Build(s, o)
			if err == nil || out != nil {
				t.Fatal("accepted invalid protocol option")
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("error leaked option contents")
			}
		})
	}
	for _, raw := range []string{"", "null", "[]", "{", `{} {}`, `{"private_key":"secret",}`} {
		s, o := fixture(t)
		s.Inbounds[1].Options = json.RawMessage(raw)
		if _, err := Build(s, o); err == nil {
			t.Fatal("accepted invalid JSON options")
		}
	}
}
