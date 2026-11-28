package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServerDetailsMigrationPreservesTrafficAndUnusedTokens(t *testing.T) {
	ctx := t.Context()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v7.db")+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy := fstest.MapFS{}
	for _, name := range names[:7] {
		data, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		legacy[name] = &fstest.MapFile{Data: data}
	}
	if err := migrate(ctx, conn, legacy); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `INSERT INTO servers (id,name,public_host,status,alert_preferences)
		VALUES (2,'edge','edge.example','online','{"quota_gb":2,"billing_day":4,"interface":"","billing_mode":"out","events":{"server_quota_90":true,"server_quota_80":false}}');
		INSERT INTO users (id,name,uuid,password,sub_token,created_at,traffic_up,traffic_down,traffic_epoch)
		VALUES (1,'legacy','id','secret','token',1,123,456,42);
		INSERT INTO server_user_traffic VALUES (2,1,10,30,40,100,200,123);
		INSERT INTO agent_join_tokens (id,token_hash,name,expires_at) VALUES (5,zeroblob(32),'open',2000000000);`)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var values [8]int64
	if err := conn.QueryRow(`SELECT epoch,up,down,total_up,total_down,user_epoch,cycle_up,cycle_down FROM server_user_traffic`).
		Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7]); err != nil || values != [8]int64{10, 30, 40, 100, 200, 42, 0, 0} {
		t.Fatalf("unexpected migrated accounting values: %v, %v", values, err)
	}
	var unused, unknownUptime, unknownMetrics, quota90 bool
	var quota, day int
	if err := conn.QueryRow(`SELECT (SELECT server_id IS NULL FROM agent_join_tokens WHERE id=5),
		uptime IS NULL,monitoring IS NULL,json_extract(alert_preferences,'$.events.server_quota_90'),
		json_extract(alert_preferences,'$.quota_gb'),json_extract(alert_preferences,'$.billing_day') FROM servers WHERE id=2`).
		Scan(&unused, &unknownUptime, &unknownMetrics, &quota90, &quota, &day); err != nil ||
		!unused || !unknownUptime || !unknownMetrics || quota90 || quota != 2 || day != 4 {
		t.Fatalf("invalid migrated availability or preferences: %v %v %v %v %d %d: %v", unused, unknownUptime, unknownMetrics, quota90, quota, day, err)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "panel.db")
	conn := openTestPath(t, path)
	mustExec(t, conn, `INSERT INTO admins (username, password_hash, created_at) VALUES ('admin', 'hash', 1)`)
	if err := SetSetting(ctx, conn, WebPort, 2053); err != nil {
		t.Fatal(err)
	}
	var appliedAt int64
	if err := conn.QueryRow("SELECT applied_at FROM schema_migrations WHERE version = 1").Scan(&appliedAt); err != nil || appliedAt <= 0 {
		t.Fatalf("migration timestamp = %d, err=%v", appliedAt, err)
	}
	for range 2 {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	conn = openTestPath(t, path)
	var count int
	var timestamp int64
	if err := conn.QueryRow("SELECT count(*), min(applied_at) FROM schema_migrations").Scan(&count, &timestamp); err != nil || count != CurrentSchema() || timestamp != appliedAt {
		t.Fatalf("migration history changed: count=%d timestamp=%d err=%v", count, timestamp, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM admins WHERE username = 'admin'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("admin lost on reopen: count=%d err=%v", count, err)
	}
	if got, err := GetSetting(ctx, conn, WebPort); err != nil || got != 2053 {
		t.Fatalf("setting lost on reopen: %d, %v", got, err)
	}
}

func TestMigrationOrderRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t)
	mustExec(t, conn, `INSERT INTO settings VALUES ('keep', 'true')`)
	source := fstest.MapFS{}
	for i := 1; i <= CurrentSchema(); i++ {
		source[fmt.Sprintf("migrations/%04d_done.sql", i)] = &fstest.MapFile{Data: []byte("SELECT 1;")}
	}
	source[fmt.Sprintf("migrations/%04d_next.sql", CurrentSchema()+1)] = &fstest.MapFile{
		Data: []byte("CREATE TABLE upgrade_probe (value TEXT); INSERT INTO upgrade_probe VALUES ('before');"),
	}
	last := fmt.Sprintf("migrations/%04d_next.sql", CurrentSchema()+2)
	source[last] = &fstest.MapFile{Data: []byte("UPDATE upgrade_probe SET value = 'after'; INSERT INTO missing_table VALUES (1);")}
	if err := migrate(ctx, conn, source); err == nil || !strings.Contains(err.Error(), last) {
		t.Fatalf("expected contextual migration failure, got %v", err)
	}
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'upgrade_probe'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL not rolled back: count=%d err=%v", count, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != CurrentSchema() {
		t.Fatalf("version not rolled back: count=%d err=%v", count, err)
	}
	source[last].Data = []byte("UPDATE upgrade_probe SET value = 'after';")
	for range 2 {
		if err := migrate(ctx, conn, source); err != nil {
			t.Fatal(err)
		}
	}
	var value string
	if err := conn.QueryRow("SELECT value FROM upgrade_probe").Scan(&value); err != nil || value != "after" {
		t.Fatalf("migration order: value=%q err=%v", value, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != CurrentSchema()+2 {
		t.Fatalf("versions after retry: count=%d err=%v", count, err)
	}
	if err := conn.QueryRow("SELECT value FROM settings WHERE key = 'keep'").Scan(&value); err != nil || value != "true" {
		t.Fatalf("existing data lost: value=%q err=%v", value, err)
	}
}

func TestMigrationRejectsInvalidSequence(t *testing.T) {
	for name, source := range map[string]fstest.MapFS{
		"no files":  {},
		"gap":       {"migrations/0003_next.sql": {Data: []byte("SELECT 1;")}},
		"bad name":  {"migrations/initial.sql": {Data: []byte("SELECT 1;")}},
		"empty SQL": {"migrations/0001_initial.sql": {Data: []byte(" \n")}},
		"duplicate version": {
			"migrations/0001_first.sql":  {Data: []byte("SELECT 1;")},
			"migrations/0001_second.sql": {Data: []byte("SELECT 1;")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := migrate(context.Background(), openTestDB(t), source); err == nil {
				t.Fatal("expected invalid migration sequence error")
			}
		})
	}
}

func TestOpenRejectsUnsupportedHistory(t *testing.T) {
	for _, version := range []int{0, CurrentSchema(), CurrentSchema() + 1, 99} {
		path := filepath.Join(t.TempDir(), "panel.db")
		conn := openTestPath(t, path)
		mustExec(t, conn, "DELETE FROM schema_migrations")
		mustExec(t, conn, "INSERT INTO schema_migrations (version,applied_at) VALUES (?,1)", version)
		conn.Close()
		opened, err := Open(context.Background(), path)
		if err == nil {
			opened.Close()
			t.Fatalf("accepted unsupported migration history version %d", version)
		}
	}
}

func TestConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	start := make(chan struct{})
	results := make(chan error, 4)
	for range 4 {
		go func() {
			<-start
			conn, err := Open(context.Background(), path)
			if err == nil {
				err = conn.Close()
			}
			results <- err
		}()
	}
	close(start)
	for range 4 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	conn := openTestPath(t, path)
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != CurrentSchema() {
		t.Fatalf("concurrent migrations: count=%d err=%v", count, err)
	}
}

