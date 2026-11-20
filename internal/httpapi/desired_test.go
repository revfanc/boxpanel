package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func joinTestNode(t *testing.T, master *agent.Master, client *agent.Client, url, pin, name, ip string) (agent.Node, app.Paths) {
	t.Helper()
	ctx := t.Context()
	_, token, err := master.IssueToken(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	paths := app.Paths{DataDir: t.TempDir()}
	paths.Database = filepath.Join(paths.DataDir, "boxpanel.db")
	id, err := app.JoinNode(ctx, paths, url, pin, token, name, ip, "test")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Open(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	identity, err := db.GetSetting(ctx, conn, db.AgentMaster)
	if err != nil || identity.ServerID != id.ServerID {
		t.Fatal("local agent identity does not match registration", err)
	}
	key, err := app.LoadAgentKey(paths, identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return agent.Node{Reader: conn, Writer: conn, Master: identity, Key: key, Client: client}, paths
}

func TestRemoteDesiredStateLifecycle(t *testing.T) {
	ctx := t.Context()
	f, store, _, _ := newSubscriptionAPI(t)
	var now atomic.Int64
	now.Store(time.Now().Unix())
	master := agent.NewMaster(f.conn, nil, func() time.Time { return time.Unix(now.Load(), 0) })
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	portInspections := 0
	f.h = New(service, Options{
		DB: f.conn, Agents: master, Applier: f.apply, Subscriptions: store, PanelPort: 2053,
		Logger: slog.New(slog.NewTextHandler(f.logs, nil)),
		ListeningPorts: func(context.Context) (map[sysinfo.Socket]bool, error) {
			portInspections++
			return map[sysinfo.Socket]bool{{Port: 2053, Proto: "tcp"}: true}, nil
		},
		RealityProbe: func(context.Context, string, int) reality.Target {
			t.Error("remote edit or suggestion probed the Reality target from the master")
			return reality.Target{}
		},
	})
	server := httptest.NewTLSServer(f.h)
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	client, err := agent.NewClient(server.URL, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	edge, paths := joinTestNode(t, master, client, server.URL, pin, "edge", "8.8.8.8")
	other, _ := joinTestNode(t, master, client, server.URL, pin, "other", "8.8.4.4")
	f.call(t, "POST", "inbounds", `{"server_id":2,"protocol":"hysteria2"}`, 422)
	beat := agent.Heartbeat{Version: "test", SingboxActive: true}
	for _, node := range []*agent.Node{&edge, &other} {
		if err := client.Heartbeat(ctx, node.Master.ServerID, node.Key, beat); err != nil {
			t.Fatal(err)
		}
	}
	generation := func(id int64) int64 {
		t.Helper()
		var value int64
		if err := f.conn.QueryRow("SELECT desired_generation FROM servers WHERE id=?", id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	edgeGen, otherGen := generation(2), generation(3)
	f.runner.failCheck = true
	f.call(t, "POST", "users", `{"name":"apply_rollback"}`, 422)
	f.runner.failCheck = false
	if generation(2) != edgeGen || generation(3) != otherGen {
		t.Fatal("master rollback left new desired generations")
	}
	f.call(t, "POST", "users", `{"name":"alice"}`, 201)
	f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","port":443,"options":{"server_name":"www.example.com"}}`, 201)
	localBytes, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	localCalls, localPorts := f.apply.calls, portInspections
	otherGen = generation(other.Master.ServerID)
	edgeGen = generation(edge.Master.ServerID)
	f.call(t, "POST", "inbounds/suggest", `{"server_id":2,"protocol":"vless_reality"}`, 200)
	f.call(t, "POST", "inbounds", `{"server_id":2,"protocol":"vless_reality","port":2053,"options":{"server_name":"www.example.com","private_key":"forbidden"}}`, 400)
	r := decodeResult[inboundRecord](t, f.call(t, "POST", "inbounds",
		`{"server_id":2,"protocol":"vless_reality","port":2053,"options":{"server_name":"www.example.com"}}`, 201))
	hy := decodeResult[inboundRecord](t, f.call(t, "POST", "inbounds", `{"server_id":2,"protocol":"hysteria2","port":8443}`, 201))
	foreign := decodeResult[inboundRecord](t, f.call(t, "POST", "inbounds", `{"server_id":3,"protocol":"hysteria2","port":8443}`, 201))
	if r.KeyStatus != "pending" || hy.KeyStatus != "pending" || generation(2) != edgeGen+2 || generation(3) != otherGen+1 ||
		localPorts != portInspections || localCalls != f.apply.calls {
		t.Fatal("remote edit did not use scoped, queued changes")
	}
	load := func() int {
		t.Helper()
		data, err := store.Load(ctx, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(data.Nodes)
	}
	if load() != 1 {
		t.Fatal("pending inbound appeared in subscription")
	}

	want, err := client.Desired(ctx, edge.Master.ServerID, edge.Key)
	if err != nil || len(want.Inbounds) != 2 || want.Inbounds[0].ID != r.ID || want.Inbounds[1].ID != hy.ID ||
		len(want.Users) != 1 || want.Users[0].Password == "" || !want.Route.BlockBittorrent {
		t.Fatal("desired scope, users or route incorrect", err)
	}
	if _, err := client.Desired(ctx, other.Master.ServerID, edge.Key); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatal("server ID override allowed", err)
	}
	for _, target := range []string{"/api/agent/v1/desired", "/api/agent/v1/desired?server_id=3"} {
		requireStatus(t, request(t, f.h, http.MethodGet, "https://example.test"+target, "", f.cookie, nil), 401)
	}
	for _, ownedByOther := range []int64{1, foreign.ID} {
		if err := client.PublishKeys(ctx, 2, edge.Key, agent.InboundKeys{InboundID: ownedByOther}); !errors.Is(err, agent.ErrUnauthorized) {
			t.Fatal("foreign inbound publication allowed", err)
		}
	}
	requireStatus(t, request(t, f.h, http.MethodPost, "https://example.test/api/agent/v1/inbound-keys",
		`{"inbound_id":2}`, f.cookie, nil), 401)
	if err := client.PublishKeys(ctx, 2, edge.Key, agent.InboundKeys{InboundID: r.ID, KeyID: want.Inbounds[0].KeyID, PublicKey: "invalid"}); err == nil {
		t.Fatal("bad Reality public material accepted")
	}

	nodeConfig := filepath.Join(paths.DataDir, "sing-box.json")
	runner, fw := &resourceRunner{}, &resourceFirewall{}
	edge.Manager, err = singbox.NewManager(edge.Reader, singbox.ApplyOptions{OS: "linux", Config: nodeConfig, Runner: runner, Firewall: fw})
	if err != nil {
		t.Fatal(err)
	}
	sync := func() {
		t.Helper()
		if err := edge.Sync(ctx, beat, func() bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	inspect := func() serverRecord {
		t.Helper()
		rows := decodeResult[[]serverRecord](t, f.call(t, "GET", "servers", "", 200))
		return rows[1]
	}
	if row := inspect(); row.SyncStatus != "synced" || *row.AppliedGeneration != generation(2) ||
		row.SingboxActive == nil || !*row.SingboxActive || load() != 3 ||
		len(fw.plan.TrafficUsers) != 1 || len(fw.plan.Ports) != 2 {
		t.Fatal("node did not apply, publish and report a healthy generation", row)
	}
	read := func(filename string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	nodeBytes := read(nodeConfig)
	if !bytes.Equal(read(f.config), localBytes) {
		t.Fatal("remote apply rewrote the master config")
	}
	material := func(conn db.SettingsDB, field string, id int64) string {
		t.Helper()
		var value string
		if err := conn.QueryRowContext(ctx, "SELECT coalesce(json_extract(options,?), '') FROM inbounds WHERE id=?", field, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	public, private := material(f.conn, "$.public_key", r.ID), material(edge.Reader, "$.private_key", r.ID)
	fingerprint := material(f.conn, "$.fingerprint", hy.ID)
	if public == "" || private == "" || fingerprint == "" || material(f.conn, "$.private_key", r.ID) != "" {
		t.Fatal("invalid key ownership")
	}
	if err := client.PublishKeys(ctx, 2, edge.Key, agent.InboundKeys{InboundID: hy.ID, KeyID: want.Inbounds[1].KeyID,
		CertSHA256: "invalid", CertPEM: material(f.conn, "$.certificate", hy.ID)}); err == nil {
		t.Fatal("invalid Hysteria2 fingerprint accepted")
	}
	f.call(t, "PUT", "servers/2", `{"public_host":"edge.example.com"}`, 200)
	links := f.call(t, "GET", "users/1/links", "", 200).Body.String()
	for _, value := range []string{"edge.example.com", public, fingerprint} {
		if !strings.Contains(links, value) {
			t.Fatal("ready node's host or public material missing from links")
		}
	}
	if strings.Contains(links, private) || strings.Contains(f.logs.String(), private) || strings.Contains(f.logs.String(), want.Users[0].Password) {
		t.Fatal("node private key or connection credentials leaked")
	}

	checks, restarts, firewallCalls := runner.checks, runner.restarts, fw.calls
	f.call(t, "PUT", fmt.Sprintf("inbounds/%d", hy.ID), `{"remark":"new label","options":{"up_mbps":0}}`, 200)
	sync()
	if runner.checks != checks || runner.restarts != restarts || fw.calls != firewallCalls || !bytes.Equal(read(nodeConfig), nodeBytes) ||
		material(f.conn, "$.fingerprint", hy.ID) != fingerprint {
		t.Fatal("metadata-only sync restarted or lost the certificate")
	}

	edgeGen, otherGen = generation(2), generation(3)
	f.call(t, "POST", "users", fmt.Sprintf(`{"name":"bob","expire_at":%d}`, now.Load()+5), 201)
	if generation(2) != edgeGen+1 || generation(3) != otherGen+1 {
		t.Fatal("effective user changes did not advance both desired generations")
	}
	runner.failCheck = true
	last := *inspect().AppliedGeneration
	sync()
	if row := inspect(); row.SyncStatus != "failed" || row.SyncError != agent.SyncCheckFailed || *row.AppliedGeneration != last ||
		!bytes.Equal(read(nodeConfig), nodeBytes) {
		t.Fatal("failed apply changed the active generation or failed to report safely", row)
	}
	var users int
	if err := edge.Reader.QueryRow("SELECT count(*) FROM users").Scan(&users); err != nil || users != 1 {
		t.Fatal("failed apply changed mirrored users", err)
	}
	runner.failCheck = false
	sync()
	if !bytes.Contains(read(nodeConfig), []byte("bob")) {
		t.Fatal("new user was not synced")
	}
	now.Add(6)
	sync()
	if row := inspect(); row.SyncStatus != "synced" || *row.AppliedGeneration != edgeGen+2 ||
		bytes.Contains(read(nodeConfig), []byte("bob")) {
		t.Fatal("expiration alone did not sync", row)
	}
	f.call(t, "PUT", "settings", `{"block_bittorrent":false}`, 200)
	sync()
	if block, err := db.GetSetting(ctx, edge.Reader, db.BlockBittorrent); err != nil || block {
		t.Fatal("BT setting was not synced", err)
	}

	keyFiles, err := filepath.Glob(filepath.Join(paths.DataDir, "certs", "*.key"))
	if err != nil || len(keyFiles) != 1 {
		t.Fatal("expected a single node TLS key", err)
	}
	if info, err := os.Stat(keyFiles[0]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("node TLS key must be private", err)
	}
	if err := os.Remove(keyFiles[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := edge.Reader.Exec("UPDATE inbounds SET options=json_remove(options,'$.private_key') WHERE id=?", r.ID); err != nil {
		t.Fatal(err)
	}
	last = generation(2)
	sync()
	if material(f.conn, "$.public_key", r.ID) == public || material(f.conn, "$.fingerprint", hy.ID) == fingerprint ||
		generation(2) != last || load() != 3 {
		t.Fatal("node did not recover missing keys without changing desired generation")
	}

	hyPath := fmt.Sprintf("inbounds/%d", hy.ID)
	oldCertificate := material(f.conn, "$.certificate", hy.ID)
	oldFingerprint := material(f.conn, "$.fingerprint", hy.ID)
	hy = decodeResult[inboundRecord](t, f.call(t, "PUT", hyPath, `{"options":{"sni":"another.example.com"}}`, 200))
	if hy.KeyStatus != "pending" || load() != 2 {
		t.Fatal("SNI edit kept old certificate in the subscription")
	}
	if err := client.PublishKeys(ctx, 2, edge.Key, agent.InboundKeys{InboundID: hy.ID, KeyID: want.Inbounds[1].KeyID,
		CertSHA256: oldFingerprint, CertPEM: oldCertificate}); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatal("obsolete material revision was accepted", err)
	}
	sync()
	if load() != 3 {
		t.Fatal("new certificate did not reach subscription")
	}

	last = generation(2)
	if _, err := f.conn.Exec("UPDATE users SET traffic_limit=100,traffic_up=99 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if generation(2) != last {
		t.Fatal("below-quota sampling changed desired generation")
	}
	if _, err := f.conn.Exec("UPDATE users SET traffic_up=100 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	sync()
	if generation(2) != last+1 || bytes.Contains(read(nodeConfig), []byte("alice")) {
		t.Fatal("crossing quota did not update the node")
	}

	f.call(t, "POST", fmt.Sprintf("inbounds/%d/toggle", r.ID), `{"enabled":false}`, 200)
	f.call(t, "DELETE", "users/1", "", 204)
	sync()
	if err := edge.Reader.QueryRow("SELECT count(*) FROM users").Scan(&users); err != nil || users != 0 ||
		bytes.Contains(read(nodeConfig), []byte("vless-reality-2053")) {
		t.Fatal("user removal or inbound toggle did not reach node", err)
	}
	f.call(t, "DELETE", fmt.Sprintf("inbounds/%d", hy.ID), "", 204)
	beforeOffline := read(nodeConfig)
	server.Close()
	if err := edge.Sync(ctx, beat, func() bool { return true }); err == nil {
		t.Fatal("expected a network failure")
	}
	if !bytes.Equal(read(nodeConfig), beforeOffline) {
		t.Fatal("unreachable master changed active config")
	}
}
