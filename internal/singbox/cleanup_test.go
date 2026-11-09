package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
)

type cleanupController struct {
	Controller
	running bool
	fail    bool
}

func (c cleanupController) State(context.Context, string) (ServiceState, error) {
	if c.fail {
		return ServiceState{}, errors.New("systemd unavailable")
	}
	return ServiceState{Running: c.running}, nil
}

func TestCleanupFirewallIncludesInterruptedAndPanelPorts(t *testing.T) {
	f := newApplyFixture(t)
	ctx := context.Background()
	f.m.controller = cleanupController{}
	previous := firewall.Plan{Ports: []firewall.Port{{Port: 443, Proto: "tcp"}}}
	next := firewall.Plan{Ports: []firewall.Port{{Port: 8443, Proto: "udp"}}, Hops: []firewall.Hop{{Start: 20000, End: 30000, Port: 8443}}}
	stored, _ := json.Marshal(previous)
	if err := db.SetSetting(ctx, f.conn, db.AppliedFirewall, json.RawMessage(stored)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, f.conn, db.PanelFirewallPorts, []int{2053, 2054}); err != nil {
		t.Fatal(err)
	}
	pending, _ := json.Marshal(pendingApply{ID: "interrupted", HadConfig: true, Previous: previous, Next: next})
	if err := os.WriteFile(f.path+".pending", pending, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, []byte("untouched-config"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.m.CleanupFirewall(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := unionPlan(previous, next)
	want = unionPlan(want, firewall.Plan{Ports: []firewall.Port{{Port: 2053, Proto: "tcp"}, {Port: 2054, Proto: "tcp"}}})
	for _, call := range f.fw.calls {
		if !reflect.DeepEqual(call.previous, want) || len(call.desired.Ports) != 0 || len(call.desired.Hops) != 0 {
			t.Fatalf("cleanup=%+v", call)
		}
	}
	if string(readFile(t, f.path)) != "untouched-config" || string(readFile(t, f.path+".pending")) != string(pending) {
		t.Fatal("modified configuration or recovery state")
	}
	if len(f.r.commands) != 0 {
		t.Fatal("cleanup executed config commands", f.r.commands)
	}
	got, err := db.GetSetting(ctx, f.conn, db.AppliedFirewall)
	if err != nil || string(got) != string(stored) {
		t.Fatal("cleanup modified stored state", err)
	}
}

func TestCleanupRefusesUnsafeStateAndCanRetry(t *testing.T) {
	for _, scenario := range []string{"running", "state failure", "journal", "firewall", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			f := newApplyFixture(t)
			f.m.controller = cleanupController{running: scenario == "running", fail: scenario == "state failure"}
			switch scenario {
			case "journal":
				if err := os.WriteFile(f.path+".pending", []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "firewall":
				f.fw.fails = 1
			case "unsupported":
				f.m.unsupported = true
			}
			if f.m.CleanupFirewall(context.Background()) == nil {
				t.Fatal("expected failure")
			}
			if scenario != "firewall" && len(f.fw.calls) != 0 {
				t.Fatal("modified firewall before validation")
			}
			if scenario == "firewall" {
				if err := f.m.CleanupFirewall(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
