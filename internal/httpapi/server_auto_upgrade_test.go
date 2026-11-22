package httpapi

import (
	"strings"
	"testing"
)

func TestServerAutoUpgradeSetting(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	if _, err := f.conn.Exec(`INSERT INTO servers(id,name,public_host,status,version,agent_public_key)
		VALUES(2,'Remote','remote.example','online','v0.3.1','key')`); err != nil {
		t.Fatal(err)
	}
	list := decodeResult[[]serverRecord](t, f.call(t, "GET", "servers", "", 200))
	if list[0].AutoUpgrade != nil || list[1].AutoUpgrade == nil || *list[1].AutoUpgrade {
		t.Fatalf("auto_upgrade defaults: %+v", list)
	}
	f.call(t, "PUT", "servers/1", `{"auto_upgrade":true}`, 400)
	f.call(t, "PUT", "servers/2", `{"auto_upgrade":"yes"}`, 400)
	calls := f.apply.calls
	updated := decodeResult[serverRecord](t, f.call(t, "PUT", "servers/2", `{"auto_upgrade":true}`, 200))
	if updated.AutoUpgrade == nil || !*updated.AutoUpgrade || updated.Name != "Remote" || f.apply.calls != calls {
		t.Fatalf("enable = %+v", updated)
	}
	var generation int64
	if err := f.conn.QueryRow("SELECT desired_generation FROM servers WHERE id=2").Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("auto-upgrade changed the desired state: %d %v", generation, err)
	}
	w := f.call(t, "PUT", "servers/2", `{"name":"Edge"}`, 200)
	if !strings.Contains(w.Body.String(), `"auto_upgrade":true`) {
		t.Fatal("rename reset auto_upgrade", w.Body.String())
	}
}
