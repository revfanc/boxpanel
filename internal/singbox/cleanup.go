package singbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
)

// CleanupFirewall is for uninstall after both units have been stopped. It never
// generates or replaces configuration, nor restarts services to recover pending
// apply. Keep the DB and recovery journal for retries or a later reinstall.
func (m *Manager) CleanupFirewall(ctx context.Context) error {
	if m.unsupported {
		return ErrUnsupported
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockApply(ctx, m.config+".lock")
	if err != nil {
		return fmt.Errorf("lock firewall cleanup: %w", err)
	}
	defer unlock()
	for _, unit := range []string{PanelUnit, SingboxUnit} {
		state, err := m.controller.State(ctx, unit)
		if err != nil {
			return err
		}
		if state.Running {
			return fmt.Errorf("stop %s before cleaning firewall rules", unit)
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("read cleanup snapshot: %w", err)
	}
	defer tx.Rollback()
	previous, err := storedPlan(ctx, tx)
	if err != nil {
		return err
	}
	raw, err := m.files.ReadFile(m.config + ".pending")
	if err == nil {
		var p pendingApply
		if json.Unmarshal(raw, &p) != nil || p.ID == "" || p.Previous.Validate() != nil || p.Next.Validate() != nil {
			return fmt.Errorf("invalid recovery journal; restore it before retrying uninstall")
		}
		previous = unionPlan(previous, unionPlan(p.Previous, p.Next))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read cleanup recovery journal: %w", err)
	}
	ports, err := db.GetSetting(ctx, tx, db.PanelFirewallPorts)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, port := range ports {
		previous = unionPlan(previous, firewall.Plan{Ports: []firewall.Port{{Port: port, Proto: "tcp"}}})
	}
	if err := m.firewall.Sync(ctx, previous, firewall.Plan{}); err != nil {
		return fmt.Errorf("clean managed firewall rules (keep files and retry uninstall): %w", err)
	}
	return nil
}
