package singbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/reality"
)

type applyRunner struct {
	t                       *testing.T
	checkFail               bool
	restartFails            int
	unhealthy               bool
	restarts, stops, checks int
	hook                    func(context.Context, string, []string)
	realCheck               bool
	commands                [][]string
}

func (r *applyRunner) LookPath(name string) (string, error) { return name, nil }
func (r *applyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string{name}, args...))
	if r.hook != nil {
		r.hook(ctx, name, args)
	}
	if len(args) > 0 && args[0] == "check" {
		r.checks++
		if r.checkFail {
			return []byte("invalid configuration: alice-password-secret, subscription-token-secret"), errors.New("check failed")
		}
		if r.realCheck {
			return (host.Native{}).Run(ctx, name, args...)
		}
		return nil, nil
	}
	if name == "journalctl" {
		return []byte("bind failed\nalice-password-secret\npassword=deleted-password\n"), nil
	}
	if name != "systemctl" {
		r.t.Fatalf("unexpected command %s %v", name, args)
	}
	switch args[0] {
	case "restart":
		r.restarts++
		if r.restartFails > 0 {
			r.restartFails--
			return []byte("secret error"), errors.New("restart failed")
		}
		return nil, nil
	case "stop":
		r.stops++
		return nil, nil
	case "is-active":
		if r.unhealthy && r.restarts == 1 {
			return []byte("failed"), errors.New("inactive")
		}
		return []byte("active\n"), nil
	}
	r.t.Fatalf("unexpected systemctl %v", args)
	return nil, nil
}

type planCall struct{ previous, desired firewall.Plan }
type applyFirewall struct {
	calls []planCall
	fails int
}

func (f *applyFirewall) Sync(ctx context.Context, previous, desired firewall.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.calls = append(f.calls, planCall{previous, desired})
	if f.fails > 0 {
		f.fails--
		return errors.New("firewall failed")
	}
	return nil
}

