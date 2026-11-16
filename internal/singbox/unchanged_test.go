package singbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
)

type observedApplyFiles struct {
	nativeApplyFiles
	changes []string
	renamed func()
}

func (f *observedApplyFiles) WriteFile(path string, data []byte, mode fs.FileMode) error {
	f.changes = append(f.changes, "write:"+path)
	return f.nativeApplyFiles.WriteFile(path, data, mode)
}
func (f *observedApplyFiles) Rename(from, to string) error {
	f.changes = append(f.changes, "rename:"+from)
	if f.renamed != nil {
		f.renamed()
	}
	return f.nativeApplyFiles.Rename(from, to)
}
func (f *observedApplyFiles) Remove(path string) error {
	f.changes = append(f.changes, "remove:"+path)
	return f.nativeApplyFiles.Remove(path)
}

type observingFirewall struct {
	FirewallSync
	observe func()
}

func (f observingFirewall) Sync(ctx context.Context, previous, next firewall.Plan) error {
	f.observe()
	return f.FirewallSync.Sync(ctx, previous, next)
}
func dataOnly(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE users SET remark='data-only',traffic_limit=1000000,expire_at=4102444800 WHERE id=1")
	return err
}
func runningApplyFixture(t *testing.T) (*applyFixture, *observedApplyFiles) {
	t.Helper()
	f := newApplyFixture(t)
	// An actual enabled inbound makes applied_users bookkeeping meaningful.
	if _, err := f.conn.Exec(`INSERT INTO inbounds(tag,protocol,port,options,created_at,updated_at) VALUES('hy2','hysteria2',8443,'{"sni":"www.bing.com","cert_mode":"self_signed"}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.r.commands = nil
	f.r.checks, f.r.restarts = 0, 0
	f.fw.calls = nil
	files := &observedApplyFiles{}
	f.m.files = files
	return f, files
}
func assertNoCoreChanges(t *testing.T, f *applyFixture, files *observedApplyFiles) {
	t.Helper()
	if f.r.checks != 0 || f.r.restarts != 0 || f.r.stops != 0 || len(f.fw.calls) != 0 || len(files.changes) != 0 {
		t.Fatalf("unchanged config touched external state: checks=%d restarts=%d stops=%d firewall=%d files=%v", f.r.checks, f.r.restarts, f.r.stops, len(f.fw.calls), files.changes)
	}
	if !reflect.DeepEqual(f.r.commands, [][]string{{"systemctl", "is-active", "--", SingboxUnit}}) {
		t.Fatalf("unexpected unchanged commands: %v", f.r.commands)
	}
}
func TestApplyUnchangedCommitsWithoutExternalChanges(t *testing.T) {
	f, files := runningApplyFixture(t)
	before := readFile(t, f.path)
	generation, err := db.GetSetting(context.Background(), f.conn, db.ApplyGeneration)
	if err != nil {
		t.Fatal(err)
	}
	// Force bookkeeping to be rebuilt from the generated snapshot.
	if err := db.SetSetting(context.Background(), f.conn, db.AppliedUsers, []int64{}); err != nil {
		t.Fatal(err)
	}
	f.r.checkFail = true // a data-only write must never invoke check
	if err := f.m.Apply(context.Background(), dataOnly); err != nil {
		t.Fatal(err)
	}
	assertNoCoreChanges(t, f, files)
	var remark string
	if err := f.conn.QueryRow("SELECT remark FROM users WHERE id=1").Scan(&remark); err != nil || remark != "data-only" {
		t.Fatal("mutation not committed", err)
	}
	active, err := db.GetSetting(context.Background(), f.conn, db.AppliedUsers)
	if err != nil || !reflect.DeepEqual(active, []int64{1}) {
		t.Fatal(active, err)
	}
	next, err := db.GetSetting(context.Background(), f.conn, db.ApplyGeneration)
	if err != nil || next == generation || len(next) != 43 {
		t.Fatal("generation not committed", err)
	}
	if !bytes.Equal(before, readFile(t, f.path)) {
		t.Fatal("config changed")
	}
	assertMissing(t, f.path+".pending")
	assertMissing(t, f.path+".tmp")
}
func TestApplyUnchangedFallbacksUseFullPipeline(t *testing.T) {
	for _, reason := range []string{"explicit-restart", "inactive", "config-bytes", "firewall-only", "missing-config"} {
		t.Run(reason, func(t *testing.T) {
			f, files := runningApplyFixture(t)
			mutation := Mutation(dataOnly)
			switch reason {
			case "explicit-restart":
				mutation = nil
			case "inactive":
				f.r.inactive = true
			case "config-bytes":
				// Even semantically equivalent JSON must be checked and replaced.
				if err := os.WriteFile(f.path, append(readFile(t, f.path), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "firewall-only":
				mutation = func(ctx context.Context, tx *sql.Tx) error {
					// Hopping affects firewall rules, but is absent from the core JSON.
					_, err := tx.ExecContext(ctx, `UPDATE inbounds SET options='{"sni":"www.bing.com","cert_mode":"self_signed","port_hopping":{"enabled":true,"start":20000,"end":21000}}'`)
					return err
				}
			case "missing-config":
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}
			var phases []string
			f.r.hook = func(_ context.Context, _ string, args []string) { phases = append(phases, args[0]) }
			files.renamed = func() { phases = append(phases, "replace") }
			f.m.firewall = observingFirewall{f.fw, func() { phases = append(phases, "firewall") }}
			if err := f.m.Apply(context.Background(), mutation); err != nil {
				t.Fatal(err)
			}
			if f.r.checks != 1 || f.r.restarts != 1 || len(f.fw.calls) != 1 || len(files.changes) == 0 {
				t.Fatal("fallback bypassed full pipeline")
			}
			want := []string{"check", "replace", "restart", "is-active", "firewall"}
			if reason == "inactive" {
				want = append([]string{"is-active"}, want...)
			}
			if !reflect.DeepEqual(phases, want) {
				t.Fatalf("pipeline order=%v want=%v", phases, want)
			}
			if reason == "firewall-only" && len(f.fw.calls[0].desired.Hops) != 1 {
				t.Fatal("hopping not applied")
			}
		})
	}
}
func TestApplyUnchangedCommitFailureDiscardsTransaction(t *testing.T) {
	f, files := runningApplyFixture(t)
	generation, err := db.GetSetting(context.Background(), f.conn, db.ApplyGeneration)
	if err != nil {
		t.Fatal(err)
	}
	err = f.m.Apply(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if err := dataOnly(ctx, tx); err != nil {
			return err
		}
		if err := db.SetSetting(ctx, tx, db.AppliedUsers, []int64{}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,admin_id,expires_at,created_at) VALUES('invalid-reference',999,0,0)`)
		return err
	})
	applyFailure(t, err, "singbox_apply_failed", false)
	assertNoCoreChanges(t, f, files)
	var remark string
	var count int
	if err := f.conn.QueryRow("SELECT remark FROM users WHERE id=1").Scan(&remark); err != nil || remark != "" {
		t.Fatal("failed commit leaked mutation", err)
	}
	if err := f.conn.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed commit leaked session", err)
	}
	next, err := db.GetSetting(context.Background(), f.conn, db.ApplyGeneration)
	if err != nil || next != generation {
		t.Fatal("failed commit changed generation", err)
	}
	active, err := db.GetSetting(context.Background(), f.conn, db.AppliedUsers)
	if err != nil || !reflect.DeepEqual(active, []int64{1}) {
		t.Fatal("failed commit changed applied users", err)
	}
	// Acquiring and committing another writer must succeed after the failure.
	if err := f.m.Apply(context.Background(), dataOnly); err != nil {
		t.Fatal(err)
	}
}
func TestApplyUnchangedRecoversPendingFirst(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "committed"}[committed], func(t *testing.T) {
			f, files := runningApplyFixture(t)
			generation, err := db.GetSetting(context.Background(), f.conn, db.ApplyGeneration)
			if err != nil {
				t.Fatal(err)
			}
			plan := appliedPlan(t, f)
			p := pendingApply{ID: generation, HadConfig: true, Previous: plan, Next: plan}
			if !committed {
				p.ID = "interrupted"
				if err := os.WriteFile(f.path+".bak", readFile(t, f.path), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.path, []byte(`{"interrupted":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path+".pending", raw, 0600); err != nil {
				t.Fatal(err)
			}
			f.r.checkFail = true
			if err := f.m.Apply(context.Background(), dataOnly); err != nil {
				t.Fatal(err)
			}
			wantRestarts := 0
			if !committed {
				wantRestarts = 1
			}
			if f.r.checks != 0 || f.r.restarts != wantRestarts || len(f.fw.calls) != wantRestarts {
				t.Fatal("pending recovery did not precede shortcut")
			}
			if committed && !reflect.DeepEqual(files.changes, []string{"remove:" + f.path + ".pending"}) {
				t.Fatal("committed pending touched config", files.changes)
			}
			assertMissing(t, f.path+".pending")
			assertMissing(t, f.path+".tmp")
			if err := f.m.Apply(context.Background(), dataOnly); err != nil {
				t.Fatal(err)
			}
			if f.r.restarts != wantRestarts {
				t.Fatal("stale generation rolled back next write")
			}
		})
	}
}
func appliedPlan(t *testing.T, f *applyFixture) firewall.Plan {
	t.Helper()
	raw, err := db.GetSetting(context.Background(), f.conn, db.AppliedFirewall)
	if err != nil {
		t.Fatal(err)
	}
	var p firewall.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestApplyUnchangedCancelledHealthDoesNotCommit(t *testing.T) {
	f, _ := runningApplyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.r.hook = func(_ context.Context, name string, args []string) {
		if name == "systemctl" && args[0] == "is-active" {
			cancel()
		}
	}
	err := f.m.Apply(ctx, dataOnly)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var remark string
	if err := f.conn.QueryRow("SELECT remark FROM users WHERE id=1").Scan(&remark); err != nil || remark != "" {
		t.Fatal("cancelled shortcut committed", err)
	}
}
