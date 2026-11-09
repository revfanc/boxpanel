package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openTestPath(t, filepath.Join(t.TempDir(), "boxpanel.db"))
}

func openTestPath(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return conn
}

func mustExec(t *testing.T, conn SettingsDB, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestOpenPragmasAndFilename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel # 中文?.db")
	conn := openTestPath(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created at the exact filename: %v", err)
	}
	// With no idle connections, each query must use a newly configured connection.
	conn.SetMaxIdleConns(0)
	for range 2 {
		for pragma, want := range map[string]string{
			"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000",
		} {
			var got string
			if err := conn.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s = %q, want %q", pragma, got, want)
			}
		}
	}
	if got := conn.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("max open connections = %d, want 1", got)
	}
}

func TestOpenFailures(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"", ":memory:", dir, filepath.Join(dir, "missing", "panel.db")} {
		t.Run(path, func(t *testing.T) {
			conn, err := Open(context.Background(), path)
			if err == nil {
				conn.Close()
				t.Fatal("expected an open error")
			}
			if conn != nil {
				t.Fatal("failed Open returned a connection")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, filepath.Join(dir, "cancelled.db")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open = %v", err)
	}
}

func TestEnableWALCancelledWhileLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	// Start in SQLite's default DELETE mode, with a writer preventing the WAL
	// transition. No busy timeout here: exercise our retry and its cancellation.
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	mustExec(t, writer, "CREATE TABLE lock_probe (value INTEGER)")
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mustExec(t, tx, "INSERT INTO lock_probe VALUES (1)")
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := enableWAL(ctx, reader); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked WAL initialization should respect context: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := enableWAL(context.Background(), reader); err != nil {
		t.Fatalf("WAL initialization after lock release: %v", err)
	}
}

func TestSchemaColumns(t *testing.T) {
	conn := openTestDB(t)
	tables := map[string][]string{
		"admins":   {"id", "username", "password_hash", "totp_secret", "created_at"},
		"sessions": {"id", "admin_id", "expires_at", "created_at"},
		"settings": {"key", "value"},
		"inbounds": {"id", "tag", "protocol", "port", "enabled", "remark", "options", "created_at", "updated_at"},
		"users":    {"id", "name", "uuid", "password", "sub_token", "enabled", "traffic_limit", "traffic_up", "traffic_down", "expire_at", "remark", "created_at"},
	}
	for table, want := range tables {
		rows, err := conn.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
		var count int
		if err := conn.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s should start empty: count=%d err=%v", table, count, err)
		}
	}
}

func TestSchemaConstraintsAndDefaults(t *testing.T) {
	conn := openTestDB(t)
	mustExec(t, conn, `INSERT INTO admins (id, username, password_hash, created_at) VALUES (1, 'admin', 'hash', 1)`)
	mustExec(t, conn, `INSERT INTO sessions (id, admin_id, expires_at, created_at) VALUES ('session', 1, 100, 1)`)
	mustExec(t, conn, `INSERT INTO inbounds (tag, protocol, port, options, created_at, updated_at) VALUES ('node', 'vless_reality', 443, '{}', 1, 1)`)
	mustExec(t, conn, `INSERT INTO users (name, uuid, password, sub_token, created_at) VALUES ('alice', 'uuid', 'password', 'token', 1)`)
	mustExec(t, conn, `INSERT INTO settings (key, value) VALUES ('web_port', '2053')`)
	for name, query := range map[string]string{
		"unique admin":      `INSERT INTO admins (username, password_hash, created_at) VALUES ('admin', 'hash', 1)`,
		"required hash":     `INSERT INTO admins (username, created_at) VALUES ('other', 1)`,
		"unique session":    `INSERT INTO sessions VALUES ('session', 1, 100, 1)`,
		"session FK":        `INSERT INTO sessions VALUES ('orphan', 999, 100, 1)`,
		"required expiry":   `INSERT INTO sessions (id, admin_id, created_at) VALUES ('other', 1, 1)`,
		"unique tag":        `INSERT INTO inbounds (tag, protocol, port, options, created_at, updated_at) VALUES ('node', 'hysteria2', 8443, '{}', 1, 1)`,
		"unique port":       `INSERT INTO inbounds (tag, protocol, port, options, created_at, updated_at) VALUES ('other', 'hysteria2', 443, '{}', 1, 1)`,
		"required options":  `INSERT INTO inbounds (tag, protocol, port, created_at, updated_at) VALUES ('other', 'hysteria2', 8443, 1, 1)`,
		"unique name":       `INSERT INTO users (name, uuid, password, sub_token, created_at) VALUES ('alice', 'new', 'password', 'new', 1)`,
		"unique uuid":       `INSERT INTO users (name, uuid, password, sub_token, created_at) VALUES ('bob', 'uuid', 'password', 'new', 1)`,
		"unique token":      `INSERT INTO users (name, uuid, password, sub_token, created_at) VALUES ('bob', 'new', 'password', 'token', 1)`,
		"required password": `INSERT INTO users (name, uuid, sub_token, created_at) VALUES ('bob', 'new', 'new', 1)`,
		"unique setting":    `INSERT INTO settings VALUES ('web_port', '443')`,
		"required value":    `INSERT INTO settings (key) VALUES ('missing')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := conn.Exec(query); err == nil {
				t.Fatal("expected constraint failure")
			}
		})
	}
	var enabled int
	var remark string
	if err := conn.QueryRow("SELECT enabled, remark FROM inbounds").Scan(&enabled, &remark); err != nil || enabled != 1 || remark != "" {
		t.Fatalf("inbound defaults: enabled=%d remark=%q err=%v", enabled, remark, err)
	}
	var limit, up, down, expiry int64
	if err := conn.QueryRow("SELECT enabled, traffic_limit, traffic_up, traffic_down, expire_at, remark FROM users").Scan(&enabled, &limit, &up, &down, &expiry, &remark); err != nil || enabled != 1 || limit != 0 || up != 0 || down != 0 || expiry != 0 || remark != "" {
		t.Fatalf("user defaults: enabled=%d limit=%d up=%d down=%d expiry=%d remark=%q err=%v", enabled, limit, up, down, expiry, remark, err)
	}
	var totp sql.NullString
	if err := conn.QueryRow("SELECT totp_secret FROM admins").Scan(&totp); err != nil || totp.Valid {
		t.Fatalf("optional TOTP = %v, err=%v", totp, err)
	}
	mustExec(t, conn, "DELETE FROM admins WHERE id = 1")
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("session cascade: count=%d err=%v", count, err)
	}
}
