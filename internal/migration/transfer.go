package migration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
)

const HeaderTarget = "X-Boxpanel-Migration-Target"
const HeaderExpires = "X-Boxpanel-Migration-Expires"

func (s *Service) PanelStatus(ctx context.Context) (Status, error) {
	out, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationOutgoing)
	if err != nil {
		return Status{}, err
	}
	if out.Phase != "" && !out.Expired(s.now()) {
		return stateStatus(out), nil
	}
	in, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationReceived)
	if err != nil || in.Phase == "" {
		return Status{Phase: "idle"}, err
	}
	return s.receivedStatus(ctx, in, true)
}

func stateStatus(state db.MasterMigration) Status {
	return Status{Phase: state.Phase, TargetURL: state.TargetURL, ExpiresAt: state.ExpiresAt,
		Migrated: state.Migrated, Total: state.Total, CoreStopped: state.CoreStopped}
}

func (s *Service) receivedStatus(ctx context.Context, state db.MasterMigration, includeNames bool) (Status, error) {
	out := stateStatus(state)
	rows, err := s.ReadDB.QueryContext(ctx, `SELECT servers.id,servers.name,n.arrived FROM master_migration_nodes n
		JOIN servers ON servers.id=n.server_id WHERE servers.status<>'removed' ORDER BY servers.id`)
	if err != nil {
		return out, fmt.Errorf("read migration progress: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var node Node
		var arrived bool
		if err := rows.Scan(&node.ID, &node.Name, &arrived); err != nil {
			return out, fmt.Errorf("read migrated node: %w", err)
		}
		out.Total++
		if arrived {
			out.Migrated++
		} else if includeNames {
			out.Waiting = append(out.Waiting, node)
		}
	}
	return out, rows.Err()
}

func (s *Service) PeerStatus(ctx context.Context, id string) (Status, error) {
	if !ValidSecret(id) {
		return Status{}, ErrToken
	}
	out, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationOutgoing)
	if err != nil {
		return Status{}, err
	}
	if matches(out.ID, id) && !out.Expired(s.now()) {
		return stateStatus(out), nil
	}
	in, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationReceived)
	if err != nil {
		return Status{}, err
	}
	if !matches(in.ID, id) {
		return Status{}, ErrToken
	}
	return s.receivedStatus(ctx, in, false)
}

