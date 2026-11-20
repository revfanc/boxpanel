package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
)

const (
	attemptLimit = 30
	retryWindow  = 24 * time.Hour
	claimLease   = 30 * time.Second
)

type Delivery struct {
	ID        int64  `json:"id"`
	Channel   string `json:"channel"`
	Kind      string `json:"kind"`
	Username  string `json:"username,omitempty"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	UpdatedAt int64  `json:"updated_at"`
	Error     string `json:"error,omitempty"`
	Grouped   bool   `json:"grouped"`
}

func (s *Service) QueueTest(ctx context.Context, channel string) (int64, error) {
	if !KnownChannel(channel) {
		return 0, invalid("channel")
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("queue test notification: %w", err)
	}
	defer tx.Rollback()
	channels, err := loadChannels(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !channelReady(channel, channels[channel]) {
		return 0, invalid("credentials_required")
	}
	now := s.opts.Now().Unix()
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO alert_outbox (channel,next_at,created_at,updated_at)
		VALUES(?,?,?,?) RETURNING id`, channel, now, now, now).Scan(&id); err != nil {
		return 0, fmt.Errorf("write test notification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit test notification: %w", err)
	}
	return id, nil
}

type claimedDelivery struct {
	id      int64
	cfg     db.AlertChannel
	message message
}

func (s *Service) claim(ctx context.Context, channel string, now time.Time) (claimedDelivery, error) {
	var job claimedDelivery
	at := now.Unix()
	expiry := now.Add(-retryWindow).Unix()
	var due bool
	if err := s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM alert_outbox
		WHERE channel=? AND status='pending' AND (next_at<=? OR created_at<=?))`, channel, at, expiry).Scan(&due); err != nil {
		return job, fmt.Errorf("check pending notifications: %w", err)
	}
	if !due {
		return job, nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return job, fmt.Errorf("claim notifications: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE alert_outbox SET status='failed',updated_at=?,
		error=coalesce(nullif(error,''),'retry_expired') WHERE channel=? AND status='pending'
		AND created_at<=? AND (attempt_id IS NULL OR next_at<=?)`, at, channel, expiry, at); err != nil {
		return job, fmt.Errorf("expire notification retries: %w", err)
	}
	cfg, err := loadChannels(ctx, tx)
	if err != nil {
		return job, err
	}
	job.cfg = cfg[channel]

	// Each outbound request spends one slot, including a retry or test.
	hourAgo := now.Add(-time.Hour).Unix()
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_attempts WHERE channel=? AND started_at<=?", channel, hourAgo); err != nil {
		return job, fmt.Errorf("prune notification attempts: %w", err)
	}
	var sent, ready, grouped int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM alert_attempts WHERE channel=?", channel).Scan(&sent); err != nil {
		return job, fmt.Errorf("read notification rate: %w", err)
	}
	const readyFilter = "channel=? AND status='pending' AND next_at<=? AND created_at>?"
	if err := tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(grouped),0) FROM alert_outbox WHERE "+readyFilter, channel, at, expiry).Scan(&ready, &grouped); err != nil {
		return job, fmt.Errorf("count ready notifications: %w", err)
	}
	if ready == 0 {
		return job, commitClaim(tx)
	}
	if sent >= attemptLimit {
		if _, err := tx.ExecContext(ctx, "UPDATE alert_outbox SET grouped=1 WHERE grouped=0 AND "+readyFilter, channel, at, expiry); err != nil {
			return job, fmt.Errorf("group rate-limited notifications: %w", err)
		}
		return job, commitClaim(tx)
	}

	if err := tx.QueryRowContext(ctx, "INSERT INTO alert_attempts (channel,started_at) VALUES(?,?) RETURNING id", channel, at).Scan(&job.id); err != nil {
		return job, fmt.Errorf("reserve notification attempt: %w", err)
	}
	update := `UPDATE alert_outbox SET attempt_id=?,attempts=attempts+1,next_at=?,updated_at=?,
		grouped=? WHERE `
	lease := now.Add(claimLease).Unix()
	switch {
	case sent == attemptLimit-1 && ready > 1:
		_, err = tx.ExecContext(ctx, update+readyFilter, job.id, lease, at, 1, channel, at, expiry)
	case grouped > 0:
		_, err = tx.ExecContext(ctx, update+"grouped=1 AND "+readyFilter, job.id, lease, at, 1, channel, at, expiry)
	default:
		_, err = tx.ExecContext(ctx, update+"id=(SELECT id FROM alert_outbox WHERE "+readyFilter+" ORDER BY next_at,id LIMIT 1)", job.id, lease, at, 0, channel, at, expiry)
	}
	if err != nil {
		return job, fmt.Errorf("reserve outbox rows: %w", err)
	}
	job.message, err = deliveryMessage(ctx, tx, job.id, now)
	if err != nil {
		return job, err
	}
	return job, commitClaim(tx)
}

