package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

// Stage validates/migrates only a private sibling directory. No live database is
// opened or replaced. The returned directory belongs to the caller.
func Stage(ctx context.Context, a Archive, parent string) (string, error) {
	if a.Manifest.Schema > db.CurrentSchema() {
		return "", ErrNewer
	}
	dir, err := os.MkdirTemp(parent, ".boxpanel-restore-")
	if err != nil {
		return "", fmt.Errorf("stage restore: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	if err = os.Mkdir(filepath.Join(dir, "certs"), 0700); err != nil {
		return "", err
	}
	for n, b := range a.Files {
		// Decode is normally the entry point, but keep Stage independently confined.
		if n != "boxpanel.db" && (filepath.Dir(n) != "certs" || filepath.Base(n) == ".." || filepath.Base(n) == ".") {
			return "", ErrInvalid
		}
		if err = os.WriteFile(filepath.Join(dir, n), b, 0600); err != nil {
			return "", fmt.Errorf("stage restore file: %w", err)
		}
	}
	uri := url.URL{Scheme: "file", Path: filepath.Join(dir, "boxpanel.db"), RawQuery: "mode=ro"}
	inspect, e := sql.Open("sqlite", uri.String())
	if e != nil {
		return "", ErrInvalid
	}
	var originalSchema, count int
	e = inspect.QueryRowContext(ctx, "SELECT max(version), count(*) FROM schema_migrations").Scan(&originalSchema, &count)
	inspect.Close()
	if originalSchema > db.CurrentSchema() {
		return "", ErrNewer
	}
	if e != nil || originalSchema != a.Manifest.Schema || count != originalSchema {
		return "", ErrInvalid
	}
	conn, err := app.OpenExisting(ctx, filepath.Join(dir, "boxpanel.db"))
	if err != nil {
		return "", ErrInvalid
	}
	defer conn.Close()
	var schema int
	if err = conn.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&schema); err != nil || schema != db.CurrentSchema() {
		return "", ErrInvalid
	}
	var integrity string
	if err = conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return "", ErrInvalid
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return "", ErrInvalid
	}
	bad := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if bad || rowErr != nil {
		return "", ErrInvalid
	}
	cfg, err := app.LoadConfig(ctx, conn)
	if err != nil || cfg.PublicHost != a.Manifest.PublicHost {
		return "", ErrInvalid
	}
	if err = validateMaterials(ctx, conn, a.Files); err != nil {
		return "", err
	}
	if _, err = app.AdminUsername(ctx, conn); err != nil {
		return "", ErrInvalid
	}
	if _, err = conn.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return "", fmt.Errorf("revoke restored sessions: %w", err)
	}
	if _, err = conn.ExecContext(ctx, "DELETE FROM agent_join_tokens"); err != nil {
		return "", fmt.Errorf("revoke restored agent join tokens: %w", err)
	}
	// Flush staged WAL before publishing. No open connection crosses the rename.
	if _, err = conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return "", err
	}
	if err = conn.Close(); err != nil {
		return "", err
	}
	if err = syncTree(dir); err != nil {
		return "", err
	}
	ok = true
	return dir, nil
}

// Hooks are deliberately outside database transactions. Stop must stop/wait for
// every live panel and core process. Apply uses the existing validated Manager.
// Recover must reconcile firewall/service state after restoring the old directory.
type Hooks struct {
	Stop    func(context.Context) error
	Apply   func(context.Context) error
	Start   func(context.Context) error
	Recover func(context.Context, bool) error
}

func Restore(ctx context.Context, target, stage string, h Hooks) (saved string, err error) {
	if h.Stop == nil || h.Apply == nil || h.Start == nil || h.Recover == nil {
		return "", fmt.Errorf("restore: lifecycle hooks required")
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if filepath.Dir(stage) != filepath.Dir(target) || stage == target {
		return "", fmt.Errorf("restore: stage must be a sibling directory")
	}
	stageInfo, e := os.Lstat(stage)
	if e != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalid
	}
	info, e := os.Lstat(target)
	exists := e == nil
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	if exists && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("restore: data directory must not be a symbolic link")
	}
	lock := target + ".restore-lock"
	if err = os.Mkdir(lock, 0700); err != nil {
		return "", fmt.Errorf("restore busy; confirm no restore is running before removing %s", lock)
	}
	defer os.Remove(lock)
	// Even a partial stop failure must restore the previous service state.
	if err = h.Stop(ctx); err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if e := h.Recover(recovery, exists); e != nil {
			return "", fmt.Errorf("restore stop failed and recovery failed; check systemd and retry")
		}
		return "", fmt.Errorf("stop restore services: %w", err)
	}
	switched := false
	defer func() {
		if err == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if e := h.Stop(recovery); e != nil {
			err = fmt.Errorf("restore rollback could not stop services; preserve %s and inspect systemd", saved)
			return
		}
		if switched {
			// Keep the failed candidate for diagnosis; do not destroy either data set.
			failed := stage + ".failed"
			if e := os.Rename(target, failed); e != nil {
				err = fmt.Errorf("restore rollback could not move candidate; preserve %s", saved)
				return
			}
		}
		if saved != "" {
			if e := os.Rename(saved, target); e != nil {
				err = fmt.Errorf("restore rollback could not restore original directory at %s", saved)
				return
			}
		}
		if e := h.Recover(recovery, exists); e != nil {
			err = fmt.Errorf("restore data rolled back but services/firewall need repair; run boxpanel apply and inspect systemd")
			return
		}
		_ = syncDir(filepath.Dir(target))
	}()
	if exists {
		// Reserve a unique name, then rename the whole original directory.
		reserved, e := os.MkdirTemp(filepath.Dir(target), filepath.Base(target)+".pre-restore-"+time.Now().UTC().Format("20060102T150405Z")+"-")
		if e != nil {
			return "", e
		}
		if e = os.Remove(reserved); e != nil {
			return "", e
		}
		if e = os.Rename(target, reserved); e != nil {
			return "", e
		}
		saved = reserved
	}
	if err = os.Rename(stage, target); err != nil {
		return saved, err
	}
	switched = true
	if err = syncDir(filepath.Dir(target)); err != nil {
		return saved, err
	}
	if err = h.Apply(ctx); err != nil {
		return saved, fmt.Errorf("apply restored configuration: %w", err)
	}
	if err = h.Start(ctx); err != nil {
		return saved, fmt.Errorf("start restored panel: %w", err)
	}
	return saved, nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func syncTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return syncDir(p)
		}
		f, e := os.OpenFile(p, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		defer f.Close()
		return f.Sync()
	})
}
