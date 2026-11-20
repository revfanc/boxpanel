package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
)

func AgentKeyPath(paths Paths) string {
	dir := paths.DataDir
	if dir == "" {
		dir = filepath.Dir(paths.Database)
	}
	return filepath.Join(dir, "agent.key")
}

func OpenNodeDatabase(ctx context.Context, paths Paths) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(paths.Database), 0700); err != nil {
		return nil, fmt.Errorf("create node database directory: %w", err)
	}
	f, err := os.OpenFile(paths.Database, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open node database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close node database file: %w", err)
	}
	return db.Open(ctx, paths.Database)
}

func CheckUninitialized(ctx context.Context, q db.SettingsDB) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM admins)
		+ (SELECT count(*) FROM settings WHERE key <> 'singbox_version')
		+ (SELECT count(*) FROM users) + (SELECT count(*) FROM inbounds)
		+ (SELECT count(*) FROM sessions) + (SELECT count(*) FROM servers WHERE is_local=0)`).Scan(&count); err != nil {
		return fmt.Errorf("check initialization state: %w", err)
	}
	if count != 0 {
		return ErrInitialized
	}
	return nil
}

func JoinNode(ctx context.Context, paths Paths, masterURL, pin, token, hostname, publicIP, version string) (agent.Identity, error) {
	client, err := agent.NewClient(masterURL, pin)
	if err != nil {
		return agent.Identity{}, err
	}
	defer client.Close()
	conn, err := OpenNodeDatabase(ctx, paths)
	if err != nil {
		return agent.Identity{}, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return agent.Identity{}, fmt.Errorf("begin local node registration: %w", err)
	}
	defer tx.Rollback()
	if err := CheckUninitialized(ctx, tx); err != nil {
		if errors.Is(err, ErrInitialized) {
			return agent.Identity{}, fmt.Errorf("数据目录已初始化，请在空目录加入。已加入的节点可先执行 boxpanel leave")
		}
		return agent.Identity{}, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return agent.Identity{}, fmt.Errorf("generate node Ed25519 key: %w", err)
	}
	keyPath := AgentKeyPath(paths)
	// A failed power cycle can leave a key before the transaction commits.
	// The uninitialized check holds the database write lock, so no live
	// identity can use that stale key.
	if err := removeAgentKey(keyPath); err != nil {
		return agent.Identity{}, err
	}
	if err := writeNewPrivateFile(keyPath, private.Seed()); err != nil {
		return agent.Identity{}, fmt.Errorf("store node signing key: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(keyPath)
		}
	}()

	identity, err := client.Join(ctx, token, public, hostname, publicIP, version)
	if errors.Is(err, agent.ErrUnauthorized) {
		return agent.Identity{}, fmt.Errorf("加入令牌无效或已过期，请在主控重新生成")
	}
	if err != nil {
		return agent.Identity{}, err
	}
	connection := db.AgentConnection{
		MasterURL: client.MasterURL(), Pin: pin, ServerID: identity.ServerID, PublicKey: base64.RawURLEncoding.EncodeToString(public),
	}
	if err := db.SetSetting(ctx, tx, db.AgentMaster, connection); err != nil {
		return agent.Identity{}, err
	}
	if err := db.SetSetting(ctx, tx, db.NodeRole, db.RoleAgent); err != nil {
		return agent.Identity{}, err
	}
	if err := db.SetSetting(ctx, tx, db.BlockBittorrent, true); err != nil {
		return agent.Identity{}, err
	}
	// A fresh identity starts after any samples retained by an earlier leave.
	var lastSample sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT max(sampled_at) FROM server_traffic_samples
		WHERE server_id=(SELECT id FROM servers WHERE is_local=1)`).Scan(&lastSample); err != nil {
		return agent.Identity{}, fmt.Errorf("read samples retained from a previous node identity: %w", err)
	}
	if lastSample.Valid {
		if err := db.SetSetting(ctx, tx, db.AgentInterfaceAck, lastSample.Int64); err != nil {
			return agent.Identity{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return agent.Identity{}, fmt.Errorf("commit local node registration: %w", err)
	}
	committed = true
	return identity, nil
}

func LoadAgentKey(paths Paths, publicKey string) (ed25519.PrivateKey, error) {
	path := AgentKeyPath(paths)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read node signing key metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != ed25519.SeedSize {
		return nil, fmt.Errorf("节点密钥格式或权限无效，请恢复 0600 的原密钥，或执行 boxpanel leave 后重新加入")
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read node signing key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("节点密钥内容无效，请执行 boxpanel leave 后重新加入")
	}
	key := ed25519.NewKeyFromSeed(seed)
	if base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) != publicKey {
		return nil, fmt.Errorf("节点密钥与本地注册信息不匹配，请执行 boxpanel leave 后重新加入")
	}
	return key, nil
}

func LeaveNode(ctx context.Context, paths Paths) error {
	conn, err := OpenExisting(ctx, paths.Database)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin leaving master: %w", err)
	}
	defer tx.Rollback()
	role, err := db.GetSetting(ctx, tx, db.NodeRole)
	if errors.Is(err, sql.ErrNoRows) {
		if err := CheckUninitialized(ctx, tx); err != nil {
			return err
		}
		return removeAgentKey(AgentKeyPath(paths))
	}
	if err != nil {
		return err
	}
	if role != db.RoleAgent {
		return fmt.Errorf("当前实例是主控，请在面板移除相应服务器")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbounds; DELETE FROM users; DELETE FROM agent_retired_traffic;
		UPDATE servers SET alert_preferences=json_set(alert_preferences,'$.interface','') WHERE is_local=1`); err != nil {
		return fmt.Errorf("clear mirrored node configuration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key <> 'singbox_version'"); err != nil {
		return fmt.Errorf("clear local node settings: %w", err)
	}
	// Commit the credential change before touching the file. A local retry
	// can clean up a leftover key if filesystem removal failed or was interrupted.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit removal of local node settings: %w", err)
	}
	// Reacquire the lock and remove the key only if a concurrent join hasn't
	// already replaced it for a new identity.
	return clearUninitializedKey(ctx, conn, AgentKeyPath(paths))
}

func clearUninitializedKey(ctx context.Context, conn *sql.DB, path string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lock old node key: %w", err)
	}
	defer tx.Rollback()
	if err := CheckUninitialized(ctx, tx); err != nil {
		if errors.Is(err, ErrInitialized) {
			return nil
		}
		return err
	}
	return removeAgentKey(path)
}

func removeAgentKey(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect old node key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != ed25519.SeedSize {
		return fmt.Errorf("agent.key 已被其他文件占用，请检查数据目录和文件权限")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove old node key: %w", err)
	}
	return nil
}

// ValidateAgentKeyPath keeps CLI overrides from aliasing the node identity file.
func ValidateAgentKeyPath(paths Paths) error {
	key, err := filepath.Abs(AgentKeyPath(paths))
	if err != nil {
		return fmt.Errorf("resolve agent key path: %w", err)
	}
	for _, name := range []string{paths.Database, paths.CertFile, paths.KeyFile} {
		other, err := filepath.Abs(name)
		if err != nil {
			return fmt.Errorf("resolve protected data path: %w", err)
		}
		if other == key || strings.HasPrefix(key, other+string(filepath.Separator)) {
			return fmt.Errorf("agent.key 是节点身份保留路径，请使用单独的数据库和证书文件")
		}
	}
	return nil
}
