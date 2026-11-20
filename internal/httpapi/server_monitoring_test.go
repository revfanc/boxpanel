package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/subscription"
)

func TestMultiServerMonitoringLifecycle(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f, store, _, _ := newSubscriptionAPI(t)
	binary, _ := exec.LookPath("sing-box")
	if binary == "" {
		t.Log("sing-box unavailable, exercising configuration generation with the test runner")
	}
	check := func(ctx context.Context, path string) ([]byte, error) {
		return checkMonitoringConfig(ctx, binary, path)
	}
	f.runner.check = check
	now := new(atomic.Int64)
	now.Store(time.Now().Unix())
	clock := func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	alertService := alerts.New(f.conn, nil, alerts.Options{Now: clock})
	must(db.SetSetting(ctx, f.conn, db.AlertChannels, map[string]db.AlertChannel{
		alerts.Bark: {Enabled: true, Credential: "test-key"},
	}))
	_, err := f.conn.Exec("UPDATE servers SET version='v0.5.0' WHERE is_local=1")
	must(err)
	newMaster := func() *agent.Master {
		m := agent.NewMaster(f.conn, nil, clock)
		m.OnStopped, m.OnAvailability = alertService.UserStopped, alertService.ServerAvailability
		return m
	}
	master := newMaster()
	authService, err := auth.New(f.conn, nil)
	must(err)
	logger := slog.New(slog.NewTextHandler(f.logs, nil))
	opts := Options{DB: f.conn, Agents: master, Applier: f.apply, Subscriptions: store, Alerts: alertService, PanelPort: 2053, Logger: logger}
	var handler atomic.Value
	handler.Store(New(authService, opts))
	f.h = handler.Load().(http.Handler)
	var lostAck, missingEndpoint atomic.Bool
	var sampleUploads atomic.Int64
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := handler.Load().(http.Handler)
		if r.URL.Path == agent.InterfaceSamplesPath {
			sampleUploads.Add(1)
			if missingEndpoint.Load() {
				http.NotFound(w, r)
				return
			}
			if lostAck.Swap(false) {
				reply := httptest.NewRecorder()
				h.ServeHTTP(reply, r)
				if reply.Code != http.StatusNoContent {
					t.Errorf("interface sample commit returned %d", reply.Code)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	defer tlsServer.Close()
	pin := certs.SPKIPin(tlsServer.Certificate().RawSubjectPublicKeyInfo)
	client, err := agent.NewClient(tlsServer.URL, pin)
	must(err)
	defer client.Close()
	edge, edgePaths := joinTestNode(t, master, client, tlsServer.URL, pin, "edge", "8.8.8.8")
	other, otherPaths := joinTestNode(t, master, client, tlsServer.URL, pin, "other", "8.8.4.4")
	edgeID, otherID := edge.Master.ServerID, other.Master.ServerID
	beat := agent.Heartbeat{Version: "v0.5.0", SingboxActive: true, Interfaces: []string{"eth0", "eth1"}}
	syncNode := func(node *agent.Node) error { return node.Sync(ctx, beat, func() bool { return true }) }
	generationZero := int64(0)
	reply, err := client.SyncHeartbeat(ctx, edgeID, edge.Key, agent.Heartbeat{Version: beat.Version, Protocol: agent.ProtocolVersion, AppliedGeneration: &generationZero})
	must(err)
	if reply.Protocol != agent.ProtocolVersion || reply.Version != beat.Version {
		t.Fatalf("unexpected master heartbeat: %+v", reply)
	}
	must(client.Heartbeat(ctx, otherID, other.Key, beat))
	f.call(t, "POST", "users", `{"name":"alice","traffic_limit":100}`, 201)

	// Create in an order different from the required subscription order.
	for _, id := range []int64{otherID, edgeID, 1} {
		f.call(t, "POST", "inbounds", fmt.Sprintf(`{"server_id":%d,"protocol":"vless_reality","port":443,"options":{"server_name":"www.example.com"}}`, id), 201)
		f.call(t, "POST", "inbounds", fmt.Sprintf(`{"server_id":%d,"protocol":"hysteria2","port":8443}`, id), 201)
	}
	edgeConfig, otherConfig := filepath.Join(edgePaths.DataDir, "sing-box.json"), filepath.Join(otherPaths.DataDir, "sing-box.json")
	edgeRunner, otherRunner := &resourceRunner{check: check}, &resourceRunner{check: check}
	for _, fixture := range []struct {
		node   *agent.Node
		config string
		runner *resourceRunner
	}{{&edge, edgeConfig, edgeRunner}, {&other, otherConfig, otherRunner}} {
		fixture.node.Manager, err = singbox.NewManager(fixture.node.Writer, singbox.ApplyOptions{
			OS: "linux", Config: fixture.config, Runner: fixture.runner, Firewall: &resourceFirewall{},
		})
		must(err)
		must(syncNode(fixture.node))
		if fixture.runner.checks == 0 {
			t.Fatalf("node %d did not validate a generated configuration", fixture.node.Master.ServerID)
		}
	}
	if f.runner.checks == 0 {
		t.Fatal("master did not validate a generated configuration")
	}
	readConfig := func(path string) []byte {
		t.Helper()
		raw, err := os.ReadFile(path)
		must(err)
		return raw
	}
	for _, config := range []string{f.config, edgeConfig, otherConfig} {
		if raw := readConfig(config); !bytes.Contains(raw, []byte("alice")) || !bytes.Contains(raw, []byte(`"private_key"`)) || !bytes.Contains(raw, []byte(`"hysteria2"`)) {
			t.Fatal("a server configuration lacks a user or protocol material")
		}
	}
	data, err := store.Load(ctx, 1, nil)
	must(err)
	public := &subscription.Handler{Store: store, Limiter: &subscription.Limiter{}, Logger: logger}
	sub := request(t, public, "GET", "https://master.test/subscribe/"+data.User.Token+"?format=base64", "", nil, nil)
	requireStatus(t, sub, 200)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sub.Body.String()))
	must(err)
	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	var got []string
	for _, uri := range lines {
		parsed, err := url.Parse(uri)
		if err != nil || parsed.Host == "" {
			t.Fatal("subscription entry is missing an endpoint")
		}
		got = append(got, parsed.Scheme+"@"+parsed.Host)
	}
	want := []string{"vless@[2001:db8::8]:443", "hysteria2@[2001:db8::8]:8443",
		"vless@8.8.8.8:443", "hysteria2@8.8.8.8:8443", "vless@8.8.4.4:443", "hysteria2@8.8.4.4:8443"}
	if !slices.Equal(got, want) {
		t.Fatalf("subscription endpoint order = %v, want %v", got, want)
	}

	settingsPath := fmt.Sprintf("alerts/settings?server_id=%d", edgeID)
	servers := func() []serverRecord {
		return decodeResult[[]serverRecord](t, f.call(t, "GET", "servers", "", 200))
	}
	initial := servers()[1]
	edgeRestarts := edgeRunner.restarts
	f.call(t, "PUT", settingsPath, `{"interface":"eth1","quota_gb":1,"billing_day":2,"billing_mode":"both"}`, 200)
	f.call(t, "PUT", settingsPath, fmt.Sprintf(`{"server_id":%d}`, otherID), 400)
	f.call(t, "GET", "alerts/settings?server_id=999", "", 404)
	f.call(t, "GET", "alerts/settings?server_id=0", "", 400)
	must(syncNode(&edge))
	if current := servers()[1]; current.DesiredGeneration != initial.DesiredGeneration || edgeRunner.restarts != edgeRestarts {
		t.Fatal("saving billing settings changed desired configuration or restarted the node")
	}
	var preference string
	must(edge.Reader.QueryRow("SELECT json_extract(alert_preferences,'$.interface') FROM servers WHERE is_local=1").Scan(&preference))
	if preference != "eth1" {
		t.Fatalf("node stored interface %q, want eth1", preference)
	}
	at := clock().Add(-time.Second).UnixNano()
	_, err = edge.Writer.Exec("INSERT INTO server_traffic_samples VALUES (1,?,'eth1',400000000,500000000)", at)
	must(err)
	_, err = other.Writer.Exec("INSERT INTO server_traffic_samples VALUES (1,?,'eth0',7,13)", at)
	must(err)
	lostAck.Store(true)
	must(syncNode(&edge))
	if _, err := db.GetSetting(ctx, edge.Reader, db.AgentInterfaceAck); err == nil {
		t.Fatal("node advanced its sample cursor after losing the HTTP response")
	}
	must(syncNode(&other))
	must(syncNode(&edge))
	ack, err := db.GetSetting(ctx, edge.Reader, db.AgentInterfaceAck)
	must(err)
	if ack != at {
		t.Fatalf("acknowledged sample timestamp = %d, want %d", ack, at)
	}
	up := sampleUploads.Load()
	restartedNode := agent.Node{Reader: edge.Reader, Writer: edge.Writer, Master: edge.Master, Key: edge.Key, Client: client, Manager: edge.Manager}
	must(syncNode(&restartedNode))
	if sampleUploads.Load() != up+1 {
		t.Fatal("restart sent more than the empty endpoint probe")
	}
	var sampleCount int
	must(f.conn.QueryRow("SELECT count(*) FROM server_traffic_samples WHERE server_id IN (?,?)", edgeID, otherID).Scan(&sampleCount))
	if sampleCount != 2 {
		t.Fatalf("master recorded %d samples after retry, want one per node", sampleCount)
	}
	remote := decodeResult[alerts.Settings](t, f.call(t, "GET", settingsPath, "", 200))
	if remote.Billing.Used != 900_000_000 || remote.Billing.Interface != "eth1" || !slices.Equal(remote.Interfaces, beat.Interfaces) {
		t.Fatalf("unexpected remote billing settings: %+v", remote)
	}
	otherSettings := decodeResult[alerts.Settings](t, f.call(t, "GET", fmt.Sprintf("alerts/settings?server_id=%d", otherID), "", 200))
	if otherSettings.Billing.Used != 13 {
		t.Fatalf("other node's independent usage = %d, want 13", otherSettings.Billing.Used)
	}
	_, err = other.Writer.Exec(`WITH RECURSIVE samples(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM samples WHERE i<1001)
		INSERT INTO server_traffic_samples SELECT 1,?+i,'eth0',0,0 FROM samples`, at)
	must(err)
	up = sampleUploads.Load()
	must(syncNode(&other))
	if sampleUploads.Load() != up+2 {
		t.Fatal("the backlog did not upload in two batches")
	}
	ack, err = db.GetSetting(ctx, other.Reader, db.AgentInterfaceAck)
	must(err)
	if ack != at+1001 {
		t.Fatal("the batch cursor did not reach the last acknowledged sample")
	}

	_, err = edge.Writer.Exec("UPDATE users SET traffic_up=40 WHERE id=1")
	must(err)
	_, err = other.Writer.Exec("UPDATE users SET traffic_down=60 WHERE id=1")
	must(err)
	must(syncNode(&edge))
	must(syncNode(&other))
	must(syncNode(&edge))
	for _, path := range []string{edgeConfig, otherConfig} {
		if bytes.Contains(readConfig(path), []byte("alice")) {
			t.Fatal("exhausted user remains in a node configuration")
		}
	}
	var userUp, userDown int64
	must(f.conn.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=1").Scan(&userUp, &userDown))
	if userUp != 40 || userDown != 60 {
		t.Fatalf("master combined traffic = %d/%d, want 40/60", userUp, userDown)
	}

	now.Add(80)
	must(syncNode(&other))
	now.Add(15)
	must(master.MarkOffline(ctx))
	opts.Agents = newMaster()
	f.h = New(authService, opts)
	handler.Store(f.h)
	must(opts.Agents.MarkOffline(ctx))
	if s := servers()[1]; s.Status != "offline" {
		t.Fatalf("edge did not go offline: %+v", s)
	}
	must(syncNode(&edge))
	must(syncNode(&edge))
	overview := decodeResult[alerts.Overview](t, f.call(t, "GET", "alerts", "", 200))
	var availability []alerts.Event
	for _, e := range overview.Unread {
		if e.Kind == "server_offline" || e.Kind == "server_recovered" {
			availability = append(availability, e)
		}
	}
	if len(availability) != 2 {
		t.Fatalf("expected one outage and recovery across master replacement: %+v", availability)
	}
	for _, event := range availability {
		if event.ServerID != edgeID || event.Address != "8.8.8.8" || event.Kind == "server_recovered" && event.OfflineSeconds != 95 {
			t.Fatalf("unexpected availability event: %+v", event)
		}
		f.call(t, "POST", "alerts/events/"+event.Key+"/read", "", 204)
	}
	var notifications int
	must(f.conn.QueryRow(`SELECT count(*) FROM alert_outbox o JOIN alert_events e ON e.event_key=o.event_key
		WHERE e.kind IN ('server_offline','server_recovered')`).Scan(&notifications))
	if notifications != 2 {
		t.Fatalf("availability outbox has %d entries, want 2", notifications)
	}

	// A new node connected to a protocol-2 master missing the new endpoint
	// reports the compatibility error before applying a different snapshot.
	before := readConfig(edgeConfig)
	missingEndpoint.Store(true)
	_, err = edge.Writer.Exec("INSERT INTO server_traffic_samples VALUES (1,?,'eth1',1,1)", clock().UnixNano())
	must(err)
	f.call(t, "PUT", "inbounds/3", `{"port":444}`, 200)
	if err := syncNode(&edge); !errors.Is(err, agent.ErrMasterOutdated) {
		t.Fatalf("missing samples endpoint: %v", err)
	}
	if !bytes.Equal(before, readConfig(edgeConfig)) || servers()[1].SyncError != agent.SyncMasterOutdated {
		t.Fatal("missing endpoint did not hold the last applied config or report its error")
	}
	missingEndpoint.Store(false)
	must(syncNode(&edge))
	if bytes.Equal(before, readConfig(edgeConfig)) || servers()[1].SyncError != "" {
		t.Fatal("restored master endpoint did not resume synchronization")
	}

	must(client.Heartbeat(ctx, edgeID, edge.Key, agent.Heartbeat{Version: beat.Version, Protocol: 1}))
	if _, err := client.Desired(ctx, edgeID, edge.Key); !errors.Is(err, agent.ErrAgentUpgradeRequired) {
		t.Fatalf("old protocol desired response: %v", err)
	}
	upgrade := signedAgentRequest(t, f.h, "POST", agent.TrafficPath, `{"counters":[]}`, edgeID, edge.Key, now.Load())
	requireStatus(t, upgrade, 426)
	if !strings.Contains(upgrade.Body.String(), "agent_upgrade_required") {
		t.Fatal("missing upgrade response code")
	}
	if s := servers()[1]; !s.UpgradeRequired || s.Protocol != 1 || s.Status != "online" || s.SyncStatus != "failed" {
		t.Fatalf("missing compatibility fields: %+v", s)
	}
}

func checkMonitoringConfig(ctx context.Context, binary, path string) ([]byte, error) {
	if binary == "" {
		return nil, nil
	}
	if runtime.GOOS != "linux" {
		// Confgen emits Linux socket marks. The host core only checks them on Linux.
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var doc map[string]json.RawMessage
		var outbounds []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(doc["outbounds"], &outbounds); err != nil {
			return nil, err
		}
		for _, outbound := range outbounds {
			delete(outbound, "routing_mark")
		}
		doc["outbounds"], err = json.Marshal(outbounds)
		if err != nil {
			return nil, err
		}
		raw, err = json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		path += ".host-check"
		if err := os.WriteFile(path, raw, 0600); err != nil {
			return nil, err
		}
		defer os.Remove(path)
	}
	return exec.CommandContext(ctx, binary, "check", "-c", path).CombinedOutput()
}

func TestInterfaceSampleValidation(t *testing.T) {
	h, m, conn, now := agentAPI(t)
	_, token, err := m.IssueToken(t.Context(), "edge")
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Register(t.Context(), agent.Registration{Token: token, PublicKey: base64.RawURLEncoding.EncodeToString(public), Hostname: "edge", Version: "test", Protocol: 2}, "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	path := "/panel" + agent.InterfaceSamplesPath
	send := func(path, body string, want int) {
		t.Helper()
		requireStatus(t, signedAgentRequest(t, h, "POST", path, body, id.ServerID, key, now.Load()), want)
	}
	at := time.Unix(now.Load(), 0)
	sample := fmt.Sprintf(`{"sampled_at":%d,"interface":"eth0","rx":205,"tx":309}`, at.UnixNano())
	ok := `{"samples":[` + sample + "]}"
	requireStatus(t, request(t, h, "POST", "https://master.test"+path, ok, nil, nil), 401)
	for _, body := range []string{`{"samples":null}`, `{}`, `{"samples":[` + sample + `],"server_id":3}`,
		strings.Replace(ok, "eth0", "lo", 1), strings.Replace(ok, "eth0", "x y", 1), strings.Replace(ok, "205", "-205", 1),
		strings.Replace(ok, strconv.FormatInt(at.UnixNano(), 10), strconv.FormatInt(at.Add(time.Hour).UnixNano(), 10), 1),
		`{"samples":[` + strings.Repeat(sample+",", agent.MaxInterfaceSamples) + sample + "]}", ok + ` {}`} {
		send(path, body, 400)
	}
	send(path, ok+strings.Repeat(" ", agent.MaxInterfaceSamplesBody), 413)
	if _, err := conn.Exec(`INSERT INTO server_traffic_samples VALUES (? ,?,'eth0',0,0)`, id.ServerID, at.AddDate(0, 0, -63).UnixNano()); err != nil {
		t.Fatal(err)
	}
	send(path, `{"samples":[`+strings.Repeat(sample+",", agent.MaxInterfaceSamples-1)+sample+"]}", 204)
	send(path, strings.Replace(ok, "309", "999", 1), 204)
	send(path, strings.Replace(ok, strconv.FormatInt(at.UnixNano(), 10), strconv.FormatInt(at.AddDate(0, 0, -63).UnixNano(), 10), 1), 204)
	var count, tx int
	if err := conn.QueryRow("SELECT count(*),coalesce(sum(tx),0) FROM server_traffic_samples WHERE server_id=?", id.ServerID).Scan(&count, &tx); err != nil || count != 1 || tx != 309 {
		t.Fatalf("samples after replay/stale report = %d/%d: %v", count, tx, err)
	}
	heartbeat := "/panel" + agent.Prefix + "/heartbeat"
	send(heartbeat, `{"version":"test","protocol":2,"interfaces":["eth0","eth0"]}`, 400)
	send(heartbeat, `{"version":"test","protocol":2,"interfaces":["eth/0"]}`, 400)
	var names []string
	for i := range 33 {
		names = append(names, fmt.Sprintf("eth%d", i))
	}
	beat, err := json.Marshal(agent.Heartbeat{Version: "test", Protocol: 2, Interfaces: names})
	if err != nil {
		t.Fatal(err)
	}
	send(heartbeat, string(beat), 400)
	send(heartbeat, `{"version":"test","protocol":1}`, 200)
	send(path, ok, 426)
	send("/panel"+agent.Prefix+"/inbound-keys", `{"inbound_id":1,"key_id":"old"}`, 426)
	if err := m.Remove(t.Context(), id.ServerID); err != nil {
		t.Fatal(err)
	}
	send(path, ok, 401)
}

func signedAgentRequest(t *testing.T, h http.Handler, method, path, body string, id int64, key ed25519.PrivateKey, timestamp int64) *httptest.ResponseRecorder {
	t.Helper()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(random)
	sent := strconv.FormatInt(timestamp, 10)
	message := fmt.Appendf(nil, "%s\n%s\n%s\n%s\n%x", method, path, sent, nonce, sha256.Sum256([]byte(body)))
	return request(t, h, method, "https://master.test"+path, body, nil, map[string]string{
		agent.HeaderServer: strconv.FormatInt(id, 10), agent.HeaderTimestamp: sent, agent.HeaderNonce: nonce,
		agent.HeaderSignature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message)),
	})
}
