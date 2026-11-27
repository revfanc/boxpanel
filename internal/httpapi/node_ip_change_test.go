package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func TestChangedNodeIPReceivesLogLeaseAndNewExit(t *testing.T) {
	ctx := t.Context()
	f, master, _ := newServerPanelTest(t)
	var peer atomic.Value
	peer.Store("8.8.8.8:4567")
	client, url, pin := panelNodeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model the transport peer seen by the master before and after renumbering.
		r.RemoteAddr = peer.Load().(string)
		f.h.ServeHTTP(w, r)
	}))
	node, paths := joinTestNode(t, master, client, url, pin, "moving-node", "8.8.8.8")
	journal := &serverPanelJournal{}
	manager, err := singbox.NewManager(node.Writer, singbox.ApplyOptions{OS: "linux", Config: filepath.Join(paths.DataDir, "sing-box.json"), Runner: journal, Firewall: &resourceFirewall{}})
	if err != nil {
		t.Fatal(err)
	}
	node.Manager, node.ReadLogs = manager, manager.LogBatch
	node.ExitPorts = func(ctx context.Context) (map[sysinfo.Socket]bool, error) {
		return map[sysinfo.Socket]bool{{Port: exitlink.PortFirst, Proto: "tcp"}: true}, nil
	}
	syncNode := func() {
		t.Helper()
		if err := node.Sync(ctx, panelHeartbeat(), func() bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	syncNode()
	syncNode()

	// Drop the control connection and let both presence and the port sample expire.
	client.Close()
	stale := time.Now().Add(-3 * time.Minute).Unix()
	if _, err := f.conn.Exec("UPDATE servers SET last_seen_at=?,exit_ports_at=? WHERE id=?", stale, stale, node.Master.ServerID); err != nil {
		t.Fatal(err)
	}
	if err := master.MarkOffline(ctx); err != nil {
		t.Fatal(err)
	}
	peer.Store("8.8.4.4:5678")
	f.call(t, "PUT", "servers/1/exit-address", `{"address":"9.9.9.9"}`, 204)
	f.call(t, "PUT", "servers/2/exit-address", `{"address":"8.8.4.4"}`, 204)
	const lease = "servers/2/logs/01010101010101010101010101010101"
	f.call(t, "PUT", lease, "", 204)
	readLogs := func() agent.LogView {
		t.Helper()
		return decodeResult[agent.LogView](t, f.call(t, "GET", "servers/2/logs", "", 200))
	}
	if readLogs().Ready {
		t.Fatal("offline node supplied logs without a heartbeat")
	}
	const policy = `{"exit_server_id":2,"mode":"all","failure":"block"}`
	f.call(t, "PUT", "servers/1/exit", policy, 422)

	// The same node key, database and master URL reconnect from the new peer.
	syncNode()
	if view := readLogs(); !view.Ready || !slices.Contains(view.Lines, "line 198") {
		t.Fatal("no fresh log snapshot from the reconnected node")
	}
	exit := decodeResult[exitServerView](t, f.call(t, "GET", "servers/1/exit", "", 200))
	if len(exit.Candidates) != 1 || exit.Candidates[0].Reason != "" || exit.Candidates[0].Address != "8.8.4.4" {
		t.Fatal("new address or port inventory is missing")
	}
	f.call(t, "PUT", "servers/1/exit", policy, 204)
	journal.delta = true
	syncNode()
	if view := readLogs(); !slices.Contains(view.Lines, "latest line") {
		t.Fatal("incremental logs stopped during exit creation")
	}
	out, err := db.ReadLocalExit(ctx, f.conn)
	if err != nil || out.Outgoing == nil || out.Outgoing.Address != "8.8.4.4" || out.Outgoing.Port != exitlink.PortFirst+1 {
		t.Fatal("entry did not use the new address and fresh port inventory", err)
	}
	in, err := db.ReadLocalExit(ctx, node.Writer)
	if err != nil || len(in.Incoming) != 1 || in.Incoming[0].Password != out.Outgoing.Password || in.Incoming[0].Port != out.Outgoing.Port || in.Incoming[0].Source != "9.9.9.9" {
		t.Fatal("node did not apply the new exit", err)
	}
	f.call(t, "DELETE", lease, "", 204)
}