func TestTrafficMigrationPreservesExistingUser(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	initial, err := migrations.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, conn, fstest.MapFS{"migrations/0001_initial.sql": {Data: initial}}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `INSERT INTO users(name,uuid,password,sub_token,traffic_up,traffic_down,created_at) VALUES('legacy','uuid','password','token',123,456,1)`)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var up, down, day, at, epoch int64
	if err := conn.QueryRow("SELECT traffic_up,traffic_down,traffic_reset_day,traffic_reset_at,traffic_epoch FROM users").Scan(&up, &down, &day, &at, &epoch); err != nil {
		t.Fatal(err)
	}
	if up != 123 || down != 456 || day != 0 || at != 0 || epoch != 0 {
		t.Fatal(up, down, day, at, epoch)
	}
	for _, day := range []int{-1, 29} {
		if _, err := conn.Exec("UPDATE users SET traffic_reset_day=?", day); err == nil {
			t.Fatal("invalid reset day accepted", day)
		}
	}
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
}

func TestAlertsMigrationPreservesUserTrafficAndIdentity(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	legacy := fstest.MapFS{}
	for _, filename := range []string{"migrations/0001_initial.sql", "migrations/0002_user_traffic.sql"} {
		data, err := migrations.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		legacy[filename] = &fstest.MapFile{Data: data}
	}
	if err := migrate(ctx, conn, legacy); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `INSERT INTO users(name,uuid,password,sub_token,created_at,traffic_up,traffic_epoch) VALUES ('legacy','id','secret','token',1,123,42)`)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var up, epoch, identity int
	if err := conn.QueryRow("SELECT traffic_up,traffic_epoch,alert_identity FROM users").Scan(&up, &epoch, &identity); err != nil || up != 123 || epoch != 42 || identity != 42 {
		t.Fatalf("legacy user changed: %d %d %d, %v", up, epoch, identity, err)
	}
	for _, table := range []string{"server_traffic_samples", "alert_events", "alert_outbox", "alert_attempts"} {
		var count int
		if err := conn.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s not empty after migrating: %d, %v", table, count, err)
		}
	}
}

