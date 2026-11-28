package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/singbox"
)

const draftProxy = `{"type":"https","host":"proxy.example.org","port":8443,"username":"member","password":"fixture-only-proxy-secret"}`

func withProxy(proxy string) string { return `{"mode":"all","external_proxy":` + proxy + "}" }

func TestLocalExternalExitLifecycle(t *testing.T) {
	f := newResources(t, false)
	const path = "servers/1/exit"
	f.call(t, "PUT", path, `{"exit_server_id":2,"external_proxy":`+draftProxy+"}", 400)
	f.call(t, "POST", path+"/test", strings.Replace(draftProxy, "proxy.example.org", "127.0.0.1", 1), 400)
	f.call(t, "PUT", path, withProxy(strings.Replace(draftProxy, "https", "ftp", 1)), 400)
	f.write(t, "PUT", path, withProxy(draftProxy), 204)
	current := func() *exitlink.Outbound {
		t.Helper()
		cfg, err := db.ReadLocalExit(t.Context(), f.conn)
		if err != nil || cfg.Outgoing == nil || cfg.Outgoing.External == nil || len(cfg.Incoming) != 0 || len(f.firewall.plan.ExitPorts) != 0 {
			t.Fatal("incorrect external exit state", err)
		}
		return cfg.Outgoing
	}
	original := current()
	const secret = "fixture-only-proxy-secret"
	if original.External.Password != secret || original.External.Type != "https" {
		t.Fatal("incorrect saved proxy")
	}
	raw, err := os.ReadFile(f.config)
	if err != nil || !strings.Contains(string(raw), secret) {
		t.Fatal("proxy credential missing in applied private config", err)
	}
	view := decodeResult[exitServerView](t, f.call(t, "GET", path, "", 200))
	if !view.ExternalSupported || view.Link.External == nil || !view.Link.External.HasPassword || view.Link.ExitServerID != 0 {
		t.Fatal("incorrect public proxy representation")
	}
	for _, resource := range []string{path, "singbox/config"} {
		if strings.Contains(f.call(t, "GET", resource, "", 200).Body.String(), secret) {
			t.Fatal("credential in panel response", resource)
		}
	}
	reuse := strings.Replace(draftProxy, `"password":"`+secret+`"`, `"use_saved_password":true`, 1)
	f.unchanged(t, "PUT", path, withProxy(reuse), 204)
	for _, endpoint := range []string{"other.example.org", "9.9.9.9"} {
		changed := strings.Replace(reuse, "proxy.example.org", endpoint, 1)
		f.call(t, "POST", path+"/test", changed, 400)
		f.call(t, "PUT", path, withProxy(changed), 400)
	}
	if err := db.SetExitAddress(t.Context(), f.conn, 1, "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if current().Revision != original.Revision {
		t.Fatal("entry renumbering or password reuse invalidated the proxy")
	}
	f.runner.failCheck = true
	replacement := strings.Replace(draftProxy, "8443", "3128", 1)
	f.write(t, "PUT", path, withProxy(replacement), 422)
	f.runner.failCheck = false
	if current().Revision != original.Revision || current().External.Port != 8443 {
		t.Fatal("failed proxy config was committed")
	}
	f.write(t, "PUT", path, withProxy(replacement), 204)
	if current().Version != original.Version+1 {
		t.Fatal("endpoint change did not invalidate probe")
	}
	f.write(t, "DELETE", path, "", 204)
	if view := decodeResult[exitServerView](t, f.call(t, "GET", path, "", 200)); view.Link != nil {
		t.Fatal("disabled external exit persisted")
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Fatal("credential in panel log")
	}
}

