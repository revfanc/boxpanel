package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/subscription"
)

// Make the archived database with the original SQL and column layout.
func legacySnapshot(t *testing.T, source *sql.DB, version int) []byte {
	t.Helper()
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "source.db")
	if _, err := source.Exec("VACUUM INTO ?", snapshot); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "legacy.db")
	legacy, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := legacy.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)")
	files := []string{"0001_initial.sql", "0002_user_traffic.sql", "0003_alerts.sql"}
	for i, file := range files[:version] {
		raw, err := os.ReadFile(filepath.Join("..", "db", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		exec(string(raw))
		exec("INSERT INTO schema_migrations VALUES(?,1)", i+1)
	}
	exec("ATTACH DATABASE ? AS src", snapshot)
	exec(`INSERT INTO admins SELECT * FROM src.admins;
		INSERT INTO sessions SELECT * FROM src.sessions;
		INSERT INTO settings SELECT * FROM src.settings WHERE key <> 'node_role';
		INSERT INTO inbounds SELECT id,tag,protocol,port,enabled,remark,options,created_at,updated_at FROM src.inbounds;`)
	if version == 1 {
		exec(`INSERT INTO users SELECT id,name,uuid,password,sub_token,enabled,traffic_limit,traffic_up,traffic_down,expire_at,remark,created_at FROM src.users`)
	} else {
		exec(`INSERT INTO users SELECT * FROM src.users;
			INSERT INTO settings SELECT 'alert_preferences', alert_preferences FROM src.servers WHERE is_local=1;`)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func stageV03(t *testing.T, conn *sql.DB, dir string) (string, *sql.DB) {
	t.Helper()
	a := decoded(t, archive(t, conn, dir, ""), "")
	a.Manifest.Schema = 3
	a.Manifest.Version = "v0.3.0"
	a.Files["boxpanel.db"] = legacySnapshot(t, conn, 3)
	a.Manifest.Files["boxpanel.db"] = fmt.Sprintf("%x", sha256.Sum256(a.Files["boxpanel.db"]))
	raw, err := pack(a)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := Stage(context.Background(), decoded(t, raw, ""), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := db.Open(context.Background(), filepath.Join(stage, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	return stage, restored
}

func TestV03RestoreConfigAndSubscriptionGoldens(t *testing.T) {
	dir, conn := fixture(t)
	ctx := context.Background()
	if _, err := conn.Exec(`DELETE FROM inbounds WHERE id=2;
		UPDATE inbounds SET tag='vless-reality-443',remark='香港 Reality',options='{"server_name":"www.example.com","handshake_server":"www.example.com","handshake_port":443,"private_key":"EBEREREREREREREREREREREREREREREREREREREREVE","public_key":"e06Qm75__kTEZaIgA31gjuNYl9Me-XLwf3SJLLD3PxM","short_ids":["0123456789abcdef","12345678"],"fingerprint":"chrome"}' WHERE id=1;
		INSERT INTO users(id,name,uuid,password,sub_token,created_at) VALUES(2,'bob','bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','test-bob-password','test-bob-token',1);`); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		db.SetSetting(ctx, conn, db.PublicHost, "203.0.113.8"),
		db.SetSetting(ctx, conn, db.ClashAPIPort, 19090),
		db.SetSetting(ctx, conn, db.ClashAPISecret, "test-only-clash-secret"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	stage, restored := stageV03(t, conn, dir)
	wantGolden(t, apply(t, restored, stage), "../confgen/testdata/vless.golden")
	d := loadSubscription(t, restored, stage)
	for _, format := range []subscription.Format{subscription.Singbox, subscription.Clash, subscription.Base64} {
		got, err := subscription.Render(d.Snapshot, format)
		if err != nil {
			t.Fatal(err)
		}
		wantGolden(t, got, fmt.Sprintf("testdata/v03_%s.golden", format))
	}
}

func wantGolden(t *testing.T, got []byte, path string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output changed from %s", path)
	}
}

func loadSubscription(t *testing.T, conn *sql.DB, dir string) subscription.Data {
	t.Helper()
	store := subscription.Store{DB: conn, Certificate: func(id int64, sni string) ([]byte, error) {
		hash := sha256.Sum256([]byte(sni))
		return os.ReadFile(filepath.Join(dir, "certs", fmt.Sprintf("hy2-%d-%x.crt", id, hash[:8])))
	}}
	token := "retained-subscription-token"
	d, err := store.Load(context.Background(), 0, &token)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestV03RestorePreservesBothProtocolsAndPinnedCertificate(t *testing.T) {
	dir, conn := fixture(t)
	before, err := os.ReadFile(filepath.Join(dir, "sing-box.json"))
	if err != nil {
		t.Fatal(err)
	}
	original := loadSubscription(t, conn, dir)
	stage, restored := stageV03(t, conn, dir)
	// Compare with the original path to model an in-place upgrade. The
	// restore also validates the actual certificate/key pair in its own stage.
	if err := os.Remove(filepath.Join(dir, "sing-box.json.pending")); err != nil {
		t.Fatal(err)
	}
	if after := apply(t, restored, dir); !bytes.Equal(before, after) {
		t.Fatal("service config changed after the schema upgrade")
	}
	migrated := loadSubscription(t, restored, stage)
	if len(migrated.Nodes) != 2 || original.Nodes[1].Fingerprint == "" ||
		migrated.Nodes[1].Fingerprint != original.Nodes[1].Fingerprint || migrated.User != original.User {
		t.Fatal("subscription identity changed")
	}
	for _, format := range []subscription.Format{subscription.Singbox, subscription.Clash, subscription.Base64} {
		want, err := subscription.Render(original.Snapshot, format)
		if err != nil {
			t.Fatal(err)
		}
		got, err := subscription.Render(migrated.Snapshot, format)
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("%s bytes changed: %v", format, err)
		}
	}
}
