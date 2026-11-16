package traffic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type Applier interface {
	Apply(context.Context, singbox.Mutation) error
}
type Scheduler struct {
	collector   *Collector
	applier     Applier
	now         func() time.Time
	logger      *slog.Logger
	failures    int
	nextAttempt time.Time
}

func NewScheduler(c *Collector, applier Applier, now func() time.Time, logger *slog.Logger) *Scheduler {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{collector: c, applier: applier, now: now, logger: logger}
}

type user struct {
	confgen.User
	epoch, resetAt int64
	resetDay       int
}

func loadUsers(ctx context.Context, tx *sql.Tx) ([]user, map[int64]bool, error) {
	active, err := db.GetSetting(ctx, tx, db.AppliedUsers)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	ids := map[int64]bool{}
	for _, id := range active {
		ids[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,enabled,traffic_limit,traffic_up,traffic_down,expire_at,traffic_epoch,traffic_reset_day,traffic_reset_at FROM users`)
	if err != nil {
		return nil, nil, fmt.Errorf("read scheduled users: %w", err)
	}
	defer rows.Close()
	users := []user{}
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &u.epoch, &u.resetDay, &u.resetAt); err != nil {
			return nil, nil, fmt.Errorf("scan scheduled user: %w", err)
		}
		users = append(users, u)
	}
	return users, ids, rows.Err()
}
func monthlyDue(u user, now time.Time) bool {
	if u.resetDay == 0 {
		return false
	}
	utc := now.UTC()
	boundary := time.Date(utc.Year(), utc.Month(), u.resetDay, 0, 0, 0, 0, time.UTC)
	return !utc.Before(boundary) && u.resetAt < boundary.Unix()
}
func reason(u user, now int64) string {
	if u.ExpireAt > 0 && u.ExpireAt <= now {
		return "expired"
	}
	return "exhausted"
}

var errUnchanged = errors.New("scheduled users no longer require apply")

// Check reevaluates under Apply's write transaction, merges all changes into
// one check/restart/rollback, and includes unflushed usage when enforcing limits.
func (s *Scheduler) Check(ctx context.Context) error {
	now := s.now()
	if now.Before(s.nextAttempt) {
		return nil
	}
	c := s.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin scheduled snapshot: %w", err)
	}
	users, active, err := loadUsers(ctx, tx)
	tx.Rollback()
	if err != nil {
		return err
	}
	needed := false
	for _, u := range users {
		d := c.pending[account{u.ID, u.epoch}]
		u.TrafficUp = add(u.TrafficUp, d.up)
		u.TrafficDown = add(u.TrafficDown, d.down)
		if monthlyDue(u, now) || active[u.ID] && !confgen.UserEligible(u.User, now.Unix()) {
			needed = true
			break
		}
	}
	if !needed {
		s.failures = 0
		s.nextAttempt = time.Time{}
		return nil
	}
	stopped := map[int64]string{}
	err = s.applier.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := c.writePending(ctx, tx); err != nil {
			return err
		}
		current, applied, err := loadUsers(ctx, tx)
		if err != nil {
			return err
		}
		changed := false
		for _, u := range current {
			if monthlyDue(u, now) {
				if _, err := tx.ExecContext(ctx, `UPDATE users SET traffic_up=0,traffic_down=0,traffic_reset_at=?,traffic_epoch=traffic_epoch+1 WHERE id=?`, now.Unix(), u.ID); err != nil {
					return fmt.Errorf("reset monthly traffic: %w", err)
				}
				u.TrafficUp, u.TrafficDown = 0, 0
				changed = true
			}
			if applied[u.ID] && !confgen.UserEligible(u.User, now.Unix()) {
				stopped[u.ID] = reason(u, now.Unix())
				changed = true
			}
		}
		if !changed {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	if err != nil {
		s.failures++
		delay := 30 * time.Second
		for i := 1; i < s.failures && delay < 10*time.Minute; i++ {
			delay = min(2*delay, 10*time.Minute)
		}
		s.nextAttempt = now.Add(delay)
		s.logger.Warn("自动停用或流量重置未生效；将自动重试，请检查内核状态与生效诊断。", "retry_at", s.nextAttempt)
		return fmt.Errorf("apply scheduled user changes: %w", err)
	}
	c.clearPending()
	s.failures = 0
	s.nextAttempt = time.Time{}
	for id, why := range stopped {
		s.logger.Info("proxy user automatically stopped", "user_id", id, "reason", why)
	}
	return nil
}

func (s *Scheduler) Run(ctx context.Context) {
	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	flush := time.NewTicker(60 * time.Second)
	defer flush.Stop()
	check := time.NewTicker(30 * time.Second)
	defer check.Stop()
	report := func(err error) {
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("流量任务未完成；将自动重试，请检查nftables 权限、内核运行状态和数据库权限。")
		}
	}
	report(s.collector.Poll(ctx))
	report(s.Check(ctx))
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// A final sample is best effort; previously collected increments must still flush.
			_ = s.collector.Poll(final)
			cancel()
			final, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.collector.Flush(final); err != nil {
				s.logger.Warn("退出时流量落库失败；尚未保存的用量会丢失，请检查数据库权限和磁盘空间。")
			}
			cancel()
			return
		case <-poll.C:
			report(s.collector.Poll(ctx))
		case <-flush.C:
			report(s.collector.Flush(ctx))
		case <-check.C:
			report(s.Check(ctx))
		}
	}
}