func PrepareTarget(ctx context.Context, stage, source, target, secret, pin string, expires int64) (db.MasterMigration, error) {
	u, err := url.Parse(target)
	if err != nil {
		return db.MasterMigration{}, ErrAddress
	}
	if expected, err := targetAddress(source, u.Hostname()); err != nil || expected != target {
		return db.MasterMigration{}, ErrAddress
	}
	conn, err := app.OpenExisting(ctx, filepath.Join(stage, "boxpanel.db"))
	if err != nil {
		return db.MasterMigration{}, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return db.MasterMigration{}, fmt.Errorf("begin migration target preparation: %w", err)
	}
	defer tx.Rollback()
	if saved, err := db.GetSetting(ctx, tx, db.PanelFallbackPin); err != nil || !matches(saved, pin) {
		return db.MasterMigration{}, fmt.Errorf("迁移快照中的主控指纹不符，请重新复制旧主控的迁移命令")
	}
	if role, err := db.GetSetting(ctx, tx, db.NodeRole); err != nil || role != db.RoleMaster {
		return db.MasterMigration{}, backup.ErrInvalid
	}
	publicHost, err := db.GetSetting(ctx, tx, db.PublicHost)
	if err != nil {
		return db.MasterMigration{}, err
	}
	if _, err := netip.ParseAddr(publicHost); err == nil {
		if err := db.SetSetting(ctx, tx, db.PublicHost, u.Hostname()); err != nil {
			return db.MasterMigration{}, err
		}
	}
	local, err := db.LocalServerID(ctx, tx)
	if err != nil {
		return db.MasterMigration{}, err
	}
	if err := db.SetExitAddress(ctx, tx, local, u.Hostname()); err != nil {
		return db.MasterMigration{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return db.MasterMigration{}, ErrAddress
	}
	if err := db.SetSetting(ctx, tx, db.WebPort, port); err != nil {
		return db.MasterMigration{}, err
	}
	if err := db.SetSetting(ctx, tx, db.WebPath, strings.Trim(u.Path, "/")); err != nil {
		return db.MasterMigration{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO master_migration_nodes(server_id)
		SELECT id FROM servers WHERE is_local=0 AND status<>'removed' AND agent_public_key IS NOT NULL`); err != nil {
		return db.MasterMigration{}, fmt.Errorf("initialize migration progress: %w", err)
	}
	state := db.MasterMigration{Phase: db.MigrationIncoming, SourceURL: source, TargetURL: target, ID: exchangeID(secret), ExpiresAt: expires}
	if err := db.SetSetting(ctx, tx, db.MigrationReceived, state); err != nil {
		return state, err
	}
	if err := tx.Commit(); err != nil {
		return state, fmt.Errorf("stage migration settings: %w", err)
	}
	return state, nil
}

func Activate(ctx context.Context, conn *sql.DB, id string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin target activation: %w", err)
	}
	defer tx.Rollback()
	in, err := db.LoadMasterMigration(ctx, tx, db.MigrationReceived)
	if err != nil {
		return err
	}
	if !matches(in.ID, id) || (in.Phase != db.MigrationActive && in.Phase != db.MigrationIncoming) {
		return ErrToken
	}
	if in.Phase == db.MigrationActive {
		return nil
	}
	in.Phase, in.ExpiresAt = db.MigrationActive, 0
	if err := db.SetSetting(ctx, tx, db.MigrationReceived, in); err != nil {
		return err
	}
	return tx.Commit()
}

type Client struct {
	base string
	http *http.Client
}

func NewClient(address, pin string) (*Client, error) {
	base, err := agent.NormalizeMasterURL(address)
	if err != nil {
		return nil, err
	}
	if !ValidSecret(pin) {
		return nil, fmt.Errorf("迁移需要固定回退证书指纹，请复制完整迁移命令")
	}
	client, err := agent.MasterHTTPClient(base, pin, 0)
	if err != nil {
		return nil, err
	}
	return &Client{strings.TrimSuffix(base, "/"), client}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) post(ctx context.Context, path string, in any) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encode migration request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+Prefix+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build migration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, agent.ErrMasterUnreachable
	}
	return resp, nil
}

func (c *Client) Download(ctx context.Context, claim Claim) (backup.Archive, string, int64, error) {
	resp, err := c.post(ctx, "/claim", claim)
	if err != nil {
		return backup.Archive{}, "", 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return backup.Archive{}, "", 0, ErrToken
	case http.StatusConflict:
		return backup.Archive{}, "", 0, ErrBusy
	case http.StatusUpgradeRequired:
		return backup.Archive{}, "", 0, ErrVersion
	case http.StatusUnprocessableEntity:
		return backup.Archive{}, "", 0, ErrAddress
	default:
		return backup.Archive{}, "", 0, fmt.Errorf("旧主控未能提供迁移快照（HTTP %d），请查看旧主控日志后重试", resp.StatusCode)
	}
	target := resp.Header.Get(HeaderTarget)
	expires, parseErr := strconv.ParseInt(resp.Header.Get(HeaderExpires), 10, 64)
	if parseErr != nil || expires <= 0 || target == "" || resp.ContentLength > backup.MaxSize {
		return backup.Archive{}, "", 0, backup.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, backup.MaxSize+1))
	if err != nil || resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength {
		return backup.Archive{}, "", 0, fmt.Errorf("迁移快照传输中断，请检查网络并在旧主控生成新命令")
	}
	defer clear(data)
	a, err := backup.Decode(data, "", false)
	if err == nil && a.Manifest.Version != claim.Version {
		err = ErrVersion
	}
	return a, target, expires, err
}

func (c *Client) ReportReady(ctx context.Context, in Ready) error {
	resp, err := c.post(ctx, "/ready", in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrToken
	}
	return ErrReady
}

func (c *Client) Abort(ctx context.Context, secret string) error {
	resp, err := c.post(ctx, "/abort", map[string]string{"secret": secret})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return ErrCommitted
	default:
		return fmt.Errorf("旧主控未能确认取消迁移，请在旧主控查看迁移状态")
	}
}

func FetchStatus(ctx context.Context, address, pin, id string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c, err := NewClient(address, pin)
	if err != nil {
		return Status{}, err
	}
	defer c.Close()
	resp, err := c.post(ctx, "/status", map[string]string{"id": id})
	if err != nil {
		return Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Status{}, ErrToken
	}
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("peer migration status returned HTTP %d", resp.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode peer migration status: %w", err)
	}
	return status, nil
}