func commitClaim(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit notification claim: %w", err)
	}
	return nil
}

func deliveryMessage(ctx context.Context, tx *sql.Tx, attempt int64, now time.Time) (message, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM alert_outbox WHERE attempt_id=?", attempt).Scan(&count); err != nil {
		return message{}, fmt.Errorf("count notification batch: %w", err)
	}
	var address string
	if err := tx.QueryRowContext(ctx, "SELECT public_host FROM servers WHERE is_local=1").Scan(&address); err != nil {
		return message{}, fmt.Errorf("read local server notification address: %w", err)
	}
	if !rules.ValidHost(address) {
		address = ""
	}
	// The first five events retain full context while bounding the payload
	// length for provider limits. The panel keeps every individual alert.
	rows, err := tx.QueryContext(ctx, `SELECT coalesce(e.kind,''),coalesce(e.address,?),coalesce(e.username,''),
		coalesce(e.used,0),coalesce(e.limit_bytes,0),coalesce(e.deadline,0),coalesce(e.offline_seconds,0),o.created_at,o.grouped
		FROM alert_outbox o LEFT JOIN alert_events e ON e.event_key=o.event_key
		WHERE o.attempt_id=? ORDER BY o.id LIMIT 5`, address, attempt)
	if err != nil {
		return message{}, fmt.Errorf("read notification messages: %w", err)
	}
	defer rows.Close()
	var parts []message
	var grouped bool
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Kind, &event.Address, &event.Username, &event.Used, &event.Limit, &event.Deadline, &event.OfflineSeconds, &event.CreatedAt, &grouped); err != nil {
			return message{}, fmt.Errorf("read notification message: %w", err)
		}
		parts = append(parts, event.message())
	}
	if err := rows.Err(); err != nil {
		return message{}, fmt.Errorf("read notification batch: %w", err)
	}
	if len(parts) == 0 {
		return message{}, fmt.Errorf("notification batch is empty")
	}
	if !grouped {
		return parts[0], nil
	}
	var text strings.Builder
	fmt.Fprintf(&text, "共 %d 条通知 · %s", count, utcString(now.Unix()))
	for _, part := range parts {
		fmt.Fprintf(&text, "\n\n%s\n%s", part.Title, part.Body)
	}
	if count > len(parts) {
		fmt.Fprintf(&text, "\n\n其余 %d 条请打开 boxpanel 概览查看。", count-len(parts))
	}
	return message{Title: "boxpanel 通知摘要", Body: text.String()}, nil
}

