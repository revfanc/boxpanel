package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/revfanc/boxpanel/internal/exitlink"
)

var (
	AgentExit = SettingKey[exitlink.Configuration]{"agent_exit"}
	ExitProbe = SettingKey[exitlink.Probe]{"exit_probe"}
	ExitUsage = SettingKey[ExitCheckpoint]{"exit_usage"}
	ExitEver  = SettingKey[bool]{"exit_ever"}
)

// ExitCheckpoint keeps the kernel baseline and the cumulative totals together.
// Linux counters persist across process restarts but reset at boot.
type ExitCheckpoint struct {
	BootID string          `json:"boot_id"`
	Raw    exitlink.Totals `json:"raw"`
	Total  exitlink.Totals `json:"total"`
}

func SetExitAddress(ctx context.Context, q SettingsDB, id int64, address string) error {
	if _, err := q.ExecContext(ctx, "UPDATE servers SET exit_address=? WHERE id=?", address, id); err != nil {
		return fmt.Errorf("store exit link address: %w", err)
	}
	return nil
}

func LocalExitConfig(ctx context.Context, q *sql.Tx, local int64) (exitlink.Configuration, error) {
	role, err := GetSetting(ctx, q, NodeRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return exitlink.Configuration{}, err
	}
	if role == RoleAgent {
		mirror, err := GetSetting(ctx, q, AgentExit)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return mirror, err
	}
	return ServerExitConfig(ctx, q, local)
}

func ServerExitConfig(ctx context.Context, q *sql.Tx, id int64) (exitlink.Configuration, error) {
	var config exitlink.Configuration
	rows, err := q.QueryContext(ctx, `SELECT l.id,l.revision,l.entry_id,l.port,l.password,l.mode,l.failure,l.rules,
		entry.exit_address,coalesce(target.exit_address,''),l.external_proxy FROM exit_links l
		JOIN servers entry ON entry.id=l.entry_id LEFT JOIN servers target ON target.id=l.exit_id
		WHERE l.entry_id=? OR l.exit_id=? ORDER BY l.id`, id, id)
	if err != nil {
		return config, fmt.Errorf("read server exit links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var out exitlink.Outbound
		var entryID int64
		var source, rules string
		var external sql.NullString
		if err := rows.Scan(&out.ID, &out.Version, &entryID, &out.Port, &out.Password, &out.Mode, &out.Failure, &rules, &source, &out.Address, &external); err != nil {
			return config, fmt.Errorf("scan server exit link: %w", err)
		}
		if entryID == id {
			if external.Valid {
				out.External = new(exitlink.Proxy)
				if err := json.Unmarshal([]byte(external.String), out.External); err != nil {
					return config, fmt.Errorf("decode external exit settings")
				}
			}
			if err := json.Unmarshal([]byte(rules), &out.Rules); err != nil {
				return config, fmt.Errorf("decode exit rules; repair server exit settings")
			}
			config.Outgoing = &out
		} else {
			config.Incoming = append(config.Incoming, exitlink.Inbound{ID: out.ID, Source: source, Port: out.Port, Password: out.Password})
		}
	}
	return config, rows.Err()
}

func ReadLocalExit(ctx context.Context, q *sql.DB) (exitlink.Configuration, error) {
	tx, err := q.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return exitlink.Configuration{}, fmt.Errorf("read local exit snapshot: %w", err)
	}
	defer tx.Rollback()
	local, err := LocalServerID(ctx, tx)
	if err != nil {
		return exitlink.Configuration{}, err
	}
	return LocalExitConfig(ctx, tx, local)
}

// RecordExitProbe accepts only a fresh result for the link currently owned by
// the reporting entry. Its callback queues an alert in the same transaction.
func RecordExitProbe(ctx context.Context, tx *sql.Tx, entryID int64, probe exitlink.Probe, onTransition func(targetIP string, incident int64, recovered bool) error) error {
	if probe.Available == nil {
		return nil
	}
	var checkedAt, incident int64
	var available *bool
	var targetIP string
	err := tx.QueryRowContext(ctx, `SELECT l.checked_at,l.available,l.incident_at,coalesce(s.exit_address,json_extract(l.external_proxy,'$.host')) FROM exit_links l
		LEFT JOIN servers s ON s.id=l.exit_id WHERE l.entry_id=? AND l.id=? AND l.revision=?`, entryID, probe.ID, probe.Version).
		Scan(&checkedAt, &available, &incident, &targetIP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read exit link probe state: %w", err)
	}
	if probe.CheckedAt <= checkedAt {
		return nil
	}
	if up := *probe.Available; available == nil || up != *available {
		if !up {
			incident = probe.CheckedAt
		}
		if incident > 0 && onTransition != nil {
			if err := onTransition(targetIP, incident, up); err != nil {
				return err
			}
		}
		if up {
			incident = 0
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exit_links SET checked_at=?,available=?,policy_down=?,incident_at=? WHERE id=?`,
		probe.CheckedAt, *probe.Available, probe.PolicyDown, incident, probe.ID); err != nil {
		return fmt.Errorf("record exit link probe: %w", err)
	}
	return nil
}
