package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	MigrationIssued   = "issued"
	MigrationFrozen   = "frozen"
	MigrationRetired  = "retired"
	MigrationIncoming = "incoming"
	MigrationActive   = "active"
)

var (
	MigrationOutgoing    = SettingKey[MasterMigration]{"master_migration_outgoing"}
	MigrationReceived    = SettingKey[MasterMigration]{"master_migration_received"}
	ErrMigrationReadOnly = errors.New("主控迁移期间只读，请在迁移完成后到新主控重试；若迁移失败，请等待旧主控解冻")
)

type MasterMigration struct {
	Phase       string `json:"phase"`
	TokenHash   string `json:"token_hash,omitempty"`
	ID          string `json:"id,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Migrated    int    `json:"migrated,omitempty"`
	Total       int    `json:"total,omitempty"`
	CoreStopped bool   `json:"core_stopped,omitempty"`
}

func LoadMasterMigration(ctx context.Context, q SettingsDB, key SettingKey[MasterMigration]) (MasterMigration, error) {
	state, err := GetSetting(ctx, q, key)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return state, err
}

func (s MasterMigration) Pending() bool {
	return s.Phase == MigrationIssued || s.Phase == MigrationFrozen
}

func (s MasterMigration) Expired(now time.Time) bool {
	return s.Pending() && s.ExpiresAt <= now.Unix()
}

func ReadOnlyMigration(ctx context.Context, q SettingsDB, now time.Time) (bool, error) {
	out, err := LoadMasterMigration(ctx, q, MigrationOutgoing)
	if err != nil || out.Phase == MigrationRetired || out.Phase == MigrationFrozen && !out.Expired(now) {
		return err == nil, err
	}
	in, err := LoadMasterMigration(ctx, q, MigrationReceived)
	return in.Phase == MigrationIncoming, err
}

// A restored archive starts a new operating instance. Transfer grants and
// source/target lifecycle belong to the original machines.
func ClearMasterMigration(ctx context.Context, q SettingsDB) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM settings WHERE key IN (?,?)", MigrationOutgoing.name, MigrationReceived.name); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM master_migration_nodes")
	return err
}
