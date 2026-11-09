package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
)

type testPortOpener struct {
	ports []int
	fail  bool
}

func (o *testPortOpener) Open(_ context.Context, port int, proto string) error {
	if proto != "tcp" {
		return errors.New("wrong protocol")
	}
	o.ports = append(o.ports, port)
	if o.fail {
		return errors.New("partial firewall failure")
	}
	return nil
}

func TestPanelFirewallEnrollmentReplayAndFailure(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	opener := &testPortOpener{}
	if err := OpenPanelFirewall(ctx, conn, 2053, false, opener); err != nil {
		t.Fatal(err)
	}
	if len(opener.ports) != 0 {
		t.Fatal("unenrolled instance modified firewall")
	}
	for _, port := range []int{2053, 2053, 2054} {
		if err := OpenPanelFirewall(ctx, conn, port, true, opener); err != nil {
			t.Fatal(err)
		}
	}
	if err := OpenPanelFirewall(ctx, conn, 2054, false, opener); err != nil {
		t.Fatal(err)
	}
	opener.fail = true
	if err := OpenPanelFirewall(ctx, conn, 2055, true, opener); err == nil {
		t.Fatal("ignored firewall failure")
	}
	ports, err := db.GetSetting(ctx, conn, db.PanelFirewallPorts)
	if err != nil || !reflect.DeepEqual(ports, []int{2053, 2054, 2055}) {
		t.Fatal("lost cleanup intent", ports, err)
	}
	if !reflect.DeepEqual(opener.ports, []int{2053, 2053, 2054, 2054, 2055}) {
		t.Fatal(opener.ports)
	}
	if err := db.SetSetting(ctx, conn, db.PanelFirewallPorts, []int{-1}); err != nil {
		t.Fatal(err)
	}
	count := len(opener.ports)
	if OpenPanelFirewall(ctx, conn, 2053, true, opener) == nil || OpenPanelFirewall(ctx, conn, 0, true, opener) == nil {
		t.Fatal("accepted invalid port")
	}
	if len(opener.ports) != count {
		t.Fatal("invalid data reached firewall")
	}
}
