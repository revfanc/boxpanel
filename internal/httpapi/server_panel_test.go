package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func newServerPanelTest(t *testing.T) (*resourceFixture, *agent.Master, *atomic.Int64) {
	t.Helper()
	f := newResources(t, false)
	now := new(atomic.Int64)
	now.Store(time.Now().Unix())
	clock := func() time.Time { return time.Unix(now.Load(), 0) }
	master := agent.NewMaster(f.conn, nil, clock)
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h = New(service, Options{DB: f.conn, Agents: master, Applier: f.apply, Singbox: f.apply.manager,
		Alerts: alerts.New(f.conn, nil, alerts.Options{Now: clock, Network: &alerts.Network{Files: host.Native{}}}),
		System: &fakeSystem{}, Logger: slog.New(slog.NewTextHandler(f.logs, nil))})
	return f, master, now
}

func panelNodeClient(t *testing.T, handler http.Handler) (*agent.Client, string, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	client, err := agent.NewClient(server.URL, pin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, server.URL, pin
}

func panelHeartbeat() agent.Heartbeat {
	status := sysinfo.Status{Supported: true, CPU: 12.34, Uptime: 86400,
		Mem: sysinfo.Usage{Used: 256_000_001, Total: 1024_000_002}, Disk: sysinfo.Usage{Used: 1000_000_000, Total: 8000_000_000},
		Net: sysinfo.Rates{RX: 128.4, TX: 512.3}, Singbox: sysinfo.Singbox{Installed: true, Running: true, Version: "1.14.4", Uptime: 122}}
	return agent.Heartbeat{Version: "test", Protocol: agent.ProtocolVersion, Uptime: 222, SingboxActive: true,
		Metrics: agent.NewMetrics(sysinfo.Monitoring{Metrics: status.Metrics(), Core: &status.Singbox})}
}

type serverPanelJournal struct {
	resourceRunner
	delta   bool
	cursors []string
}

func (j *serverPanelJournal) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "journalctl" || !slices.Contains(args, "--output=json") {
		return j.resourceRunner.Run(ctx, name, args...)
	}
	if !slices.Contains(args, "--lines=200") {
		return nil, fmt.Errorf("missing journal limit")
	}
	cursor := ""
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--after-cursor="); ok {
			cursor = value
		}
	}
	j.cursors = append(j.cursors, cursor)
	var buf bytes.Buffer
	record := func(id, message string) {
		_ = json.NewEncoder(&buf).Encode(map[string]string{"__CURSOR": id, "MESSAGE": message})
	}
	if cursor == "" {
		for i := range 199 {
			record(fmt.Sprint(i), fmt.Sprintf("line %d", i))
		}
		record("head", "connection failed with old-pass\npassword=my-hidden-password")
	} else if cursor == "head" && j.delta {
		record("delta", "latest line")
	}
	return buf.Bytes(), nil
}

