package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (m ExitMonitor) samplePublicIP(ctx context.Context) error {
	ip, err := m.DetectIP(ctx)
	if err != nil {
		return fmt.Errorf("probe public IP: %w", err)
	}
	if !ValidPublicIP(ip) {
		return ErrPublicIP
	}
	address := ip.Unmap().String()
	_, err = m.Writer.ExecContext(ctx, "UPDATE servers SET public_ip=? WHERE is_local=1 AND public_ip<>?", address, address)
	if err != nil {
		return fmt.Errorf("save detected public IP: %w", err)
	}
	return nil
}

// ReconcilePublicIPs runs on the master. Failed Apply leaves the observation
// pending and preserves the last committed link revision on both endpoints.
func (m ExitMonitor) ReconcilePublicIPs(ctx context.Context) error {
	// A family switch changes every endpoint in a connected set of links.
	// Wait for compatible observations from its peers before switching any of them.
	const pending = `s.status<>'removed' AND s.public_ip<>'' AND s.public_ip<>s.exit_address
		AND ((instr(s.public_ip,':')>0)=(instr(s.exit_address,':')>0) OR NOT EXISTS (
			WITH RECURSIVE peers(id) AS (
				SELECT s.id UNION
				SELECT CASE WHEN l.entry_id=p.id THEN l.exit_id ELSE l.entry_id END
				FROM peers p JOIN exit_links l ON l.exit_id IS NOT NULL AND p.id IN (l.entry_id,l.exit_id)
			)
			SELECT 1 FROM peers JOIN servers peer ON peer.id=peers.id
			WHERE (instr(coalesce(nullif(peer.public_ip,''),peer.exit_address),':')>0)
				<> (instr(s.public_ip,':')>0)
		))`
	applyAddresses := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE servers AS s SET exit_address=public_ip WHERE "+pending)
		if err != nil {
			return fmt.Errorf("update managed link addresses: %w", err)
		}
		return nil
	}
	// Check impact inside the writer transaction so concurrent link edits
	// cannot commit a local configuration change through the DB-only path.
	needsApply := errors.New("local link requires apply")
	err := m.update(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var local bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM servers s WHERE "+pending+`
			AND EXISTS (SELECT 1 FROM exit_links l JOIN servers home ON home.is_local=1
			WHERE l.exit_id IS NOT NULL AND home.id IN (l.entry_id,l.exit_id) AND s.id IN (l.entry_id,l.exit_id)))`).Scan(&local)
		if err != nil {
			return fmt.Errorf("check local link address changes: %w", err)
		}
		if local {
			return needsApply
		}
		return applyAddresses(ctx, tx)
	})
	if errors.Is(err, needsApply) {
		return m.Manager.Apply(ctx, applyAddresses)
	}
	return err
}
