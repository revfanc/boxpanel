package httpapi

import (
	"context"
	"fmt"
	"io"
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
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/traffic"
)

func TestRemoteTrafficLifecycle(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f := newResources(t, false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	master := agent.NewMaster(f.conn, nil, nil)
	master.OnStopped = alerts.New(f.conn, nil, alerts.Options{}).UserStopped
	must(db.SetSetting(ctx, f.conn, db.AlertChannels, map[string]db.AlertChannel{
		alerts.Bark: {Enabled: true, Credential: "test-key"},
	}))
	service, err := auth.New(f.conn, nil)
	must(err)
	f.h = New(service, Options{DB: f.conn, Agents: master, Applier: f.apply, PanelPort: 2053, Logger: logger})
	var disconnected, lostAck atomic.Bool
	var uploads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if disconnected.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == agent.TrafficPath {
			uploads.Add(1)
			if lostAck.Swap(false) {
				// Commit at the master, but lose the successful response.
				reply := httptest.NewRecorder()
				f.h.ServeHTTP(reply, r)
				if reply.Code != 204 {
					t.Errorf("traffic reply before losing acknowledgement = %d", reply.Code)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		f.h.ServeHTTP(w, r)
	}))
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	client, err := agent.NewClient(server.URL, pin)
	must(err)
	defer client.Close()
	edge, edgePaths := joinTestNode(t, master, client, server.URL, pin, "edge", "8.8.8.8")
	other, otherPaths := joinTestNode(t, master, client, server.URL, pin, "other", "8.8.4.4")
	beat := agent.Heartbeat{Version: "test", SingboxActive: true}
	f.call(t, "POST", "users", `{"name":"alice","traffic_limit":100,"traffic_reset_day":1}`, 201)
	config := func(filename string) string {
		t.Helper()
		raw, err := os.ReadFile(filename)
		must(err)
		return string(raw)
	}
	sync := func(node *agent.Node) error { return node.Sync(ctx, beat, func() bool { return true }) }
	edgeConfig := filepath.Join(edgePaths.DataDir, "sing-box.json")
	otherConfig := filepath.Join(otherPaths.DataDir, "sing-box.json")
	for _, fixture := range []struct {
		node   *agent.Node
		config string
	}{{&edge, edgeConfig}, {&other, otherConfig}} {
		node := fixture.node
		must(client.Heartbeat(ctx, node.Master.ServerID, node.Key, beat))
		f.call(t, "POST", "inbounds", fmt.Sprintf(`{"server_id":%d,"protocol":"vless_reality","port":443,"options":{"server_name":"www.example.com"}}`, node.Master.ServerID), 201)
		node.Manager, err = singbox.NewManager(node.Writer, singbox.ApplyOptions{
			OS: "linux", Config: fixture.config, Runner: &resourceRunner{}, Firewall: &resourceFirewall{},
		})
		must(err)
		must(sync(node))
		if !strings.Contains(config(fixture.config), "alice") {
			t.Fatal("initial node config is missing alice")
		}
	}
	var sample int64
	fetch := func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{{UserID: 1, Download: sample}}, nil
	}
	edge.Accounting = traffic.NewNodeCollector(edge.Writer, edge.Reader, fetch, "test-boot")
	must(edge.Accounting.Poll(ctx))
	usage := func(want int64) {
		t.Helper()
		var got int64
		must(f.conn.QueryRow("SELECT traffic_down FROM users WHERE id=1").Scan(&got))
		if got != want {
			t.Fatalf("master download = %d, want %d", got, want)
		}
	}
	alertCounts := func(want int) {
		t.Helper()
		for _, kind := range []string{"user_stopped", "user_quota_80", "user_quota_100"} {
			var events, queued int
			must(f.conn.QueryRow(`SELECT count(*),count(o.id) FROM alert_events e
				LEFT JOIN alert_outbox o ON o.event_key=e.event_key WHERE e.kind=?`, kind).Scan(&events, &queued))
			if events != want || queued != want {
				t.Fatalf("%s recorded %d events and %d notifications, want %d each", kind, events, queued, want)
			}
		}
	}
	generation := func(node *agent.Node) int64 {
		t.Helper()
		var value int64
		must(f.conn.QueryRow("SELECT desired_generation FROM servers WHERE id=?", node.Master.ServerID).Scan(&value))
		return value
	}
	edgeGen, otherGen := generation(&edge), generation(&other)
	disconnected.Store(true)
	sample = 40
	must(edge.Accounting.Poll(ctx))
	must(edge.Accounting.Flush(ctx))
	if err := sync(&edge); err == nil || !strings.Contains(config(edgeConfig), "alice") {
		t.Fatal("master unavailability changed the active node config or went unnoticed")
	}
	usage(0)
	lostAck.Store(true)
	disconnected.Store(false)
	must(sync(&edge))
	usage(40)
	must(sync(&edge))
	usage(40)
	must(sync(&edge))
	if uploads.Load() != 2 || generation(&edge) != edgeGen || generation(&other) != otherGen {
		t.Fatal("offline recovery, unchanged snapshot suppression or below-quota generation failed")
	}
	alertCounts(0)
	// The accounting process can also stop while the proxy keeps serving.
	sample = 70
	edge.Accounting = traffic.NewNodeCollector(edge.Writer, edge.Reader, fetch, "test-boot")
	must(edge.Accounting.Poll(ctx))
	must(sync(&edge))
	usage(40) // The restarted collector has sampled, but not flushed.
	must(edge.Accounting.Flush(ctx))
	must(sync(&edge))
	usage(70)

	sample = 100
	must(edge.Accounting.Poll(ctx))
	must(edge.Accounting.Flush(ctx))
	sample = 106 // Sampled inside ApplyMirror, after the upload's database snapshot.
	must(sync(&edge))
	usage(100)
	alertCounts(1) // The master has no local inbounds or locally applied users.
	if generation(&edge) != edgeGen+1 || generation(&other) != otherGen+1 {
		t.Fatal("aggregate quota crossing did not invalidate both nodes")
	}
	must(sync(&other))
	if strings.Contains(config(edgeConfig), "alice") || strings.Contains(config(otherConfig), "alice") {
		t.Fatal("exhausted user stayed in a node's active configuration")
	}
	var retired int
	must(edge.Reader.QueryRow("SELECT count(*) FROM agent_retired_traffic WHERE user_id=1 AND down=106").Scan(&retired))
	if retired != 1 {
		t.Fatal("last unflushed bytes were lost while removing the mirror")
	}
	lostAck.Store(true)
	must(sync(&edge))
	usage(106)
	// A new heartbeat loop has no in-memory acknowledgements.
	edge = agent.Node{Reader: edge.Reader, Writer: edge.Writer, Master: edge.Master, Key: edge.Key,
		Client: edge.Client, Manager: edge.Manager, Accounting: edge.Accounting}
	must(sync(&edge))
	usage(106)
	alertCounts(1)
	must(edge.Reader.QueryRow("SELECT count(*) FROM agent_retired_traffic").Scan(&retired))
	if retired != 0 {
		t.Fatal("acknowledged retired counters were not removed")
	}

	// Monthly reset restores both mirrors. The next quota stop exercises the
	// original local scheduler now that the master also serves the user.
	scheduler := traffic.NewScheduler(traffic.NewCollector(f.conn, f.conn, nil), f.apply,
		func() time.Time { return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) }, logger)
	scheduler.OnStopped = master.OnStopped
	must(scheduler.Check(ctx))
	usage(0)
	must(sync(&edge))
	must(sync(&other))
	if !strings.Contains(config(edgeConfig), "alice") || !strings.Contains(config(otherConfig), "alice") {
		t.Fatal("monthly reset did not restore alice on all nodes")
	}
	f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","port":443,"options":{"server_name":"www.example.com"}}`, 201)
	sample = 100
	must(edge.Accounting.Poll(ctx))
	must(edge.Accounting.Flush(ctx))
	must(sync(&edge))
	usage(100)
	alertCounts(1)
	must(scheduler.Check(ctx))
	must(scheduler.Check(ctx))
	alertCounts(2)
	if strings.Contains(config(f.config), "alice") {
		t.Fatal("local scheduler did not apply the aggregate quota stop")
	}
	rows := decodeResult[[]serverRecord](t, f.call(t, "GET", "servers", "", 200))
	if rows[1].TrafficDown == nil || *rows[1].TrafficDown != 206 {
		t.Fatal("the server total was reset with the user cycle")
	}

	f.call(t, "DELETE", "users/1", "", 204)
	newUser := decodeResult[userView](t, f.call(t, "POST", "users", `{"name":"newuser"}`, 201))
	if newUser.ID <= 1 {
		t.Fatal("master reused a deleted ID whose node reports could still be in flight")
	}
	must(client.ReportTraffic(ctx, edge.Master.ServerID, edge.Key, agent.TrafficReport{
		Counters: []agent.TrafficCounter{{UserID: 1, Epoch: 3, Down: 300}},
	}))
	newUser = decodeResult[userView](t, f.call(t, "GET", fmt.Sprintf("users/%d", newUser.ID), "", 200))
	if newUser.TrafficDown != 0 {
		t.Fatal("deleted user's late report was applied to a different account")
	}
}
