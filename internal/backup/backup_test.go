package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
)

const testPass = "test-only-passphrase-1234"

type runner struct{}

func (runner) LookPath(s string) (string, error)                      { return s, nil }
func (runner) Run(context.Context, string, ...string) ([]byte, error) { return []byte("active"), nil }

type control struct{}

func (control) Reload(context.Context) error          { return nil }
func (control) Enable(context.Context, string) error  { return nil }
func (control) Start(context.Context, string) error   { return nil }
func (control) Stop(context.Context, string) error    { return nil }
func (control) Restart(context.Context, string) error { return nil }
func (control) State(context.Context, string) (singbox.ServiceState, error) {
	return singbox.ServiceState{Running: true}, nil
}

type firewallStub struct{}

func (firewallStub) Sync(context.Context, firewall.Plan, firewall.Plan) error { return nil }

func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	p := app.Paths{DataDir: dir, Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "certs/panel.crt"), KeyFile: filepath.Join(dir, "certs/panel.key")}
	if _, err := app.Initialize(ctx, app.InitOptions{Paths: p, PublicIP: "127.0.0.1", Port: 2053}); err != nil {
		t.Fatal(err)
	}
	conn, err := app.OpenExisting(ctx, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	keys, err := reality.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"server_name": "www.example.com", "handshake_server": "www.example.com", "handshake_port": 443, "private_key": keys.PrivateKey, "public_key": keys.PublicKey, "short_ids": []string{"0123456789abcdef"}, "fingerprint": "chrome"})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO inbounds(id,tag,protocol,port,options,created_at,updated_at) VALUES(1,'reality','vless_reality',443,?,1,1)`, []any{string(raw)}},
		{`INSERT INTO inbounds(id,tag,protocol,port,options,created_at,updated_at) VALUES(2,'hy2','hysteria2',8443,?,1,1)`, []any{`{"sni":"www.bing.com","cert_mode":"self_signed","up_mbps":0,"down_mbps":0,"port_hopping":{"enabled":false,"start":20000,"end":30000}}`}},
		{`INSERT INTO users(name,uuid,password,sub_token,traffic_up,traffic_down,created_at) VALUES('alice','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','retained-password','retained-subscription-token',123,456,1)`, nil},
		{`INSERT INTO sessions(id,admin_id,expires_at,created_at) VALUES('revoked-after-restore',1,9999999999,1)`, nil},
	} {
		if _, err := conn.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	apply(t, conn, dir)
	for _, name := range []string{"sing-box.json.bak", "sing-box.json.pending", "sing-box.json.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not archived"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, conn
}
func apply(t *testing.T, conn *sql.DB, dir string) []byte {
	t.Helper()
	config := filepath.Join(dir, "sing-box.json")
	m, err := singbox.NewManager(conn, singbox.ApplyOptions{OS: "linux", Config: config, Binary: "/test/sing-box", Runner: runner{}, Controller: control{}, Firewall: firewallStub{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func archive(t *testing.T, conn *sql.DB, dir, pass string) []byte {
	t.Helper()
	b, err := Create(context.Background(), conn, filepath.Join(dir, "certs"), "test-version", pass, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func decoded(t *testing.T, b []byte, pass string) Archive {
	t.Helper()
	a, err := Decode(b, pass, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRoundTripPreservesIdentityAndConfig(t *testing.T) {
	dir, conn := fixture(t)
	before, _ := os.ReadFile(filepath.Join(dir, "sing-box.json"))
	encrypted := archive(t, conn, dir, testPass)
	if bytes.Contains(encrypted, []byte("retained-password")) {
		t.Fatal("unencrypted credential")
	}
	a := decoded(t, encrypted, testPass)
	if len(a.Files) < 5 || a.Manifest.Schema != db.CurrentSchema() {
		t.Fatal("incomplete backup")
	}
	for n := range a.Files {
		if n != "boxpanel.db" && !strings.HasPrefix(n, "certs/") {
			t.Fatalf("unexpected backup file %s", n)
		}
	}
	stage, err := Stage(context.Background(), a, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	restored, err := db.Open(context.Background(), filepath.Join(stage, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, table := range []string{"admins", "users", "inbounds", "settings", "schema_migrations"} {
		dump := func(c *sql.DB) [][]any {
			order := "id"
			if table == "settings" {
				order = "key"
			}
			if table == "schema_migrations" {
				order = "version"
			}
			rows, e := c.Query("SELECT * FROM " + table + " ORDER BY " + order)
			if e != nil {
				t.Fatal(e)
			}
			defer rows.Close()
			cols, _ := rows.Columns()
			var result [][]any
			for rows.Next() {
				values := make([]any, len(cols))
				dest := make([]any, len(cols))
				for i := range values {
					dest[i] = &values[i]
				}
				if e = rows.Scan(dest...); e != nil {
					t.Fatal(e)
				}
				result = append(result, values)
			}
			return result
		}
		if !reflect.DeepEqual(dump(conn), dump(restored)) {
			t.Fatalf("%s changed", table)
		}
	}
	var count int
	if err = restored.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal("sessions survived")
	}
	for name, want := range a.Files {
		if name == "boxpanel.db" {
			continue
		}
		got, e := os.ReadFile(filepath.Join(stage, name))
		if e != nil || !bytes.Equal(got, want) {
			t.Fatalf("certificate changed: %s", name)
		}
	}
	after := apply(t, restored, stage)
	if !bytes.Equal(before, bytes.ReplaceAll(after, []byte(stage), []byte(dir))) {
		t.Fatal("generated config changed")
	}
}

func TestAuthenticatedArchiveRejectsDamage(t *testing.T) {
	dir, conn := fixture(t)
	b := archive(t, conn, dir, testPass)
	if _, err := Decode(b, "incorrect-passphrase-123", true); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	// Header/salt/nonce/payload/tag and truncated footer are all authenticated.
	for _, i := range []int{0, len(magic) - 1, len(magic), len(magic) + 16, len(b) / 2, len(b) - 1} {
		bad := bytes.Clone(b)
		bad[i] ^= 1
		if _, err := Decode(bad, testPass, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted corruption at %d: %v", i, err)
		}
	}
	for _, n := range []int{0, 1, len(magic), len(b) - 1, len(b) / 2} {
		if _, err := Decode(b[:n], testPass, true); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	other := archive(t, conn, dir, testPass)
	if bytes.Equal(b, other) {
		t.Fatal("reused nonce/salt")
	}
	if _, err := Decode(archive(t, conn, dir, ""), "", true); err == nil {
		t.Fatal("HTTP accepts plaintext")
	}
}

func rawTar(t *testing.T, headers []*tar.Header, payloads [][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(payloads) > i {
			if _, err := tw.Write(payloads[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestArchiveEntryAndManifestValidation(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "certs/../../outside", "certs/../outside", "certs/a/b", "certs/./a", "certs\\a", "C:/file", "sing-box.json"} {
		t.Run(name, func(t *testing.T) {
			b := rawTar(t, []*tar.Header{{Name: name, Mode: 0600, Typeflag: tar.TypeReg}}, nil)
			if _, err := Decode(b, "", false); err == nil {
				t.Fatal("unsafe name accepted")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeChar, tar.TypeFifo} {
		b := rawTar(t, []*tar.Header{{Name: "certs/link", Typeflag: kind, Linkname: "../outside", Mode: 0600}}, nil)
		if _, err := Decode(b, "", false); err == nil {
			t.Fatal("link/special accepted")
		}
	}
	missing := rawTar(t, []*tar.Header{{Name: "boxpanel.db", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}}, [][]byte{{1}})
	if _, err := Decode(missing, "", false); err == nil {
		t.Fatal("missing manifest accepted")
	}
	duplicate := rawTar(t, []*tar.Header{{Name: "manifest.json", Typeflag: tar.TypeReg}, {Name: "manifest.json", Typeflag: tar.TypeReg}}, nil)
	if _, err := Decode(duplicate, "", false); err == nil {
		t.Fatal("duplicate accepted")
	}
	dir, conn := fixture(t)
	a := decoded(t, archive(t, conn, dir, ""), "")
	a.Files["boxpanel.db"][0] ^= 1
	b, err := pack(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(b, "", false); err == nil {
		t.Fatal("bad SHA accepted")
	}
	plain := archive(t, conn, dir, "")
	for _, b := range [][]byte{plain[:len(plain)-1], append(bytes.Clone(plain), 1), append(bytes.Clone(plain), plain...)} {
		if _, err := Decode(b, "", false); err == nil {
			t.Fatal("gzip truncation/trailing data accepted")
		}
	}
}

func TestSchemaValidationAndOldMigration(t *testing.T) {
	dir, conn := fixture(t)
	a := decoded(t, archive(t, conn, dir, ""), "")
	a.Manifest.Schema = db.CurrentSchema() + 1
	if _, err := Stage(context.Background(), a, t.TempDir()); !errors.Is(err, ErrNewer) {
		t.Fatal(err)
	}
	a.Manifest.Schema = 1
	if _, err := Stage(context.Background(), a, t.TempDir()); !errors.Is(err, ErrInvalid) {
		t.Fatal("manifest/schema mismatch", err)
	}
	// Build a v1 snapshot by removing the v2 columns and v3 tables.
	if _, err := conn.Exec(`ALTER TABLE users DROP COLUMN traffic_reset_day; ALTER TABLE users DROP COLUMN traffic_reset_at; ALTER TABLE users DROP COLUMN traffic_epoch; ALTER TABLE users DROP COLUMN alert_identity; DROP TABLE alert_outbox; DROP TABLE alert_attempts; DROP TABLE alert_events; DROP TABLE server_traffic_samples; DELETE FROM schema_migrations WHERE version>1`); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "old.db")
	if _, err := conn.Exec("VACUUM INTO ?", snapshot); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	a.Files["boxpanel.db"] = b
	stage, err := Stage(context.Background(), a, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	migrated, err := db.Open(context.Background(), filepath.Join(stage, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var day int
	if err = migrated.QueryRow("SELECT traffic_reset_day FROM users").Scan(&day); err != nil || day != 0 {
		t.Fatal("old migration failed", err)
	}
}

func TestRestoreRollbackAtEachStep(t *testing.T) {
	for _, failure := range []string{"none", "stop", "apply", "start", "recover"} {
		t.Run(failure, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "data")
			stage := filepath.Join(parent, "stage")
			for _, dir := range []string{target, stage} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(filepath.Join(target, "identity"), []byte("old"), 0600)
			os.WriteFile(filepath.Join(stage, "identity"), []byte("new"), 0600)
			stops, recovered := 0, false
			hooks := Hooks{Stop: func(context.Context) error {
				stops++
				if failure == "stop" && stops == 1 {
					return errors.New("stop")
				}
				return nil
			}, Apply: func(context.Context) error {
				b, e := os.ReadFile(filepath.Join(target, "identity"))
				if e != nil || string(b) != "new" {
					t.Fatal("apply before swap")
				}
				if failure == "apply" || failure == "recover" {
					return errors.New("apply")
				}
				return nil
			}, Start: func(context.Context) error {
				if failure == "start" {
					return errors.New("start")
				}
				return nil
			}, Recover: func(ctx context.Context, exists bool) error {
				recovered = true
				if !exists || ctx.Err() != nil {
					t.Fatal("bad recovery context")
				}
				if failure == "recover" {
					return errors.New("recover")
				}
				return nil
			}}
			saved, err := Restore(context.Background(), target, stage, hooks)
			b, e := os.ReadFile(filepath.Join(target, "identity"))
			if e != nil {
				t.Fatal(e)
			}
			if failure == "none" {
				if err != nil || string(b) != "new" || recovered {
					t.Fatal(err)
				}
				old, _ := os.ReadFile(filepath.Join(saved, "identity"))
				if string(old) != "old" {
					t.Fatal("lost original")
				}
			} else {
				if err == nil || string(b) != "old" || !recovered {
					t.Fatal("rollback failed", err)
				}
			}
		})
	}
}

func TestAutomaticRotationAndFailureStatus(t *testing.T) {
	dir, conn := fixture(t)
	s := Service{DB: conn, DataDir: dir, Version: "test"}
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if err := s.Tick(context.Background(), now.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Tick(context.Background(), now.AddDate(0, 0, 9)); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(dir, "backups/boxpanel-auto-*.tar.gz"))
	if err != nil || len(names) != 7 {
		t.Fatal(len(names), err)
	}
	for _, n := range names {
		info, _ := os.Stat(n)
		if info.Mode().Perm() != 0600 {
			t.Fatal("unsafe file mode")
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "backups"))
	if info.Mode().Perm() != 0700 {
		t.Fatal("unsafe directory mode")
	}
	status, err := s.Status()
	if err != nil || status.Result != "success" || status.LastSuccess != now.AddDate(0, 0, 9).Unix() {
		t.Fatal(status, err)
	}
	conn.Close()
	if err := s.Tick(context.Background(), now.AddDate(0, 0, 10)); err == nil {
		t.Fatal("missing failure")
	}
	failed, err := s.Status()
	if err != nil || failed.Result != "failed" || failed.LastSuccess != status.LastSuccess {
		t.Fatal(failed, err)
	}
	names, _ = filepath.Glob(filepath.Join(dir, "backups/boxpanel-auto-*.tar.gz"))
	if len(names) != 7 {
		t.Fatal("failed attempt pruned backups")
	}
}
func TestBackupRejectsLinksAndOutputOverwrite(t *testing.T) {
	dir, conn := fixture(t)
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(dir, "certs/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), conn, filepath.Join(dir, "certs"), "test", "", time.Now()); err == nil {
		t.Fatal("symlink archived")
	}
	output := filepath.Join(t.TempDir(), "out")
	if err := os.Symlink(outside, output); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(output, []byte("overwrite")); err == nil {
		t.Fatal("followed output symlink")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "outside" {
		t.Fatal("outside modified")
	}
}
func TestPinnedMaterialMissingOrModified(t *testing.T) {
	dir, conn := fixture(t)
	a := decoded(t, archive(t, conn, dir, ""), "")
	for name := range a.Files {
		if strings.HasPrefix(name, "certs/hy2-") && strings.HasSuffix(name, ".crt") {
			delete(a.Files, name)
			delete(a.Manifest.Files, name)
			if _, err := Stage(context.Background(), a, t.TempDir()); err == nil {
				t.Fatal("missing pinned certificate accepted")
			}
			return
		}
	}
	t.Fatal("no pinned material")
}

func TestSnapshotWaitsForWriterAndCapturesOneCommit(t *testing.T) {
	dir, conn := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writer, err := db.Open(ctx, filepath.Join(dir, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE users SET traffic_up=1000,traffic_down=2000"); err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		b, e := Create(ctx, conn, filepath.Join(dir, "certs"), "test", "", time.Now())
		done <- b
		errs <- e
	}()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b := <-done
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	a := decoded(t, b, "")
	stage, err := Stage(ctx, a, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	restored, err := db.Open(ctx, filepath.Join(stage, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var up, down int64
	if err = restored.QueryRow("SELECT traffic_up,traffic_down FROM users").Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if !(up == 123 && down == 456) && !(up == 1000 && down == 2000) {
		t.Fatal("torn snapshot")
	}
}
