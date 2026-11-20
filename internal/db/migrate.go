package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies embedded SQL in version order. The pending batch and its version
// records share one transaction, so a failure leaves the previous schema intact.
// The writer used by Open locks before reading migration history, so concurrent
// openers cannot apply a version twice.
func Migrate(ctx context.Context, conn *sql.DB) error {
	return migrate(ctx, conn, migrations)
}

const (
	serversMigration = "migrations/0004_servers.sql"
	agentsMigration  = "migrations/0005_agents.sql"
)

func migrate(ctx context.Context, pool *sql.DB, source fs.FS) error {
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

	conn, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve database migration connection: %w", err)
	}
	defer conn.Close()
	if slices.Contains(names, serversMigration) || slices.Contains(names, agentsMigration) {
		restore, err := prepareTableRebuild(ctx, conn)
		if err != nil {
			return err
		}
		defer restore()
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
		if err := applyMigration(ctx, tx, names[i], scripts[i]); err != nil {
			return fmt.Errorf("apply database migration %q (restore a backup or repair the migration before retrying): %w", names[i], err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", i+1, time.Now().Unix()); err != nil {
			return fmt.Errorf("record database migration %q: %w", names[i], err)
		}
	}
	if err := Commit(conn, tx); err != nil {
		return fmt.Errorf("commit database migrations: %w", err)
	}
	return nil
}

// CurrentSchema is the highest embedded migration number understood by this binary.
func CurrentSchema() int {
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	return len(names)
}

// SQLite requires foreign_keys to be changed outside a transaction. Pin the
// connection so no application query can use it with checks suspended.
func prepareTableRebuild(ctx context.Context, conn *sql.Conn) (func(), error) {
	var foreignKeys, legacyAlter int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return nil, fmt.Errorf("read foreign key mode: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA legacy_alter_table").Scan(&legacyAlter); err != nil {
		return nil, fmt.Errorf("read table rename mode: %w", err)
	}
	restore := func() {
		// Cleanup is needed even after a canceled migration. Discard any
		// connection whose original enforcement mode cannot be restored.
		_, err := conn.ExecContext(context.Background(), fmt.Sprintf(
			"PRAGMA foreign_keys=%d; PRAGMA legacy_alter_table=%d", foreignKeys, legacyAlter))
		if err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF; PRAGMA legacy_alter_table=ON"); err != nil {
		restore()
		return nil, fmt.Errorf("prepare database table rebuild: %w", err)
	}
	return restore, nil
}

func applyMigration(ctx context.Context, tx *sql.Tx, name, script string) error {
	var tables string
	switch name {
	case serversMigration:
		tables = "('inbounds','server_traffic_samples','alert_events')"
	case agentsMigration:
		tables = "('servers')"
	default:
		_, err := tx.ExecContext(ctx, script)
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE tbl_name IN `+tables+` AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return fmt.Errorf("read schema objects for table rebuild: %w", err)
	}
	var definitions []string
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema object: %w", err)
		}
		definitions = append(definitions, ddl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read schema objects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	for _, ddl := range definitions {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("restore schema object: %w", err)
		}
	}
	rows, err = tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check migrated foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("migrated database contains foreign key violations")
	}
	return rows.Err()
}