func (s *Service) finish(ctx context.Context, job claimedDelivery, now time.Time, sendErr error) error {
	at := now.Unix()
	if sendErr == nil {
		if _, err := s.writer.ExecContext(ctx, `UPDATE alert_outbox SET status='sent',updated_at=?,error=''
			WHERE status='pending' AND attempt_id=?`, at, job.id); err != nil {
			return fmt.Errorf("mark notification delivered: %w", err)
		}
		return nil
	}
	// The first failure waits 30 seconds, then up to 3600 seconds. Schedule
	// at most to the expiry so the panel sees terminal failures promptly.
	seconds := int64(retryWindow / time.Second)
	if _, err := s.writer.ExecContext(ctx, `UPDATE alert_outbox SET updated_at=?,error=?,
		status=CASE WHEN created_at+?<=? THEN 'failed' ELSE 'pending' END,
		next_at=min(created_at+?,?+min(3600,30 << min(attempts-1,7)))
		WHERE status='pending' AND attempt_id=?`, at, failureCode(sendErr), seconds, at, seconds, at, job.id); err != nil {
		return fmt.Errorf("schedule notification retry: %w", err)
	}
	return nil
}

func (s *Service) deliverLoop(ctx context.Context, channel string) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		job, err := s.claim(ctx, channel, s.opts.Now())
		if err != nil {
			s.log(err)
		} else if job.id != 0 {
			var sendErr error
			if channelReady(channel, job.cfg) {
				sendErr = s.sender.send(ctx, channel, job.cfg, job.message)
			} else {
				sendErr = errChannelDisabled
			}
			if ctx.Err() == nil {
				s.log(s.finish(ctx, job, s.opts.Now(), sendErr))
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

type Overview struct {
	Billing     Billing    `json:"billing"`
	Unread      []Event    `json:"unread"`
	UnreadCount int        `json:"unread_count"`
	Recent      []Delivery `json:"recent"`
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	out := Overview{Unread: []Event{}, Recent: []Delivery{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("read alert overview: %w", err)
	}
	defer tx.Rollback()
	cfg, err := loadConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	serverID, err := db.LocalServerID(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Billing, err = billing(ctx, tx, serverID, cfg, s.opts.Now(), s.opts.Network != nil)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM alert_events WHERE read_at=0").Scan(&out.UnreadCount); err != nil {
		return out, fmt.Errorf("count unread alerts: %w", err)
	}
	out.Unread, err = unread(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Recent, err = recent(ctx, tx)
	return out, err
}

func unread(ctx context.Context, tx *sql.Tx) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_key,server_id,kind,address,username,used,limit_bytes,threshold,deadline,created_at,offline_seconds
		FROM alert_events WHERE read_at=0 ORDER BY created_at DESC,event_key DESC LIMIT 30`)
	if err != nil {
		return nil, fmt.Errorf("read unread alerts: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Key, &e.ServerID, &e.Kind, &e.Address, &e.Username, &e.Used, &e.Limit, &e.Threshold, &e.Deadline, &e.CreatedAt, &e.OfflineSeconds); err != nil {
			return nil, fmt.Errorf("read alert details: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func recent(ctx context.Context, tx *sql.Tx) ([]Delivery, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.channel,coalesce(e.kind,'test'),coalesce(e.username,''),
		o.status,o.attempts,o.updated_at,o.error,o.grouped FROM alert_outbox o LEFT JOIN alert_events e ON e.event_key=o.event_key
		ORDER BY o.updated_at DESC,o.id DESC LIMIT 30`)
	if err != nil {
		return nil, fmt.Errorf("read recent notifications: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.Channel, &d.Kind, &d.Username, &d.Status, &d.Attempts, &d.UpdatedAt, &d.Error, &d.Grouped); err != nil {
			return nil, fmt.Errorf("read notification details: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) ReadEvent(ctx context.Context, key string) (bool, error) {
	if key == "" || len(key) > 160 {
		return false, nil
	}
	result, err := s.writer.ExecContext(ctx, `UPDATE alert_events SET read_at=CASE WHEN read_at=0 THEN ? ELSE read_at END
		WHERE event_key=?`, s.opts.Now().Unix(), key)
	if err != nil {
		return false, fmt.Errorf("mark alert read: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count read alerts: %w", err)
	}
	return count != 0, nil
}
