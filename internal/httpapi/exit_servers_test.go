package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func TestExitServersLifecycle(t *testing.T) {
	ctx := t.Context()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f, store, _, _ := newSubscriptionAPI(t)
	authService, err := auth.New(f.conn, nil)
	require(err)
	alertService := alerts.New(f.conn, f.conn, alerts.Options{})
	master := agent.NewMaster(f.conn, f.conn, nil)
	master.OnExitAvailability = alertService.ExitAvailability
	f.h = New(authService, Options{DB: f.conn, Applier: f.apply, Singbox: f.apply.manager, Agents: master, Alerts: alertService,
		Subscriptions: store, PanelPort: 2053, Logger: slog.New(slog.NewTextHandler(f.logs, nil)),
		ListeningPorts: func(context.Context) (map[sysinfo.Socket]bool, error) { return f.busy, nil }})
	server := httptest.NewTLSServer(f.h)
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	newClient := func() *agent.Client {
		cl, err := agent.NewClient(server.URL, pin)
		require(err)
		t.Cleanup(cl.Close)
		return cl
	}
	client := newClient()
	entry, _ := joinTestNode(t, master, client, server.URL, pin, "entry", "8.8.8.8")
	target, _ := joinTestNode(t, master, newClient(), server.URL, pin, "target", "8.8.4.4")
	legacy, _ := joinTestNode(t, master, newClient(), server.URL, pin, "legacy", "1.1.1.1")
	var entryPath = fmt.Sprintf("servers/%d/exit", entry.Master.ServerID)
	targetPath := fmt.Sprintf("servers/%d/exit", target.Master.ServerID)
	legacyPath := fmt.Sprintf("servers/%d/exit", legacy.Master.ServerID)
	beat := agent.Heartbeat{Version: "v0.5.1", Protocol: agent.ProtocolVersion, SingboxActive: true}
	legacyBeat := beat
	legacyBeat.Protocol = agent.MinNodeProtocol
	_, err = client.SyncHeartbeat(ctx, legacy.Master.ServerID, legacy.Key, legacyBeat)
	require(err)
	_, err = client.SyncHeartbeat(ctx, entry.Master.ServerID, entry.Key, beat)
	require(err)
	_, err = client.SyncHeartbeat(ctx, target.Master.ServerID, target.Key, beat)
	require(err)
	f.call(t, "PUT", "servers/1/exit-address", `{"address":"9.9.9.9"}`, 204)
	f.call(t, "PUT", "servers/1/exit-address", `{"address":"127.0.0.1"}`, 400)
	f.call(t, "PUT", entryPath, `{"exit_server_id":2}`, 400)
	f.call(t, "PUT", entryPath, `{"exit_server_id":999}`, 422)
	f.call(t, "PUT", entryPath, `{"exit_server_id":3}`, 422)
	if w := f.call(t, "PUT", entryPath, `{"exit_server_id":4}`, 422); !bytes.Contains(w.Body.Bytes(), []byte("exit_upgrade_required")) {
		t.Fatal("legacy target was not marked for upgrade", w.Body.String())
	}
	f.call(t, "PUT", legacyPath, `{"exit_server_id":1}`, 422)
	f.call(t, "PUT", entryPath, `{"exit_server_id":1,"rules":{"domains":["https://www.example.com"]}}`, 400)
	view := decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200))
	if view.Link != nil || len(view.Candidates) != 3 || view.Candidates[1].Reason != "ports_pending" || view.Candidates[2].Reason != "upgrade_required" {
		t.Fatalf("bad initial choices %+v", view.Candidates)
	}
	if decodeResult[exitServerView](t, f.call(t, "GET", legacyPath, "", 200)).Supported {
		t.Fatal("old protocol exposed as a supported entry")
	}

	// The allocator must avoid a listener, a user service, a hopping range and
	// the control port on the local target. Existing subscriptions stay intact.
	f.busy[sysinfo.Socket{Port: 20000, Proto: "udp"}] = true
	require(db.SetSetting(ctx, f.conn, db.ClashAPIPort, 20005))
	f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","port":20001,"options":{"server_name":"www.example.com"}}`, 201)
	f.call(t, "POST", "inbounds", `{"protocol":"hysteria2","options":{"port_hopping":{"enabled":true,"start":20002,"end":20004}}}`, 201)
	f.call(t, "POST", "users", `{"name":"alice"}`, 201)
	before, err := store.Load(ctx, 1, nil)
	require(err)
	f.write(t, "PUT", entryPath, `{"exit_server_id":1,"rules":{"presets":["ai"],"domains":["API.Example.com.","api.example.com"],"domain_suffixes":["example.net"]}}`, 204)
	if len(f.firewall.plan.ExitPorts) != 1 || f.firewall.plan.ExitPorts[0] != (firewall.ExitPort{Port: 20006, Source: "8.8.8.8"}) {
		t.Fatalf("wrong restricted port plan %+v", f.firewall.plan)
	}
	f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","port":20006,"options":{"server_name":"www.example.com"}}`, 409)
	desired, err := client.Desired(ctx, entry.Master.ServerID, entry.Key)
	require(err)
	if desired.Exit == nil || desired.Exit.Outgoing == nil || len(desired.Inbounds) != 0 {
		t.Fatal("entry did not get a private outbound")
	}
	out := desired.Exit.Outgoing
	if out.Port != 20006 || out.Address != "9.9.9.9" || out.Mode != exitlink.Split || out.Failure != exitlink.Block || len(out.Rules.Domains) != 1 || out.Rules.Domains[0] != "api.example.com" {
		t.Fatal("incorrect exit address, defaults or rules")
	}
	cfg, err := os.ReadFile(f.config)
	require(err)
	if !bytes.Contains(cfg, []byte(out.Password)) {
		t.Fatal("local exit core has no matching private credential")
	}
	for _, path := range []string{entryPath, "singbox/config", "inbounds", "users/1/links", "rules/preview?format=singbox"} {
		if w := f.call(t, "GET", path, "", 200); strings.Contains(w.Body.String(), out.Password) {
			t.Fatalf("private key leaked through %s", path)
		}
	}
	after, err := store.Load(ctx, 1, nil)
	require(err)
	if len(before.Nodes) != len(after.Nodes) || len(after.Nodes) != 2 {
		t.Fatal("internal listener changed the user subscription")
	}
	if old, err := client.Desired(ctx, legacy.Master.ServerID, legacy.Key); err != nil || old.Exit != nil {
		t.Fatal("older node desired structure acquired a live exit", err)
	}

	// Failed local apply leaves the reservation and the original generation intact.
	f.runner.failCheck = true
	f.call(t, "PUT", "servers/2", `{"public_host":"8.8.8.9"}`, 422)
	if s := decodeResult[serverRecord](t, f.call(t, "GET", "servers/2", "", 200)); s.PublicHost != "8.8.8.8" {
		t.Fatal("failed local apply changed subscription address")
	}
	f.call(t, "PUT", fmt.Sprintf("servers/%d/exit-address", entry.Master.ServerID), `{"address":"8.8.8.9"}`, 422)
	f.runner.failCheck = false
	if config, err := client.Desired(ctx, entry.Master.ServerID, entry.Key); err != nil || config.Generation != desired.Generation || config.Exit.Outgoing.Address != out.Address {
		t.Fatal("failed address edit advanced the committed desired snapshot", err)
	}
	f.call(t, "PUT", "servers/1/exit-address", `{"address":"9.9.9.10"}`, 204)
	revised, err := client.Desired(ctx, entry.Master.ServerID, entry.Key)
	require(err)
	if next := revised.Exit.Outgoing; next.Password != out.Password || next.Version != out.Version+1 || next.Address != "9.9.9.10" {
		t.Fatal("address change should preserve the port and credential and advance revision")
	}

	// A fresh remote port report is authenticated and reserves both transports.
	targetBeat := beat
	targetBeat.Exit = &agent.ExitReport{Ports: exitlink.EncodePorts([]int{20000, 20001}), Usage: &exitlink.Totals{Up: 20, Down: 30}}
	_, err = client.SyncHeartbeat(ctx, target.Master.ServerID, target.Key, targetBeat)
	require(err)
	// The local entry's probe tag embeds its revision. Editing the local
	// public IP through settings must regenerate the probe and roll back on error.
	f.call(t, "PUT", "settings", `{"public_host":"9.9.9.10"}`, 200)
	f.call(t, "PUT", "servers/1/exit-address", `{"use_server_ip":true}`, 204)
	f.call(t, "PUT", "servers/1/exit", `{"exit_server_id":3,"mode":"all"}`, 204)
	f.runner.failCheck = true
	f.call(t, "PUT", "settings", `{"public_host":"9.9.9.11"}`, 422)
	f.runner.failCheck = false
	if current := decodeResult[exitServerView](t, f.call(t, "GET", "servers/1/exit", "", 200)); current.Address != "9.9.9.10" || current.ServerIP != current.Address {
		t.Fatal("local address did not roll back")
	}
	f.call(t, "PUT", "settings", `{"public_host":"9.9.9.11"}`, 200)
	localExit, err := db.ReadLocalExit(ctx, f.conn)
	require(err)
	configBytes, err := os.ReadFile(f.config)
	require(err)
	if localExit.Outgoing == nil || !bytes.Contains(configBytes, []byte(localExit.Outgoing.Revision.ProbeTag())) {
		t.Fatal("local probe still targets the old revision")
	}
	f.call(t, "DELETE", "servers/1/exit", "", 204)
	f.write(t, "PUT", entryPath, `{"exit_server_id":3,"mode":"all","failure":"direct"}`, 204)
	if len(f.firewall.plan.ExitPorts) != 0 {
		t.Fatal("switch did not close former local source rules")
	}
	newDesired, err := client.Desired(ctx, target.Master.ServerID, target.Key)
	require(err)
	if len(newDesired.Exit.Incoming) != 1 || newDesired.Exit.Incoming[0].Port != 20002 || newDesired.Exit.Incoming[0].Password == out.Password {
		t.Fatal("new target lacks a unique credential and reserved port")
	}
	desired, err = client.Desired(ctx, entry.Master.ServerID, entry.Key)
	require(err)
	out = desired.Exit.Outgoing
	if out == nil || out.Password != newDesired.Exit.Incoming[0].Password || out.Mode != exitlink.All {
		t.Fatal("new target and entry have mismatched settings")
	}
	// A numeric server edit moves the existing link. Domain edits, saved
	// overrides, and selecting automatic mode again retain their own semantics.
	rev := out.Version
	for _, step := range []struct {
		path, body, ip  string
		custom, changed bool
	}{
		{"servers/3", `{"public_host":"8.8.4.5"}`, "8.8.4.5", false, true},
		{"servers/3", `{"public_host":"cdn.example.org"}`, "8.8.4.5", false, false},
		{"servers/3/exit-address", `{"address":"1.0.0.1"}`, "1.0.0.1", true, true},
		{"servers/3", `{"public_host":"8.8.4.6"}`, "1.0.0.1", true, false},
		{"servers/3/exit-address", `{"use_server_ip":true}`, "8.8.4.6", false, true},
	} {
		status := 200
		if strings.HasSuffix(step.path, "/exit-address") {
			status = 204
		}
		f.call(t, "PUT", step.path, step.body, status)
		if step.changed {
			rev++
		}
		view := decodeResult[exitServerView](t, f.call(t, "GET", targetPath, "", 200))
		d, err := client.Desired(ctx, entry.Master.ServerID, entry.Key)
		require(err)
		next := d.Exit.Outgoing
		if view.CustomAddress != step.custom || view.Address != step.ip || next.ID != out.ID || next.Version != rev || next.Password != out.Password || next.Port != out.Port || next.Address != step.ip {
			t.Fatalf("incorrect address or link identity after %s", step.path)
		}
		out = next
	}
	// Even if a node reports protocol 2 after a link existed, it sees the
	// old response shape and cannot be selected for new links.
	_, err = client.SyncHeartbeat(ctx, entry.Master.ServerID, entry.Key, legacyBeat)
	require(err)
	if old, err := client.Desired(ctx, entry.Master.ServerID, entry.Key); err != nil || old.Exit != nil {
		t.Fatal("older node received exit configuration", err)
	}
	_, err = client.SyncHeartbeat(ctx, entry.Master.ServerID, entry.Key, beat)
	require(err)

	probe := beat
	ready, err := client.Desired(ctx, entry.Master.ServerID, entry.Key)
	require(err)
	probe.AppliedGeneration = &ready.Generation
	at := time.Now().Unix()
	probe.Exit = &agent.ExitReport{Probe: &exitlink.Probe{Revision: out.Revision, CheckedAt: at, Available: new(false), Failures: 2, PolicyDown: true}}
	for range 2 {
		_, err = client.SyncHeartbeat(ctx, entry.Master.ServerID, entry.Key, probe)
		require(err)
	}
	view = decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200))
	if view.Link.Status != "fallback" || *view.Link.CheckedAt != at {
		t.Fatal("status did not reflect the entry-side fallback")
	}
	// Another identity cannot update the entry's probe.
	probe.Exit.Probe = &exitlink.Probe{Revision: out.Revision, CheckedAt: at + 1, Available: new(true)}
	_, err = client.SyncHeartbeat(ctx, target.Master.ServerID, target.Key, probe)
	require(err)
	view = decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200))
	if view.Link.Status != "fallback" {
		t.Fatal("accepted a probe from a different server")
	}
	_, err = client.SyncHeartbeat(ctx, entry.Master.ServerID, entry.Key, probe)
	require(err)
	if view = decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200)); view.Link.Status != "available" {
		t.Fatal("recovery not shown")
	}
	f.call(t, "PUT", entryPath, `{"exit_server_id":3,"mode":"all","failure":"block"}`, 204)
	if waiting := decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200)); waiting.Link.Status != "syncing" {
		t.Fatal("fresh probe hid a pending change on the entry")
	}
	var notifications int
	require(f.conn.QueryRow("SELECT count(*) FROM alert_events WHERE kind IN ('exit_down','exit_recovered') AND server_id=?", entry.Master.ServerID).Scan(&notifications))
	if notifications != 2 {
		t.Fatalf("got %d exit events; want one outage and one recovery", notifications)
	}
	targetBeat.Exit.Usage = &exitlink.Totals{Up: 19, Down: 30}
	_, err = client.SyncHeartbeat(ctx, target.Master.ServerID, target.Key, targetBeat)
	require(err)
	usage := decodeResult[exitServerView](t, f.call(t, "GET", targetPath, "", 200)).Forwarded
	if usage != (exitlink.Totals{Up: 20, Down: 30}) {
		t.Fatalf("retry regressed or double-counted forwarded traffic: %+v", usage)
	}
	traffic := decodeResult[serverTrafficView](t, f.call(t, "GET", fmt.Sprintf("servers/%d/traffic", target.Master.ServerID), "", 200))
	if traffic.ExitTraffic == nil || *traffic.ExitTraffic != usage || traffic.UserTraffic != (cycleBytes{}) {
		t.Fatal("exit traffic was not shown separately from user traffic")
	}

	// Both mirrors apply their own state. Removing a target cleans the entry's
	// desired state and lets the removed target fetch only the revocation.
	var remoteFirewall resourceFirewall
	target.Manager, err = singbox.NewManager(target.Writer, singbox.ApplyOptions{OS: "linux", Config: filepath.Join(t.TempDir(), "target.json"), Runner: &resourceRunner{}, Firewall: &remoteFirewall})
	require(err)
	require(target.Sync(ctx, beat, func() bool { return true }))
	if len(remoteFirewall.plan.ExitPorts) != 1 {
		t.Fatal("remote target did not install its own restricted port")
	}
	f.call(t, "DELETE", fmt.Sprintf("servers/%d", target.Master.ServerID), "", http.StatusNoContent)
	if next, err := client.Desired(ctx, entry.Master.ServerID, entry.Key); err != nil || next.Exit != nil {
		t.Fatal("deletion left the surviving entry configured", err)
	}
	if _, err := client.Desired(ctx, target.Master.ServerID, target.Key); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatal("removed target was allowed to read desired state", err)
	}
	if err := target.Sync(ctx, beat, func() bool { return true }); !errors.Is(err, agent.ErrUnauthorized) {
		t.Fatal("target did not finish removing stale local exit data", err)
	}
	if len(remoteFirewall.plan.ExitPorts) != 0 {
		t.Fatal("removed node retained its source filter or service port")
	}
	if _, err := db.GetSetting(ctx, target.Reader, db.AgentExit); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("removed node retained its link credential")
	}

	f.write(t, "PUT", entryPath, `{"exit_server_id":1,"mode":"all"}`, 204)
	f.write(t, "DELETE", entryPath, "", 204)
	if data := decodeResult[exitServerView](t, f.call(t, "GET", entryPath, "", 200)); data.Link != nil || len(f.firewall.plan.ExitPorts) != 0 {
		t.Fatal("disabled link remained active")
	}
}
