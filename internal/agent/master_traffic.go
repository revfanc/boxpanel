package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/traffic"
)

var ErrInvalidTraffic = errors.New("invalid node traffic counters")

func (m *Master) RecordTraffic(ctx context.Context, identity authenticated, report TrafficReport) error {
	if report.Counters == nil || len(report.Counters) > MaxTrafficUsers {
		return ErrInvalidTraffic
	}
	seen := make(map[int64]struct{}, len(report.Counters))
	for _, c := range report.Counters {
		if _, duplicate := seen[c.UserID]; duplicate || c.UserID <= 0 || c.Epoch <= 0 || c.Up < 0 || c.Down < 0 {
			return ErrInvalidTraffic
		}
		seen[c.UserID] = struct{}{}
	}
	conn, err := m.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve agent traffic connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent traffic report: %w", err)
	}
	defer tx.Rollback()
	var serverID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM servers WHERE "+agentIdentity, identity.id, identity.publicKey).Scan(&serverID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		return fmt.Errorf("verify node before recording traffic: %w", err)
	}
	now := m.now()
	regression := false
	for _, c := range report.Counters {
		backward, err := m.recordCounter(ctx, tx, serverID, c, now)
		if err != nil {
			return err
		}
		regression = regression || backward
	}
	if err := db.Commit(conn, tx); err != nil {
		return fmt.Errorf("commit agent traffic report: %w", err)
	}
	if regression {
		m.Logger.Warn("节点累计流量出现回退；已保留确认值。请检查节点数据库及计数状态。", "server_id", serverID)
	}
	return nil
}

func (m *Master) recordCounter(ctx context.Context, tx *sql.Tx, serverID int64, c TrafficCounter, now time.Time) (bool, error) {
	user := confgen.User{ID: c.UserID}
	var epoch, up, down, totalUp, totalDown, userEpoch, recordedEpoch, cycleUp, cycleDown int64
	err := tx.QueryRowContext(ctx, `SELECT u.enabled,u.traffic_limit,u.expire_at,u.traffic_up,u.traffic_down,
		coalesce(t.epoch,0),coalesce(t.up,0),coalesce(t.down,0),coalesce(t.total_up,0),coalesce(t.total_down,0),
		u.traffic_epoch,coalesce(t.user_epoch,0),coalesce(t.cycle_up,0),coalesce(t.cycle_down,0)
		FROM users u LEFT JOIN server_user_traffic t ON t.user_id=u.id AND t.server_id=? WHERE u.id=?`, serverID, c.UserID).
		Scan(&user.Enabled, &user.TrafficLimit, &user.ExpireAt, &user.TrafficUp, &user.TrafficDown, &epoch, &up, &down,
			&totalUp, &totalDown, &userEpoch, &recordedEpoch, &cycleUp, &cycleDown)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read node traffic baseline: %w", err)
	}
	if c.Epoch < epoch {
		return false, nil
	}
	backward := false
	if c.Epoch > epoch {
		up, down = 0, 0
	} else {
		backward = c.Up < up || c.Down < down
		c.Up, c.Down = max(c.Up, up), max(c.Down, down)
		if c.Up == up && c.Down == down {
			return backward, nil
		}
	}
	deltaUp, deltaDown := c.Up-up, c.Down-down
	if recordedEpoch != userEpoch {
		cycleUp, cycleDown = 0, 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_user_traffic (server_id,user_id,epoch,up,down,total_up,total_down,updated_at,user_epoch,cycle_up,cycle_down)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id,user_id) DO UPDATE SET
		epoch=excluded.epoch,up=excluded.up,down=excluded.down,total_up=excluded.total_up,
		total_down=excluded.total_down,updated_at=excluded.updated_at,user_epoch=excluded.user_epoch,
		cycle_up=excluded.cycle_up,cycle_down=excluded.cycle_down`, serverID, c.UserID, c.Epoch, c.Up, c.Down,
		traffic.AddBytes(totalUp, deltaUp), traffic.AddBytes(totalDown, deltaDown), now.Unix(), userEpoch,
		traffic.AddBytes(cycleUp, deltaUp), traffic.AddBytes(cycleDown, deltaDown)); err != nil {
		return false, fmt.Errorf("save node traffic baseline: %w", err)
	}
	newUp, newDown := traffic.AddBytes(user.TrafficUp, deltaUp), traffic.AddBytes(user.TrafficDown, deltaDown)
	if newUp == user.TrafficUp && newDown == user.TrafficDown {
		return backward, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET traffic_up=?,traffic_down=? WHERE id=?", newUp, newDown, user.ID); err != nil {
		return false, fmt.Errorf("aggregate node traffic: %w", err)
	}
	wasEligible := confgen.UserEligible(user, now.Unix())
	user.TrafficUp, user.TrafficDown = newUp, newDown
	if m.OnStopped != nil && wasEligible && !confgen.UserEligible(user, now.Unix()) {
		local, err := db.GetSetting(ctx, tx, db.AppliedUsers)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if !slices.Contains(local, user.ID) {
			if err := m.OnStopped(ctx, tx, user.ID, "exhausted", now); err != nil {
				return false, fmt.Errorf("record remote quota stop: %w", err)
			}
		}
	}
	return backward, nil
}
