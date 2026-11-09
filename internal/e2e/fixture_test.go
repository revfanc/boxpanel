package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/subscription"
)

type object = map[string]any

type fixture struct {
	server                                          object
	client                                          []byte
	realityAddress, hysteriaAddress, controlAddress string
	releaseServerPorts                              func()
	secrets                                         []string
}

func newFixture(t *testing.T, handshake *httptest.Server) fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir, now := t.TempDir(), time.Now()
	vless, control := reserveTCP(t), reserveTCP(t)
	// Reserve both transports on the Hysteria2 port so the model's unique
	// numeric ports cannot collide with a TCP listener in this test.
	hy2, udp := reserveDualPort(t)
	pair, err := reality.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := reality.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	material, err := certs.Hysteria2("127.0.0.1", now)
	if err != nil {
		t.Fatal(err)
	}
	files := confgen.CertificateFiles{CertificatePath: filepath.Join(dir, "hy2.crt"), KeyPath: filepath.Join(dir, "hy2.key")}
	writePrivate(t, files.CertificatePath, material.Certificate)
	writePrivate(t, files.KeyPath, material.PrivateKey)
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		t.Fatal(err)
	}
	uuid[6], uuid[8] = uuid[6]&0x0f|0x40, uuid[8]&0x3f|0x80
	user := confgen.User{ID: 1, Name: "loopback", Enabled: true,
		UUID: fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), Password: randomToken(t)}
	// This model is persisted once and never mutated. Both generators consume
	// these exact records; Store.Load performs the real DB -> client mapping.
	snapshot := confgen.Snapshot{BlockBittorrent: true, Users: []confgen.User{user}, Inbounds: []confgen.Inbound{
		{ID: 1, Tag: "reality", Protocol: "vless_reality", Port: vless.port(), Enabled: true,
			Options: encode(t, confgen.RealityOptions{ServerName: "example.com", HandshakeServer: "127.0.0.1",
				HandshakePort: handshake.Listener.Addr().(*net.TCPAddr).Port, PrivateKey: pair.PrivateKey,
				PublicKey: pair.PublicKey, ShortIDs: []string{sid}, Fingerprint: "chrome"})},
		{ID: 2, Tag: "hysteria2", Protocol: "hysteria2", Port: hy2.port(), Enabled: true,
			Options: encode(t, confgen.Hysteria2Options{SNI: "127.0.0.1", CertMode: "self_signed"})},
	}}
	secret, token := randomToken(t), randomToken(t)
	conn, err := db.Open(ctx, filepath.Join(dir, "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, in := range snapshot.Inbounds {
		_, err = tx.ExecContext(ctx, `INSERT INTO inbounds (id,tag,protocol,port,enabled,options,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, in.ID, in.Tag, in.Protocol, in.Port, in.Enabled, string(in.Options), now.Unix(), now.Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users (id,name,uuid,password,sub_token,enabled,created_at)
		VALUES (?,?,?,?,?,?,?)`, user.ID, user.Name, user.UUID, user.Password, token, user.Enabled, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []struct {
		key   db.SettingKey[string]
		value string
	}{{db.PublicHost, "127.0.0.1"}, {db.PanelName, "e2e"}, {db.SubPath, "subscribe"}, {db.WebPath, "panel"}, {db.PanelTLSMode, "self_signed"}} {
		if err := db.SetSetting(ctx, tx, setting.key, setting.value); err != nil {
			t.Fatal(err)
		}
	}
	// Global has no remote rule sets. DNS URLs are loopback too, although all
	// actual destinations are IP literals and require no DNS queries.
	for _, err := range []error{
		db.SetSetting(ctx, tx, db.WebPort, control.port()),
		db.SetSetting(ctx, tx, db.BlockBittorrent, snapshot.BlockBittorrent),
		db.SetSetting(ctx, tx, db.Hysteria2Fingerprints, map[string]string{"2": material.Fingerprint}),
		db.SetSetting(ctx, tx, db.Rules, db.RulesSettings{Preset: "global", Source: "metacubex", UpdateInterval: "1d",
			DNS: db.DNSSettings{Remote: handshake.URL + "/dns-query", Local: handshake.URL + "/dns-query"}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := subscription.Store{DB: conn, Now: func() time.Time { return now }, Certificate: func(id int64, sni string) ([]byte, error) {
		if id != 2 || sni != "127.0.0.1" {
			return nil, fmt.Errorf("unexpected certificate lookup")
		}
		return os.ReadFile(files.CertificatePath)
	}}
	data, err := store.Load(ctx, 0, &token)
	if err != nil {
		t.Fatal(err)
	}
	client, err := subscription.Render(data.Snapshot, subscription.Singbox)
	if err != nil {
		t.Fatal(err)
	}
	server, err := confgen.Build(snapshot, confgen.Options{Now: now, ClashAPIPort: control.port(), ClashAPISecret: secret,
		Hysteria2TLS: map[int64]confgen.CertificateFiles{2: files}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{server: loopbackServer(t, server), client: client,
		realityAddress: vless.address(), hysteriaAddress: hy2.address(), controlAddress: control.address(),
		releaseServerPorts: func() { vless.release(); hy2.release(); _ = udp.Close(); control.release() },
		secrets:            []string{pair.PrivateKey, user.UUID, user.Password, token, secret, string(material.PrivateKey)}}
}

func loopbackServer(t *testing.T, raw []byte) object {
	t.Helper()
	cfg := decode(t, raw)
	for _, raw := range cfg["inbounds"].([]any) {
		raw.(object)["listen"] = "127.0.0.1"
	}
	route := cfg["route"].(object)
	kept, removed := []any{}, 0
	for _, raw := range route["rules"].([]any) {
		rule := raw.(object)
		if rule["ip_is_private"] == true && rule["action"] == "reject" {
			removed++
			continue
		}
		kept = append(kept, rule)
	}
	if removed != 1 {
		t.Fatal("expected exactly one production private-IP rejection rule")
	}
	route["rules"] = kept
	return cfg
}

func loopbackClient(t *testing.T, raw []byte, node string, port int) object {
	t.Helper()
	cfg := decode(t, raw) // fresh copy for each case, including the wrong-key case
	var mixed []any
	for _, raw := range cfg["inbounds"].([]any) {
		in := raw.(object)
		if in["type"] == "mixed" {
			in["listen_port"] = port
			mixed = append(mixed, in)
		}
	}
	if len(mixed) != 1 {
		t.Fatal("subscription must contain exactly one mixed inbound")
	}
	cfg["inbounds"] = mixed
	var outbounds []any
	found := false
	for _, raw := range cfg["outbounds"].([]any) {
		out := raw.(object)
		if out["type"] == "urltest" {
			continue // never start the generated external gstatic health check
		}
		if out["tag"] == "proxy" {
			out["outbounds"], out["default"] = []string{node}, node
		}
		if out["tag"] == node {
			found = true
		}
		outbounds = append(outbounds, out)
	}
	if !found {
		t.Fatal("tested node missing from production subscription")
	}
	cfg["outbounds"] = outbounds
	// No TUN, automatic interface binding, private-IP bypass or rule downloads.
	cfg["route"] = object{"rules": []any{}, "final": node, "default_domain_resolver": "dns-local"}
	return cfg
}

func randomToken(t *testing.T) string {
	t.Helper()
	v, err := auth.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) object {
	t.Helper()
	var cfg object
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