func TestServerPanelHeartbeatAndLeasedLogs(t *testing.T) {
	ctx := t.Context()
	f, master, now := newServerPanelTest(t)
	var uploads atomic.Int64
	var loseAck atomic.Bool
	client, url, pin := panelNodeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agent.LogsPath {
			uploads.Add(1)
			if loseAck.Swap(false) {
				record := httptest.NewRecorder()
				f.h.ServeHTTP(record, r)
				if record.Code != 204 {
					t.Errorf("log batch commit returned %d", record.Code)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		f.h.ServeHTTP(w, r)
	}))
	edge, paths := joinTestNode(t, master, client, url, pin, "edge", "8.8.8.8")
	other, _ := joinTestNode(t, master, client, url, pin, "other", "8.8.4.4")
	if err := client.Heartbeat(ctx, other.Master.ServerID, other.Key, panelHeartbeat()); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("servers/%d/", edge.Master.ServerID)
	status := func() serverStatusView {
		t.Helper()
		return decodeResult[serverStatusView](t, f.call(t, "GET", path+"status", "", 200))
	}
	view := func() agent.LogView {
		t.Helper()
		return decodeResult[agent.LogView](t, f.call(t, "GET", path+"logs", "", 200))
	}
	logsA := path + "logs/01010101010101010101010101010101"
	logsB := path + "logs/02020202020202020202020202020202"
	legacy := agent.Heartbeat{Version: "old-v2", Protocol: agent.ProtocolVersion, SingboxActive: true}
	reply, err := client.SyncHeartbeat(ctx, edge.Master.ServerID, edge.Key, legacy)
	previous := status()
	if err != nil || reply.Protocol != agent.ProtocolVersion || reply.LogRequest != nil ||
		previous.Metrics != nil || previous.SingboxActive == nil || !*previous.SingboxActive {
		t.Fatalf("optional heartbeat monitoring changed legacy behavior: %+v, %v", reply, err)
	}
	f.call(t, "PUT", logsA, "", 409)
	f.call(t, "POST", path+"restart", "", 409)
	beat := panelHeartbeat()
	legacyBody, _ := json.Marshal(legacy)
	metricsOnly := legacy
	metricsOnly.Metrics = beat.Metrics
	metricsBody, _ := json.Marshal(metricsOnly)
	if added := len(metricsBody) - len(legacyBody); added > 154 {
		t.Fatalf("monitoring exceeds 20 MB/month at the normal heartbeat interval: %d bytes", added)
	}
	journal := &serverPanelJournal{}
	config := filepath.Join(paths.DataDir, "sing-box.json")
	manager, err := singbox.NewManager(edge.Writer, singbox.ApplyOptions{OS: "linux", Config: config, Runner: journal, Firewall: &resourceFirewall{}})
	if err != nil {
		t.Fatal(err)
	}
	edge.ReadLogs, edge.Manager = manager.LogBatch, manager
	sync := func(interval time.Duration) {
		t.Helper()
		if err := edge.Sync(ctx, beat, func() bool { return true }); err != nil {
			t.Fatal(err)
		}
		if got := edge.HeartbeatInterval(); got != interval {
			t.Fatalf("heartbeat interval got %v, want %v", got, interval)
		}
	}
	sync(agent.SyncInterval)
	if uploads.Load() != 0 || view().Ready {
		t.Fatal("logs uploaded before a viewer opened them")
	}
	if got := status(); got.Metrics == nil || got.Metrics.CPU != 12.3 || got.Metrics.Mem.Used != 256 || got.Metrics.Disk.Total != 8000 ||
		got.Metrics.Net.TX != 512 || got.MetricsAt == nil || *got.MetricsAt != now.Load() ||
		got.Core == nil || got.Core.Version != "1.14.4" {
		t.Fatalf("bad monitored remote status: %+v", got)
	}
	beat.Metrics.CPU = 41.5
	now.Add(1)
	sync(agent.SyncInterval)
	if got := status(); got.Metrics.CPU != 41.5 || *got.MetricsAt != now.Load() {
		t.Fatal("remote status did not replace its previous sample")
	}
	if err := os.WriteFile(config+".bak", []byte(`{"password":"old-pass"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const panelURL = "https://example.test/api/v1/"
	requireStatus(t, request(t, f.h, "PUT", panelURL+logsA, "", f.cookie, map[string]string{"Origin": "https://elsewhere.test"}), 403)
	f.call(t, "PUT", logsA, "", 204)
	if got := view(); got.Ready || len(got.Lines) != 0 {
		t.Fatal("logging appeared ready before the first upload")
	}
	reply, err = client.SyncHeartbeat(ctx, edge.Master.ServerID, edge.Key, beat)
	if err != nil || reply.LogRequest == nil || reply.LogRequest.TTLMS != 60_000 {
		t.Fatalf("log request missing from heartbeat: %+v, %v", reply, err)
	}
	batchID, err := auth.RandomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	foreignReport := agent.LogReport{ID: reply.LogRequest.ID, BatchID: batchID, Snapshot: true, Lines: []string{"foreign"}}
	if err := client.ReportLogs(ctx, other.Master.ServerID, other.Key, foreignReport); !errors.Is(err, agent.ErrLogInactive) {
		t.Fatal("foreign node could submit to the active log stream", err)
	}
	if err := client.ReportLogs(ctx, other.Master.ServerID, edge.Key, foreignReport); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatal("foreign signed identity accepted", err)
	}
	sync(3 * time.Second)
	initial := view()
	if !initial.Ready || len(initial.Lines) != agent.LogLines || initial.Lines[0] != "line 1" ||
		initial.Lines[len(initial.Lines)-2] != "connection failed with [REDACTED]" ||
		strings.Contains(strings.Join(initial.Lines, "\n"), "my-hidden-password") {
		t.Fatal("initial journal was not bounded and redacted")
	}
	journal.delta = true
	loseAck.Store(true)
	sync(3 * time.Second)
	sync(3 * time.Second)
	sync(3 * time.Second)
	got := view()
	if got.Lines[len(got.Lines)-1] != "latest line" || strings.Count(strings.Join(got.Lines, "\n"), "latest line") != 1 ||
		!slices.Equal(journal.cursors, []string{"", "head", "delta"}) || uploads.Load() != 5 {
		t.Fatalf("log cursor or acknowledgement replay is incorrect: %v, %d", journal.cursors, uploads.Load())
	}
	now.Add(45)
	f.call(t, "PUT", logsB, "", 204)
	f.call(t, "DELETE", logsA, "", 204)
	sync(3 * time.Second)
	f.call(t, "DELETE", logsB, "", 204)
	sync(agent.SyncInterval)
	if got := view(); got.Ready || len(got.Lines) != 0 {
		t.Fatal("last viewer left but logs remained available")
	}
	f.call(t, "PUT", logsA, "", 204)
	sync(3 * time.Second)
	now.Add(10)
	f.call(t, "PUT", logsA, "", 204)
	now.Add(50)
	journal.delta = false
	sync(3 * time.Second) // The initial lease would already have expired.
	now.Add(10)
	sync(agent.SyncInterval)
	if view().Ready || uploads.Load() != 6 {
		t.Fatal("expired viewer caused stale logs or renewed uploads")
	}
	if err := client.Heartbeat(ctx, edge.Master.ServerID, edge.Key, legacy); err != nil || status().Metrics != nil {
		t.Fatal("missing metrics after a downgrade kept an obsolete sample", err)
	}
}

func TestServerPanelRestartIsDurableAndUsesFullApply(t *testing.T) {
	ctx := t.Context()
	f, master, _ := newServerPanelTest(t)
	var dropResult atomic.Bool
	client, url, pin := panelNodeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agent.Prefix+"/heartbeat" && dropResult.Load() {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"restart_result"`)) && dropResult.Swap(false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		f.h.ServeHTTP(w, r)
	}))
	edge, paths := joinTestNode(t, master, client, url, pin, "edge", "8.8.8.8")
	runner := &resourceRunner{}
	manager, err := singbox.NewManager(edge.Writer, singbox.ApplyOptions{OS: "linux", Config: filepath.Join(paths.DataDir, "sing-box.json"), Runner: runner, Firewall: &resourceFirewall{}})
	if err != nil {
		t.Fatal(err)
	}
	edge.Manager = manager
	beat := panelHeartbeat()
	sync := func(node *agent.Node) error { return node.Sync(ctx, beat, func() bool { return true }) }
	if err := sync(&edge); err != nil {
		t.Fatal(err)
	}
	before, checks := runner.restarts, runner.checks
	const restartPath = "servers/2/restart"
	state := func() agent.RestartState {
		t.Helper()
		result := decodeResult[serverStatusView](t, f.call(t, "GET", "servers/2/status", "", 200)).Restart
		if result == nil {
			t.Fatal("missing restart status")
		}
		return *result
	}
	queue := func() agent.RestartState {
		t.Helper()
		return decodeResult[agent.RestartState](t, f.call(t, "POST", restartPath, "", 202))
	}
	requireStatus(t, request(t, f.h, "POST", "https://example.test/api/v1/"+restartPath, "", nil, nil), 401)
	first := queue()
	if first.ID == "" || first != queue() {
		t.Fatal("duplicate administrator requests replaced a pending restart")
	}
	desired, err := client.Desired(ctx, 2, edge.Key)
	applied, _ := db.GetSetting(ctx, edge.Reader, db.AgentAppliedGeneration)
	if err != nil || desired.RestartID != first.ID || desired.Generation != applied {
		t.Fatal("one-shot command did not travel with unchanged desired generation", err)
	}
	dropResult.Store(true)
	if err := sync(&edge); err == nil || runner.restarts != before+1 || runner.checks != checks+1 || state().Status != "pending" {
		t.Fatalf("explicit restart did not enter full Apply or did not preserve pending state: %v", err)
	}
	fresh := agent.Node{Reader: edge.Reader, Writer: edge.Writer, Master: edge.Master, Key: edge.Key, Client: client, Manager: manager}
	if err := sync(&fresh); err != nil || runner.restarts != before+1 || state().Status != "succeeded" {
		t.Fatal("service restart or lost heartbeat result repeated the kernel restart", err)
	}
	second := queue()
	if second.ID == first.ID {
		t.Fatal("a new action reused a completed command")
	}
	runner.failCheck = true
	if err := sync(&fresh); err != nil || runner.restarts != before+1 || state().Error != agent.SyncCheckFailed || state().Status != "failed" {
		t.Fatal("failed check did not stop and report the restart", err)
	}
	late := beat
	late.RestartResult = &agent.RestartResult{ID: first.ID}
	if _, err := client.SyncHeartbeat(ctx, 2, edge.Key, late); err != nil || state().ID != second.ID || state().Status != "failed" {
		t.Fatal("stale result overwrote the current command", err)
	}
	interrupted := queue()
	if _, err := fresh.Writer.Exec("INSERT INTO agent_restart_commands (id) VALUES (?)", interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if err := sync(&fresh); err != nil || state().Error != "interrupted" || runner.restarts != before+1 {
		t.Fatal("an interrupted command was retried", err)
	}
	f.call(t, "POST", "servers/1/restart", "", 204)
	if f.runner.checks != 1 || f.runner.restarts != 1 {
		t.Fatal("unified local restart bypassed the apply path")
	}
}