func TestRemoteExternalExitTestAndSwitch(t *testing.T) {
	ctx := t.Context()
	f, master, _ := newServerPanelTest(t)
	client, url, pin := panelNodeClient(t, f.h)
	node, _ := joinTestNode(t, master, client, url, pin, "entry", "8.8.8.8")
	id := node.Master.ServerID
	path := fmt.Sprintf("servers/%d/exit", id)
	beat := panelHeartbeat()
	heartbeat := func() (agent.HeartbeatReply, error) { return client.SyncHeartbeat(ctx, id, node.Key, beat) }
	beat.Protocol = agent.ExitProtocol
	if _, err := heartbeat(); err != nil {
		t.Fatal(err)
	}
	f.call(t, "POST", path+"/test", draftProxy, 422)
	f.call(t, "PUT", path, withProxy(draftProxy), 422)
	beat.Protocol = agent.ProtocolVersion
	if _, err := heartbeat(); err != nil {
		t.Fatal(err)
	}
	tested := false
	node.ProbeProxy = func(_ context.Context, p exitlink.Proxy) (netip.Addr, error) {
		if p.Host != "proxy.example.org" || p.Password != "fixture-only-proxy-secret" {
			return netip.Addr{}, errors.New("wrong proxy")
		}
		tested = true
		return netip.MustParseAddr("9.9.9.9"), nil
	}
	// Queue an unsaved draft, deliver it over the enrolled node's signed
	// heartbeat and submit the result through the normal node sync path.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(t, f.h, "POST", "https://example.test/api/v1/"+path+"/test", draftProxy, f.cookie, nil)
	}()
	deadline := time.Now().Add(4 * time.Second)
	for {
		reply, err := heartbeat()
		if err != nil {
			t.Fatal(err)
		}
		if reply.ProxyTest != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test command never reached selected entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager, err := singbox.NewManager(node.Writer, singbox.ApplyOptions{OS: "linux", Config: filepath.Join(t.TempDir(), "core.json"), Runner: &resourceRunner{}, Firewall: &resourceFirewall{}})
	if err != nil {
		t.Fatal(err)
	}
	node.Manager = manager
	if err := node.Sync(ctx, beat, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		requireStatus(t, result, 200)
		if !tested || decodeResult[map[string]string](t, result)["ip"] != "9.9.9.9" {
			t.Fatal("panel did not receive entry-side exit IP")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("node result did not reach pending panel request")
	}
	if view := decodeResult[exitServerView](t, f.call(t, "GET", path, "", 200)); view.Link != nil {
		t.Fatal("test saved the draft")
	}

	// Switch a managed entry to an ISP proxy and back. The former remote
	// target must lose its internal listener when the entry switches.
	target, _ := joinTestNode(t, master, client, url, pin, "exit", "8.8.4.4")
	targetBeat := beat
	targetBeat.Exit = &agent.ExitReport{Ports: exitlink.EncodePorts([]int{20000})}
	if _, err := client.SyncHeartbeat(ctx, target.Master.ServerID, target.Key, targetBeat); err != nil {
		t.Fatal(err)
	}
	managed := fmt.Sprintf(`{"exit_server_id":%d}`, target.Master.ServerID)
	f.call(t, "PUT", path, managed, 204)
	f.call(t, "PUT", path, withProxy(draftProxy), 204)
	desired, err := client.Desired(ctx, id, node.Key)
	if err != nil || desired.Exit == nil || desired.Exit.Outgoing == nil || desired.Exit.Outgoing.External == nil || desired.Exit.Outgoing.External.Password != "fixture-only-proxy-secret" {
		t.Fatal("entry lacked its proxy configuration", err)
	}
	peer, err := client.Desired(ctx, target.Master.ServerID, target.Key)
	if err != nil || peer.Exit != nil {
		t.Fatal("former target retained exit listener or received proxy credentials", err)
	}
	beat.Protocol = agent.ExitProtocol
	if _, err := heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Desired(ctx, id, node.Key); !errors.Is(err, agent.ErrAgentUpgradeRequired) {
		t.Fatal("older node could fetch proxy settings", err)
	}
	f.call(t, "PUT", path, managed, 204)
	desired, err = client.Desired(ctx, id, node.Key)
	if err != nil || desired.Exit == nil || desired.Exit.Outgoing == nil || desired.Exit.Outgoing.External != nil {
		t.Fatal("legacy node could not switch back to a managed exit", err)
	}
}