func TestExitAddressMigrationPreservesDistinctOrigins(t *testing.T) {
	ctx := t.Context()
	conn := legacyServersDB(t)
	migrateToVersion(t, conn, 14)
	mustExec(t, conn, `INSERT INTO servers (id,name,public_host,exit_address) VALUES
		(2,'standard','8.8.8.8','8.8.8.8'),(3,'domain','cdn.example','8.8.4.4'),
		(4,'custom','1.1.1.1','1.0.0.1'),(5,'empty','example.org',''),
		(6,'legacy-v4','1.0.0.2',''),(7,'legacy-v6','2606:4700::1','')`)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{2: "8.8.8.8", 3: "8.8.4.4", 4: "1.0.0.1", 5: "", 6: "1.0.0.2", 7: "2606:4700::1"} {
		var address, detected string
		if err := conn.QueryRow("SELECT exit_address,public_ip FROM servers WHERE id=?", id).Scan(&address, &detected); err != nil || address != want || detected != "" {
			t.Fatalf("server %d address=%s detected=%s err=%v", id, address, detected, err)
		}
	}
}

func migrateToVersion(t *testing.T, conn *sql.DB, version int) {
	t.Helper()
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, path := range files[:version] {
		data, err := migrations.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		old[path] = &fstest.MapFile{Data: data}
	}
	if err := migrate(t.Context(), conn, old); err != nil {
		t.Fatal(err)
	}
}

func TestExternalExitMigrationPreservesExistingLinks(t *testing.T) {
	conn := legacyServersDB(t)
	migrateToVersion(t, conn, 16)
	mustExec(t, conn, `INSERT INTO servers(id,name,public_host) VALUES(2,'edge','edge.example.org'),(3,'other','other.example.org');
        INSERT INTO exit_links(id,entry_id,exit_id,revision,port,password,mode,failure,rules,checked_at,available,policy_down,incident_at)
        VALUES(17,1,2,4,20301,printf('%044d',1),'split','direct','{"presets":["ai"]}',123,0,1,117),
        (42,3,2,1,20302,printf('%044d',2),'all','block','{}',0,NULL,0,0);
        DELETE FROM exit_links WHERE id=42;`)
	if err := Migrate(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := conn.QueryRow(`SELECT id=17 AND exit_id=2 AND revision=4 AND port=20301 AND password=printf('%044d',1)
        AND mode='split' AND failure='direct' AND rules='{"presets":["ai"]}' AND checked_at=123 AND available=0
        AND policy_down=1 AND incident_at=117 AND external_proxy IS NULL FROM exit_links WHERE entry_id=1`).Scan(&preserved); err != nil || !preserved {
		t.Fatal("migration changed a managed link or its health history", err)
	}
	mustExec(t, conn, `INSERT INTO exit_links(entry_id,external_proxy,port,password,mode,failure,rules)
        VALUES(3,'{"type":"http","host":"proxy.example.org","port":443}',443,'','all','block','{}')`)
	var id int64
	if err := conn.QueryRow("SELECT id FROM exit_links WHERE entry_id=3").Scan(&id); err != nil || id <= 42 {
		t.Fatal("migration reused a retired link identity", err)
	}
}