func TestServerPanelUsageMatchesUserTotals(t *testing.T) {
	f, _, now := newServerPanelTest(t)
	now.Store(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC).Unix())
	for _, name := range []string{"alice", "bob"} {
		f.call(t, "POST", "users", fmt.Sprintf(`{"name":%q}`, name), 201)
	}
	if _, err := f.conn.Exec(`INSERT INTO servers (id,name,public_host,status) VALUES
		(2,'edge','edge.example','online'),(3,'removed','removed.example','removed');
		UPDATE users SET traffic_epoch=5,traffic_up=400,traffic_down=600,traffic_limit=900 WHERE id=1;
		UPDATE users SET traffic_epoch=5,traffic_up=100,traffic_down=150,expire_at=unixepoch()+3600 WHERE id=2;
		INSERT INTO server_user_traffic (server_id,user_id,epoch,up,down,total_up,total_down,updated_at,user_epoch,cycle_up,cycle_down)
		VALUES (2,1,10,300,400,1000,2000,123,5,300,400),
		(3,1,10,0,100,500,400,123,5,0,100),(2,2,10,75,100,75,100,123,4,75,100);
		INSERT INTO server_traffic_samples (server_id,sampled_at,interface,rx,tx)
		VALUES (1,?,'eth0',600000000,1500000000),(2,?,'eth1',400000000,200000000);
		INSERT INTO alert_events (event_key,server_id,kind,address,username,used,limit_bytes,threshold,deadline,created_at) VALUES
		('t95',2,'server_offline','edge.example','',0,0,0,0,unixepoch())`, (now.Load()-1)*1e9, (now.Load()-1)*1e9); err != nil {
		t.Fatal(err)
	}
	var combined cycleBytes
	for _, id := range []string{"1", "2"} {
		user := decodeResult[userView](t, f.call(t, "GET", "users/"+id, "", 200))
		combined.add(cycleBytes{user.TrafficUp, user.TrafficDown})
	}
	load := func(id int) serverTrafficView {
		t.Helper()
		return decodeResult[serverTrafficView](t, f.call(t, "GET", fmt.Sprintf("servers/%d/traffic", id), "", 200))
	}
	for id := 1; id <= 2; id++ {
		path := fmt.Sprintf("servers/%d/alerts", id)
		prefs := decodeResult[alerts.ServerPreferences](t, f.call(t, "GET", path, "", 200))
		if prefs.Config.Events["server_quota_90"] != (id == 1) || prefs.Interfaces == nil {
			t.Fatalf("unexpected migrated alert settings for %d: %+v", id, prefs)
		}
		f.call(t, "PUT", path, `{"quota_gb":2,"billing_day":1,"billing_mode":"both","events":{"server_quota_90":true}}`, 200)
		prefs = decodeResult[alerts.ServerPreferences](t, f.call(t, "GET", path, "", 200))
		if prefs.Config.Mode != "both" || prefs.Config.QuotaGB != 2 || !prefs.Config.Events["server_quota_90"] {
			t.Fatal("server preferences did not round trip")
		}
	}
	overview := decodeResult[panelOverview](t, f.call(t, "GET", "overview", "", 200))
	local, remote := load(1), load(2)
	if len(overview.Servers) != 2 || combined != overview.UserTraffic || combined != (cycleBytes{500, 750}) ||
		local.UserTraffic != (cycleBytes{200, 350}) || remote.UserTraffic != (cycleBytes{300, 400}) ||
		local.Billing.Used != 2100000000 || remote.Billing.Used != 600000000 ||
		local.Billing.Percent != 105 || remote.Billing.Percent != 30 || len(local.Users) != 2 || len(remote.Users) != 1 ||
		overview.Servers[0].UserTraffic != local.UserTraffic || overview.Servers[1].UserTraffic != remote.UserTraffic ||
		math.Abs(overview.Servers[0].Share+overview.Servers[1].Share-100) > 0.01 ||
		len(overview.TopUsers) != 2 || overview.TopUsers[0].Name != "alice" ||
		len(overview.Expiring) != 1 || overview.Expiring[0].Name != "bob" ||
		len(overview.Exhausted) != 1 || overview.Exhausted[0].Name != "alice" ||
		overview.UnreadCount != 1 || overview.Unread[0].ServerID != 2 {
		t.Fatalf("overview and per-server traffic do not reconcile: %v / %v / %v", overview, local, remote)
	}
	for _, endpoint := range []string{"status", "traffic", "alerts"} {
		one := decodeResult[map[string]json.RawMessage](t, f.call(t, "GET", "servers/1/"+endpoint, "", 200))
		two := decodeResult[map[string]json.RawMessage](t, f.call(t, "GET", "servers/2/"+endpoint, "", 200))
		if len(one) != len(two) {
			t.Fatalf("%s keys differ between local and remote", endpoint)
		}
		for key := range one {
			if _, ok := two[key]; !ok {
				t.Fatalf("%s missing field %s on remote", endpoint, key)
			}
		}
		f.call(t, "GET", "servers/3/"+endpoint, "", 404)
	}
}
