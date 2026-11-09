package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/revfanc/boxpanel/internal/db"
)

type PortOpener interface {
	Open(context.Context, int, string) error
}

// OpenPanelFirewall records intent before touching the host, so even a partial
// firewall failure can be cleaned up by uninstall. Replay only manages instances
// enrolled by the installer; ordinary development/CLI instances are unchanged.
func OpenPanelFirewall(ctx context.Context, conn *sql.DB, port int, enroll bool, opener PortOpener) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("open panel firewall: invalid port; choose 1–65535")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record panel firewall: %w", err)
	}
	defer tx.Rollback()
	ports, err := db.GetSetting(ctx, tx, db.PanelFirewallPorts)
	if errors.Is(err, sql.ErrNoRows) {
		if !enroll {
			return nil
		}
		ports, err = []int{}, nil
	}
	if err != nil {
		return err
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("invalid recorded panel port; restore panel_firewall_ports before retrying")
		}
	}
	if !slices.Contains(ports, port) {
		ports = append(ports, port)
	}
	if err := db.SetSetting(ctx, tx, db.PanelFirewallPorts, ports); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record panel firewall: %w", err)
	}
	if err := opener.Open(ctx, port, "tcp"); err != nil {
		return fmt.Errorf("open panel firewall (check firewall permissions and retry): %w", err)
	}
	return nil
}