type applyFixture struct {
	m    *Manager
	conn *sql.DB
	r    *applyRunner
	fw   *applyFirewall
	path string
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(dir, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.SetSetting(ctx, conn, db.BlockBittorrent, true); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO users (id,name,uuid,password,sub_token,created_at) VALUES (1,'alice','11111111-2222-3333-4444-555555555555','alice-password-secret','subscription-token-secret',0)`); err != nil {
		t.Fatal(err)
	}
	r := &applyRunner{t: t}
	fw := &applyFirewall{}
	binary := filepath.Join(dir, "fake-sing-box")
	if err := os.WriteFile(binary, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(conn, ApplyOptions{OS: "linux", Binary: binary, Config: filepath.Join(dir, "sing-box.json"), Runner: r, Firewall: fw, HealthTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return &applyFixture{m, conn, r, fw, m.config}
}
func changeBT(ctx context.Context, tx *sql.Tx) error {
	return db.SetSetting(ctx, tx, db.BlockBittorrent, false)
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected absent file %s: %v", path, err)
	}
}
func (f *applyFixture) bt(t *testing.T, want bool) {
	t.Helper()
	got, err := db.GetSetting(context.Background(), f.conn, db.BlockBittorrent)
	if err != nil || got != want {
		t.Fatalf("BT=%v error=%v want=%v", got, err, want)
	}
}
func applyFailure(t *testing.T, err error, code string, recoveryFailed bool) *ApplyError {
	t.Helper()
	var failure *ApplyError
	if !errors.As(err, &failure) || failure.Code != code || failure.RollbackFailed != recoveryFailed {
		t.Fatalf("unexpected failure: %#v %v", failure, err)
	}
	return failure
}

func TestApplyCheckFailureRollsBackDB(t *testing.T) {
	f := newApplyFixture(t)
	f.r.checkFail = true
	failure := applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_check_failed", false)
	f.bt(t, true)
	assertMissing(t, f.path)
	assertMissing(t, f.path+".tmp")
	if f.r.restarts != 0 || len(f.fw.calls) != 0 {
		t.Fatal("failed check reached external state")
	}
	if strings.Contains(failure.Detail, "alice-password-secret") || strings.Contains(failure.Detail, "subscription-token-secret") {
		t.Fatal("check output leaked credentials")
	}
	if _, err := db.GetSetting(context.Background(), f.conn, db.ClashAPISecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("runtime secret committed after failed check")
	}
}

func TestApplyRestartRollbackAndRecoveryFailure(t *testing.T) {
	for _, failures := range []int{1, 2} {
		t.Run(string(rune('0'+failures)), func(t *testing.T) {
			f := newApplyFixture(t)
			if err := f.m.Apply(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			original := readFile(t, f.path)
			f.r.restarts = 0
			f.r.restartFails = failures
			failure := applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_restart_failed", failures == 2)
			f.bt(t, true)
			if string(readFile(t, f.path)) != string(original) || string(readFile(t, f.path+".bak")) != string(original) {
				t.Fatal("old configuration not restored")
			}
			if f.r.restarts != 2 {
				t.Fatal("rollback did not restart old config")
			}
			if !strings.Contains(failure.Detail, "bind failed") || strings.Contains(failure.Detail, "alice-password-secret") || strings.Contains(failure.Detail, "deleted-password") {
				t.Fatalf("bad diagnostic %q", failure.Detail)
			}
			if failures == 2 {
				_ = readFile(t, f.path+".pending")
				if err := f.m.Apply(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			}
			assertMissing(t, f.path+".pending")
			assertMissing(t, f.path+".tmp")
		})
	}
}

func TestApplyLivenessFailureAndFirstApply(t *testing.T) {
	f := newApplyFixture(t)
	f.r.unhealthy = true
	applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_restart_failed", false)
	f.bt(t, true)
	assertMissing(t, f.path)
	if f.r.stops != 1 {
		t.Fatal("failed first apply must stop service")
	}
}

func TestApplyFirewallFailureRestoresDBFileAndRules(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	original := readFile(t, f.path)
	f.fw.fails = 1
	applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_firewall_failed", false)
	f.bt(t, true)
	if string(readFile(t, f.path)) != string(original) {
		t.Fatal("firewall failure kept new config")
	}
	if len(f.fw.calls) != 3 {
		t.Fatal("missing firewall compensation")
	}
}

func TestApplySuccessAndStartupRegenerate(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), changeBT); err != nil {
		t.Fatal(err)
	}
	f.bt(t, false)
	first := readFile(t, f.path)
	secret, err := db.GetSetting(context.Background(), f.conn, db.ClashAPISecret)
	if err != nil || len(secret) != 43 {
		t.Fatal("missing stable secret")
	}
	port, err := db.GetSetting(context.Background(), f.conn, db.ClashAPIPort)
	if err != nil || port < 1 {
		t.Fatal("missing control port")
	}
	// Simulate process restart and an absent on-disk config (DB is authoritative).
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(f.conn, ApplyOptions{OS: "linux", Binary: f.m.binary, Config: f.path, Runner: f.r, Firewall: f.fw})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, f.path)) != string(first) {
		t.Fatal("startup did not regenerate from stable runtime values")
	}
	if f.r.checks != 2 || f.r.restarts != 2 || len(f.fw.calls) != 2 {
		t.Fatal("startup bypassed apply pipeline")
	}
	for _, path := range []string{f.path, f.path + ".lock"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe permissions")
		}
	}
}

func TestApplySerializesTwoMutations(t *testing.T) {
	f := newApplyFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	secondEntered := make(chan struct{})
	var running, maxRunning atomic.Int32
	mutation := func(first bool) Mutation {
		return func(ctx context.Context, tx *sql.Tx) error {
			active := running.Add(1)
			defer running.Add(-1)
			if active > maxRunning.Load() {
				maxRunning.Store(active)
			}
			if first {
				close(entered)
				<-release
			} else {
				close(secondEntered)
			}
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT traffic_up FROM users WHERE id=1").Scan(&n); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE users SET traffic_up=? WHERE id=1", n+1)
			return err
		}
	}
	results := make(chan error, 2)
	go func() { results <- f.m.Apply(context.Background(), mutation(true)) }()
	<-entered
	// A separate Manager (as used by a CLI process) must also take the file lock.
	other, err := NewManager(f.conn, ApplyOptions{OS: "linux", Binary: f.m.binary, Config: f.path, Runner: f.r, Firewall: f.fw})
	if err != nil {
		t.Fatal(err)
	}
	go func() { results <- other.Apply(context.Background(), mutation(false)) }()
	select {
	case <-secondEntered:
		t.Fatal("concurrent mutation entered before first finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := f.conn.QueryRow("SELECT traffic_up FROM users WHERE id=1").Scan(&n); err != nil || n != 2 || maxRunning.Load() != 1 {
		t.Fatal("lost or concurrent mutation")
	}
}

func TestApplyCancellationStillRestores(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	original := readFile(t, f.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.r.hook = func(_ context.Context, name string, args []string) {
		if name == "systemctl" && args[0] == "restart" {
			cancel()
		}
	}
	applyFailure(t, f.m.Apply(ctx, changeBT), "singbox_restart_failed", false)
	f.bt(t, true)
	if string(readFile(t, f.path)) != string(original) {
		t.Fatal("cancelled request prevented rollback")
	}
}

func TestApplyHysteriaCertificatesAndPortPlan(t *testing.T) {
	f := newApplyFixture(t)
	mutation := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO inbounds (id,tag,protocol,port,options,created_at,updated_at) VALUES (1,'hy2','hysteria2',8443,'{"sni":"www.bing.com","cert_mode":"self_signed","port_hopping":{"enabled":true,"start":20000,"end":30000}}',0,0)`)
		return err
	}
	if err := f.m.Apply(context.Background(), mutation); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			TLS struct {
				CertificatePath string `json:"certificate_path"`
				KeyPath         string `json:"key_path"`
			}
		}
	}
	if err := json.Unmarshal(readFile(t, f.path), &config); err != nil {
		t.Fatal(err)
	}
	tls := config.Inbounds[0].TLS
	firstCert := readFile(t, tls.CertificatePath)
	if info, err := os.Stat(tls.KeyPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions")
	}
	fingerprints, err := db.GetSetting(context.Background(), f.conn, db.Hysteria2Fingerprints)
	if err != nil || len(fingerprints["1"]) != 64 {
		t.Fatal("fingerprint not saved")
	}
	if got := f.fw.calls[0].desired; !reflect.DeepEqual(got, firewall.Plan{Ports: []firewall.Port{{Port: 8443, Proto: "udp"}}, Hops: []firewall.Hop{{Start: 20000, End: 30000, Port: 8443}}}) {
		t.Fatalf("wrong plan: %+v", got)
	}
	if err := f.m.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, tls.CertificatePath)) != string(firstCert) || len(f.fw.calls[1].desired.Hops) != 1 {
		t.Fatal("startup rotated TLS or lost hopping")
	}
	if err := f.m.Apply(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM inbounds")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	last := f.fw.calls[len(f.fw.calls)-1]
	if len(last.previous.Hops) != 1 || len(last.desired.Hops) != 0 || len(last.desired.Ports) != 0 {
		t.Fatal("deletion did not clean ports/hops")
	}
}

