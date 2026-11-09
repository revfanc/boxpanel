package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies embedded SQL in version order. The pending batch and its version
// records share one transaction, so a failure leaves the previous schema intact.
// Use a connection returned by Open, whose transactions acquire a write lock before
// reading migration history; concurrent openers then cannot apply a version twice.
func Migrate(ctx context.Context, conn *sql.DB) error {
	return migrate(ctx, conn, migrations)
}

func migrate(ctx context.Context, conn *sql.DB, source fs.FS) error {
	names, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list database migrations: %w", err)
	}
	if len(names) == 0 {
		return fmt.Errorf("load database migrations: no SQL files; rebuild the binary")
	}
	scripts := make([]string, len(names))
	for i, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		prefix := fmt.Sprintf("%04d_", i+1)
		if !strings.HasPrefix(base, prefix) || base == prefix+".sql" {
			return fmt.Errorf("load database migration %q: expected %04d_name.sql; fix the embedded migration sequence", name, i+1)
		}
		script, err := fs.ReadFile(source, name)
		if err != nil {
			return fmt.Errorf("read database migration %q: %w", name, err)
		}
		if strings.TrimSpace(string(script)) == "" {
			return fmt.Errorf("load database migration %q: SQL is empty; restore the migration", name)
		}
		scripts[i] = string(script)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create database migration history: %w", err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read database migration history: %w", err)
	}
	current := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan database migration version: %w", err)
		}
		if version != current+1 || version > len(scripts) {
			rows.Close()
			return fmt.Errorf("database migration history has unsupported version %d: use a compatible binary or restore a backup", version)
		}
		current = version
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read database migration versions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close database migration history: %w", err)
	}
	for i := current; i < len(scripts); i++ {
		if _, err := tx.ExecContext(ctx, scripts[i]); err != nil {
			return fmt.Errorf("apply database migration %q (restore a backup or repair the migration before retrying): %w", names[i], err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", i+1, time.Now().Unix()); err != nil {
			return fmt.Errorf("record database migration %q: %w", names[i], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database migrations: %w", err)
	}
	return nil
}
