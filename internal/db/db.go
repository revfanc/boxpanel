// Package db provides SQLite connections, embedded migrations and typed settings.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Open opens a filesystem database and applies all pending migrations. The parent
// directory must already exist. The caller owns the returned DB and must close it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("open database: provide a non-empty filesystem path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	// Encode paths as URIs so '?' and '#' in filenames cannot alter the options.
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	query := url.Values{}
	// Driver options apply to every connection, including replacements in the pool.
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Set("_txlock", "immediate")
	uri.RawQuery = query.Encode()
	conn, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite has one writer. Keep access serialized within this process, while
	// busy_timeout handles competing CLI/server connections to the same file.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect to database (check the file and directory permissions): %w", err)
	}
	if err := enableWAL(ctx, conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable database WAL (check for another process holding a database lock): %w", err)
	}
	if err := Migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return conn, nil
}

func enableWAL(ctx context.Context, conn *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var mode string
		err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil {
			if mode != "wal" {
				return fmt.Errorf("SQLite returned journal mode %q instead of WAL", mode)
			}
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			return err
		}
		// Switching a fresh database to WAL can return BUSY without invoking
		// SQLite's busy handler. Retry only this contention, with a bounded wait.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for database WAL lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
