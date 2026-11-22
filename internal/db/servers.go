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

// UserServerAccess is a boolean SQL expression over a row of users that is
// true when that user may use the server bound to its single parameter.
const UserServerAccess = "(users.all_servers=1 OR EXISTS (SELECT 1 FROM user_servers WHERE user_servers.user_id=users.id AND user_servers.server_id=?))"

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
