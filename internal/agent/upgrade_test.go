package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestNewerRelease(t *testing.T) {
	for _, c := range []struct {
		target, current string
		want            bool
	}{
		{"v0.4.0", "v0.3.1", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.3.1", "v0.3.1", false},
		{"v0.3.0", "v0.3.1", false},
		{"v0.4.0", "v0.4.0-rc.2", true},
		{"v0.4.0-rc.2", "v0.4.0-rc.1", true},
		{"v0.4.0-rc.10", "v0.4.0-rc.9", true},
		{"v0.4.0-rc.1", "v0.4.0", false},
		{"v0.4.0", "dev", false},
		{"main", "v0.3.1", false},
		{"v0.4.0-beta.1", "v0.3.1", false},
		{"v0.4.0;reboot", "v0.3.1", false},
	} {
		if got := NewerRelease(c.target, c.current); got != c.want {
			t.Errorf("NewerRelease(%q, %q) = %v, want %v", c.target, c.current, got, c.want)
		}
	}
}

func TestHeartbeatReplyCarriesAutoUpgrade(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`UPDATE servers SET version='v0.4.0' WHERE is_local=1;
		INSERT INTO servers (id,name,public_host,status,agent_public_key) VALUES (2,'edge','edge.example','online','key')`); err != nil {
		t.Fatal(err)
	}
	master := NewMaster(conn, conn, nil)
	beat := Heartbeat{Version: "v0.3.1", Protocol: ProtocolVersion}
	reply, err := master.RecordHeartbeat(ctx, authenticated{2, "key"}, beat)
	if err != nil || reply.AutoUpgrade || reply.Version != "v0.4.0" {
		t.Fatalf("default reply = %+v, %v", reply, err)
	}
	if _, err := conn.Exec("UPDATE servers SET auto_upgrade=1 WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	if reply, err = master.RecordHeartbeat(ctx, authenticated{2, "key"}, beat); err != nil || !reply.AutoUpgrade {
		t.Fatalf("enabled reply = %+v, %v", reply, err)
	}
}
