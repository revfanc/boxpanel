package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type exitMonitorApplier struct {
	writer   *sql.DB
	attempts int
	err      error
}

func (a *exitMonitorApplier) Apply(ctx context.Context, mutation singbox.Mutation) error {
	a.attempts++
	if a.err != nil {
		return a.err
	}
	return (ExitMonitor{Writer: a.writer}).update(ctx, mutation)
}

func monitorDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "exit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestExitMonitorDebouncesAndRetriesPolicy(t *testing.T) {
	ctx := t.Context()
	conn := monitorDB(t)
	key, err := exitlink.NewPassword()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO servers(id,name,public_host,exit_address) VALUES(2,'exit','8.8.8.8','8.8.8.8');
		UPDATE servers SET exit_address='9.9.9.9' WHERE id=1;
		INSERT INTO exit_links(id,entry_id,exit_id,port,password,mode,failure,rules) VALUES (1,1,2,20000,?,'all','direct','{}')`, key); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, conn, db.ClashAPIPort, 2053); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, conn, db.ClashAPISecret, "test-secret"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	var success, switchAddress bool
	var transitions []bool
	applier := &exitMonitorApplier{writer: conn}
	monitor := ExitMonitor{Writer: conn, Reader: conn, Manager: applier, now: func() time.Time { return now },
		probe: func(_ context.Context, port int, secret string, revision exitlink.Revision) (bool, error) {
			if port != 2053 || secret != "test-secret" || revision != (exitlink.Revision{ID: 1, Version: 1}) {
				t.Fatal("incorrect local probe target")
			}
			if switchAddress {
				if err := db.SetExitAddress(ctx, conn, 2, "8.8.4.4"); err != nil {
					t.Fatal(err)
				}
			}
			return success, nil
		},
		OnTransition: func(_ context.Context, _ *sql.Tx, entry int64, address string, incident, _ time.Time, recovered bool) error {
			if entry != 1 || address != "8.8.8.8" || incident.Unix() != 1030 {
				t.Fatal("wrong exit incident")
			}
			transitions = append(transitions, recovered)
			return nil
		}}
	run := func(want error) {
		t.Helper()
		if err := monitor.checkLink(ctx); !errors.Is(err, want) {
			t.Fatal(err)
		}
		now = now.Add(exitlink.ProbeEvery)
	}
	run(nil)
	if applier.attempts != 0 {
		t.Fatal("a single failure changed the routing policy")
	}
	applier.err = errors.New("apply failed")
	run(applier.err)
	if p, err := db.GetSetting(ctx, conn, db.ExitProbe); err != nil || p.Available == nil || *p.Available || p.PolicyDown {
		t.Fatal("failed apply should report the outage without recording fallback", err)
	}
	applier.err = nil
	run(nil)
	run(nil)
	success = true
	run(nil)
	if applier.attempts != 3 || !slices.Equal(transitions, []bool{false, true}) {
		t.Fatalf("policy was not applied once per confirmed change: applies %d, alerts %v", applier.attempts, transitions)
	}
	switchAddress = true
	run(nil)
	if p, err := db.GetSetting(ctx, conn, db.ExitProbe); err != nil || p.CheckedAt != 1120 {
		t.Fatal("probe against a superseded address was committed", err)
	}
}

func TestExitCountersResumeAndHandleNewBoot(t *testing.T) {
	ctx := t.Context()
	conn := monitorDB(t)
	if err := db.SetSetting(ctx, conn, db.ExitEver, true); err != nil {
		t.Fatal(err)
	}
	raw := exitlink.Totals{Up: 12, Down: 8}
	monitor := ExitMonitor{Writer: conn, Reader: conn, BootID: "first",
		counters: func(context.Context) (exitlink.Totals, bool, error) { return raw, true, nil }}
	expect := func(want exitlink.Totals) {
		t.Helper()
		if err := monitor.sampleUsage(ctx); err != nil {
			t.Fatal(err)
		}
		checkpoint, err := db.GetSetting(ctx, conn, db.ExitUsage)
		if err != nil || checkpoint.Total != want {
			t.Fatalf("usage %+v, want %+v, error %v", checkpoint.Total, want, err)
		}
	}
	expect(raw)
	expect(raw)
	raw.Down = 32
	expect(exitlink.Totals{Up: 12, Down: 32})
	monitor.BootID, raw = "second", exitlink.Totals{Up: 512, Down: 5}
	expect(exitlink.Totals{Up: 524, Down: 37})
	raw = exitlink.Totals{Up: 5, Down: 5}
	expect(exitlink.Totals{Up: 529, Down: 37})
}

func TestExitProbeUsesThePrivateControllerAndAnAlternateTarget(t *testing.T) {
	revision := exitlink.Revision{ID: 3, Version: 9}
	var requests atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.URL.Path != "/proxies/"+revision.ProbeTag()+"/delay" ||
			r.URL.Query().Get("timeout") != "4000" || r.URL.Query().Get("url") == "" {
			t.Error("bad probe request")
		}
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"delay":1}`)
	}))
	defer controller.Close()
	port := controller.Listener.Addr().(*net.TCPAddr).Port
	if up, err := probeExit(t.Context(), port, "test-secret", revision); err != nil || !up || requests.Load() != 2 {
		t.Fatalf("probe failed after trying alternate target: up=%v err=%v attempts=%d", up, err, requests.Load())
	}
}
