package db

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	RoleMaster = "master"
	RoleAgent  = "agent"
)

// LocalServerID identifies the server that owns this process's kernel and firewall.
func LocalServerID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var id int64
	if err := q.QueryRowContext(ctx, "SELECT id FROM servers WHERE is_local=1").Scan(&id); err != nil {
		return 0, fmt.Errorf("read local server: %w", err)
	}
	return id, nil
}
