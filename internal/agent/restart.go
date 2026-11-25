package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/revfanc/boxpanel/internal/auth"
)

var ErrMonitoringRequired = errors.New("node monitoring is unavailable")

const restartInterrupted = "interrupted"

type RestartState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type RestartResult struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

func (r *RestartResult) Valid() bool {
	return r == nil || len(decodeBytes(r.ID, 16)) != 0 && (r.Error == restartInterrupted ||
		ValidSyncError(r.Error) && r.Error != SyncKeys && r.Error != SyncMasterOutdated)
}

// QueueRestart coalesces concurrent requests while a command is pending. The
// command travels with desired state without advancing the configuration version.
func (m *Master) QueueRestart(ctx context.Context, serverID int64) (RestartState, error) {
	var state RestartState
	id, err := auth.RandomToken(16)
	if err != nil {
		return state, fmt.Errorf("generate restart command ID: %w", err)
	}
	err = m.writer.QueryRowContext(ctx, `UPDATE servers SET
		restart_id=CASE WHEN restart_status='pending' THEN restart_id ELSE ? END, restart_status='pending', restart_error=''
		WHERE id=? AND is_local=0 AND status<>'removed' AND monitoring IS NOT NULL AND protocol>=?
		RETURNING restart_id,restart_status`, id, serverID, MinNodeProtocol).Scan(&state.ID, &state.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrMonitoringRequired
	}
	if err != nil {
		return state, fmt.Errorf("queue node restart: %w", err)
	}
	return state, nil
}

func recordRestartResult(ctx context.Context, tx *sql.Tx, serverID int64, result *RestartResult) error {
	if result == nil {
		return nil
	}
	status := "succeeded"
	if result.Error != "" {
		status = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE servers SET restart_status=?,restart_error=?
		WHERE id=? AND restart_id=? AND restart_status='pending'`, status, result.Error, serverID, result.ID); err != nil {
		return fmt.Errorf("record node restart result: %w", err)
	}
	return nil
}

func (n *Node) restart(ctx context.Context, id string) (*RestartResult, error) {
	if len(decodeBytes(id, 16)) == 0 {
		return nil, fmt.Errorf("invalid restart command ID")
	}
	claimed, err := n.claimRestart(ctx, id)
	if err != nil {
		return nil, err
	}
	result := &RestartResult{ID: id}
	if !claimed {
		err := n.Reader.QueryRowContext(ctx, "SELECT error FROM agent_restart_commands WHERE id=?", id).Scan(&result.Error)
		if err != nil {
			return nil, fmt.Errorf("read stored restart result: %w", err)
		}
		return result, nil
	}
	// A nil mutation forces the complete check, atomic replacement, restart
	// and rollback path, even when the generated config has not changed.
	if err := n.Manager.Apply(ctx, nil); err != nil {
		result.Error = syncErrorCode(err)
	}
	if _, err := n.Writer.ExecContext(ctx, "UPDATE agent_restart_commands SET error=? WHERE id=?", result.Error, id); err != nil {
		return nil, fmt.Errorf("save node restart result: %w", err)
	}
	return result, nil
}

func (n *Node) claimRestart(ctx context.Context, id string) (bool, error) {
	tx, err := n.Writer.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin node restart claim: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO agent_restart_commands (id) VALUES (?) ON CONFLICT DO NOTHING", id)
	if err != nil {
		return false, fmt.Errorf("claim node restart: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read node restart claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit node restart claim: %w", err)
	}
	return claimed != 0, nil
}
