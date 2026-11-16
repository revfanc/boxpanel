package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

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
	if err := conn.QueryRow("SELECT count(*), min(applied_at) FROM schema_migrations").Scan(&count, &timestamp); err != nil || count != 2 || timestamp != appliedAt {
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
	source := fstest.MapFS{
		"migrations/0001_initial.sql": {Data: []byte("SELECT 1;")},
		"migrations/0002_traffic.sql": {Data: []byte("SELECT 1;")},
		"migrations/0003_next.sql":    {Data: []byte("CREATE TABLE upgrade_probe (value TEXT); INSERT INTO upgrade_probe VALUES ('before');")},
		"migrations/0004_next.sql":    {Data: []byte("UPDATE upgrade_probe SET value = 'after'; INSERT INTO missing_table VALUES (1);")},
	}
	if err := migrate(ctx, conn, source); err == nil || !strings.Contains(err.Error(), "0004_next.sql") {
		t.Fatalf("expected contextual migration failure, got %v", err)
	}
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'upgrade_probe'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL not rolled back: count=%d err=%v", count, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatalf("version not rolled back: count=%d err=%v", count, err)
	}
	source["migrations/0004_next.sql"].Data = []byte("UPDATE upgrade_probe SET value = 'after';")
	for range 2 {
		if err := migrate(ctx, conn, source); err != nil {
			t.Fatal(err)
		}
	}
	var value string
	if err := conn.QueryRow("SELECT value FROM upgrade_probe").Scan(&value); err != nil || value != "after" {
		t.Fatalf("migration order: value=%q err=%v", value, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 4 {
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
	for _, version := range []int{0, 3, 99} {
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
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
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
