package singbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// applyDevelopmentDBOnly is ONLY for serve --dev resource editing. It does not
// generate/check config, touch certificates/firewall/files, or restart services.
// Restart, CLI apply and production never use this substitute.
func (m *Manager) applyDevelopmentDBOnly(ctx context.Context, mutate Mutation) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve development-only connection: %w", err)
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin development-only transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			discardConnection(connection)
			result = errors.Join(result, fmt.Errorf("rollback development-only transaction: %w", err))
		}
	}()
	if err := mutate(ctx, tx); err != nil {
		return fmt.Errorf("mutate development-only transaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		discardConnection(connection)
		return fmt.Errorf("commit development-only transaction: %w", err)
	}
	return nil
}
