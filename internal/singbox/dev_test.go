package singbox

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestExplicitDevelopmentDBOnly(t *testing.T) {
	f := newApplyFixture(t)
	if _, err := NewManager(f.conn, ApplyOptions{OS: "linux", DevDBOnly: true, Config: f.path}); err == nil {
		t.Fatal("production accepted development substitute")
	}
	m, err := NewManager(f.conn, ApplyOptions{OS: "darwin", Dev: true, DevDBOnly: true, Config: f.path, Runner: f.r, Firewall: f.fw})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Apply(ctx, changeBT); err != nil {
		t.Fatal(err)
	}
	f.bt(t, false)
	err = m.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := db.SetSetting(ctx, tx, db.BlockBittorrent, true); err != nil {
			return err
		}
		return errors.New("mutation failed")
	})
	if err == nil {
		t.Fatal("failed mutation committed")
	}
	f.bt(t, false)
	// A rejected deferred COMMIT must not return a still-open transaction to
	// the single-connection pool (the same SQLite edge case as production).
	err = m.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,admin_id,expires_at,created_at) VALUES ('invalid',999,1,1)"); err != nil {
			return err
		}
		return db.SetSetting(ctx, tx, db.BlockBittorrent, true)
	})
	if err == nil {
		t.Fatal("invalid FK committed")
	}
	f.bt(t, false)
	if err := m.Apply(ctx, changeBT); err != nil {
		t.Fatal("connection unusable after rejected commit", err)
	}
	if !errors.Is(m.Apply(ctx, nil), ErrUnsupported) || !errors.Is(m.Restart(ctx), ErrUnsupported) {
		t.Fatal("dev claimed real apply success")
	}
	if err := m.Startup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".lock", ".tmp", ".pending", ".bak"} {
		assertMissing(t, f.path+suffix)
	}
	if len(f.r.commands) != 0 || len(f.fw.calls) != 0 {
		t.Fatal("dev had system effects")
	}
}