func TestApplyRealSingboxCheck(t *testing.T) {
	binary, err := filepath.Abs("../../.tools/bin/sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); errors.Is(err, fs.ErrNotExist) {
		t.Skip(".tools/bin/sing-box is unavailable; run make tools")
	}
	f := newApplyFixture(t)
	f.m.binary = binary
	f.r.realCheck = true
	keys, err := reality.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	realityOptions, _ := json.Marshal(confgen.RealityOptions{ServerName: "www.example.com", HandshakeServer: "www.example.com", HandshakePort: 443, PrivateKey: keys.PrivateKey, PublicKey: keys.PublicKey, ShortIDs: []string{"1234567890abcdef"}})
	mutation := func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbounds (tag,protocol,port,options,created_at,updated_at) VALUES ('reality','vless_reality',443,?,0,0)`, string(realityOptions)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO inbounds (tag,protocol,port,options,created_at,updated_at) VALUES ('hy2','hysteria2',8443,'{"sni":"www.bing.com","cert_mode":"self_signed","obfs_password":"test-obfs","port_hopping":{"enabled":true,"start":20000,"end":30000}}',0,0)`)
		return err
	}
	if err := f.m.Apply(context.Background(), mutation); err != nil {
		var e *ApplyError
		errors.As(err, &e)
		t.Fatalf("real check failed: %v diagnostic=%+v", err, e)
	}
	if f.r.checks != 1 || f.r.restarts != 1 {
		t.Fatal("real check did not run through apply")
	}
}

