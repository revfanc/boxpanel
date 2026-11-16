package singbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/revfanc/boxpanel/internal/firewall"
)

func TestTrafficPlanRollbackAndPendingRecovery(t *testing.T) {
	ctx := context.Background()
	f, _ := runningApplyFixture(t)
	before := readFile(t, f.path)
	var initial firewall.Plan
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	initial, err = storedPlan(ctx, tx)
	tx.Rollback()
	if err != nil || !reflect.DeepEqual(initial.TrafficUsers, []int64{1}) {
		t.Fatal(initial, err)
	}
	f.fw.fails = 1
	observed := 0
	f.m.firewall = observingFirewall{f.fw, func() {
		var p pendingApply
		if err := json.Unmarshal(readFile(t, f.path+".pending"), &p); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p.Previous.TrafficUsers, []int64{1}) || len(p.Next.TrafficUsers) != 0 {
			t.Fatal("traffic users missing from pending", p)
		}
		observed++
	}}
	err = f.m.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET enabled=0 WHERE id=1")
		return err
	})
	if err == nil || observed != 2 || len(f.fw.calls) != 2 {
		t.Fatal("traffic failure did not restore", err, observed, f.fw.calls)
	}
	if !reflect.DeepEqual(f.fw.calls[1].desired.TrafficUsers, []int64{1}) || string(readFile(t, f.path)) != string(before) {
		t.Fatal("previous traffic/config not restored")
	}
	var enabled bool
	if err := f.conn.QueryRow("SELECT enabled FROM users WHERE id=1").Scan(&enabled); err != nil || !enabled {
		t.Fatal("DB mutation leaked", err)
	}
	assertMissing(t, f.path+".pending")
	// Simulate an interrupted uncommitted generation removing this user.
	f.m.firewall = f.fw
	raw, _ := json.Marshal(pendingApply{ID: "interrupted-traffic", HadConfig: true, Previous: initial, Next: firewall.Plan{Ports: initial.Ports}})
	if err := f.m.files.WriteFile(f.path+".pending", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.m.files.WriteFile(f.path, []byte("uncommitted config"), 0600); err != nil {
		t.Fatal(err)
	}
	f.fw.calls = nil
	if err := f.m.Apply(ctx, dataOnly); err != nil {
		t.Fatal(err)
	}
	if len(f.fw.calls) != 1 || !reflect.DeepEqual(f.fw.calls[0].desired.TrafficUsers, []int64{1}) {
		t.Fatal("pending recovery did not restore accounting plan", f.fw.calls)
	}
	assertMissing(t, f.path+".pending")
}

func TestUnionPlanIncludesTrafficUsers(t *testing.T) {
	p := unionPlan(firewall.Plan{TrafficUsers: []int64{1, 2}}, firewall.Plan{TrafficUsers: []int64{2, 3}})
	if !reflect.DeepEqual(p.TrafficUsers, []int64{1, 2, 3}) {
		t.Fatal(p)
	}
}