func TestApplyUnsupportedHasNoSideEffects(t *testing.T) {
	for _, opts := range []ApplyOptions{{OS: "darwin"}, {OS: "linux", Dev: true}} {
		opts.Config = filepath.Join(t.TempDir(), "absent", "config")
		m, err := NewManager(nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Startup(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := m.Restart(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		if _, err := m.Config(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		if _, err := m.Logs(context.Background(), 200); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
}

type failingApplyFiles struct {
	nativeApplyFiles
	failWrite  string
	failRename bool
	failRemove string
}

func (f *failingApplyFiles) WriteFile(path string, data []byte, mode fs.FileMode) error {
	if path == f.failWrite {
		return fs.ErrPermission
	}
	return f.nativeApplyFiles.WriteFile(path, data, mode)
}
func (f *failingApplyFiles) Rename(from, to string) error {
	if f.failRename {
		return fs.ErrPermission
	}
	return f.nativeApplyFiles.Rename(from, to)
}
func (f *failingApplyFiles) Remove(path string) error {
	if path == f.failRemove {
		return fs.ErrPermission
	}
	return f.nativeApplyFiles.Remove(path)
}

func TestApplyFileFailures(t *testing.T) {
	for _, phase := range []string{"stage", "backup", "rename", "restore"} {
		t.Run(phase, func(t *testing.T) {
			f := newApplyFixture(t)
			if err := f.m.Apply(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			original := readFile(t, f.path)
			files := &failingApplyFiles{}
			switch phase {
			case "stage":
				files.failWrite = f.path + ".tmp"
			case "backup":
				files.failWrite = f.path + ".bak"
			case "rename":
				files.failRename = true
			case "restore":
				files.failWrite = f.path
				f.r.restartFails = 1
			}
			f.m.files = files
			err := f.m.Apply(context.Background(), changeBT)
			if err == nil {
				t.Fatal("ignored file failure")
			}
			f.bt(t, true)
			if phase == "restore" {
				applyFailure(t, err, "singbox_restart_failed", true)
				if f.r.stops != 1 {
					t.Fatal("unrestorable config must stop new service")
				}
				if string(readFile(t, f.path+".bak")) != string(original) {
					t.Fatal("lost known-good backup")
				}
			} else if string(readFile(t, f.path)) != string(original) {
				t.Fatal("file failure changed active config")
			}
			assertMissing(t, f.path+".tmp")
		})
	}
}

func TestApplyGenerationFailureDoesNotCommit(t *testing.T) {
	f := newApplyFixture(t)
	err := f.m.Apply(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO inbounds(tag,protocol,port,options,created_at,updated_at) VALUES('invalid','unsupported',443,'{}',0,0)`)
		return err
	})
	applyFailure(t, err, "singbox_check_failed", false)
	var count int
	if err := f.conn.QueryRow("SELECT count(*) FROM inbounds").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid inbound committed")
	}
	if f.r.checks != 0 || f.r.restarts != 0 {
		t.Fatal("generation failure executed commands")
	}
}

func TestApplyFirewallRecoveryAlsoFails(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.fw.fails = 2
	applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_firewall_failed", true)
	f.bt(t, true)
	_ = readFile(t, f.path+".pending")
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, f.path+".pending")
}

func TestStartupRecoversInterruptedApplyBeforeNewCheck(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	original := readFile(t, f.path)
	// Crash after replacement and partial firewall sync, before DB commit.
	p := pendingApply{ID: "uncommitted", HadConfig: true, Next: firewall.Plan{Ports: []firewall.Port{{Port: 8443, Proto: "udp"}}, Hops: []firewall.Hop{{Start: 20000, End: 30000, Port: 8443}}}}
	raw, _ := json.Marshal(p)
	for path, data := range map[string][]byte{f.path + ".bak": original, f.path: []byte(`{"crashed":true}`), f.path + ".pending": raw} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.r.checkFail = true
	applyFailure(t, f.m.Startup(context.Background()), "singbox_check_failed", false)
	if string(readFile(t, f.path)) != string(original) {
		t.Fatal("startup did not recover before failed new check")
	}
	last := f.fw.calls[len(f.fw.calls)-1]
	if len(last.previous.Hops) != 1 || len(last.desired.Hops) != 0 {
		t.Fatal("interrupted hops were not cleaned up")
	}
	assertMissing(t, f.path+".pending")
	f.bt(t, true)
}

func TestCommittedPendingJournalDoesNotRollback(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	files := &failingApplyFiles{failRemove: f.path + ".pending"}
	f.m.files = files
	if err := f.m.Apply(context.Background(), changeBT); err != nil {
		t.Fatal(err)
	}
	f.bt(t, false)
	committed := readFile(t, f.path)
	_ = readFile(t, f.path+".pending")
	files.failRemove = ""
	f.r.checkFail = true
	restarts := f.r.restarts
	applyFailure(t, f.m.Apply(context.Background(), nil), "singbox_check_failed", false)
	if f.r.restarts != restarts || string(readFile(t, f.path)) != string(committed) {
		t.Fatal("committed generation was rolled back")
	}
	assertMissing(t, f.path+".pending")
	f.bt(t, false)
}

func TestStartupBeforeCoreInstallation(t *testing.T) {
	f := newApplyFixture(t)
	if err := os.Remove(f.m.binary); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.r.commands) != 0 {
		t.Fatal("fresh startup called system commands")
	}
	if err := os.WriteFile(f.path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Startup(context.Background()); err == nil {
		t.Fatal("missing installed binary treated as fresh setup")
	}
}

func TestApplyCommitFailureDiscardsOpenSQLiteTransaction(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	original := readFile(t, f.path)
	err := f.m.Apply(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if err := changeBT(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		// SQLite accepts the write but refuses COMMIT. database/sql marks Tx done,
		// so merely deferring tx.Rollback would leak this transaction into the pool.
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,admin_id,expires_at,created_at) VALUES('invalid-reference',999,0,0)`)
		return err
	})
	applyFailure(t, err, "singbox_apply_failed", false)
	f.bt(t, true)
	if string(readFile(t, f.path)) != string(original) {
		t.Fatal("commit failure did not restore config")
	}
	var sessions int
	if err := f.conn.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("failed commit leaked uncommitted session")
	}
	// A following transaction can acquire the writer and apply normally.
	if err := f.m.Apply(context.Background(), changeBT); err != nil {
		t.Fatal(err)
	}
	f.bt(t, false)
}
