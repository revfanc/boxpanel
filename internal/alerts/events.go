package alerts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
)

const (
	userQuota80     = "user_quota_80"
	userQuota100    = "user_quota_100"
	userExpiring    = "user_expiring"
	userExpired     = "user_expired"
	userStopped     = "user_stopped"
	serverQuota80   = "server_quota_80"
	serverQuota90   = "server_quota_90"
	serverQuota100  = "server_quota_100"
	certWarning     = "certificate_warning"
	coreDown        = "core_down"
	coreRecovered   = "core_recovered"
	serverOffline   = "server_offline"
	serverRecovered = "server_recovered"
)

var eventTitles = map[string]string{
	userQuota80:     "用户流量达到 80%",
	userQuota100:    "用户流量达到 100%",
	userExpiring:    "用户将在 3 天内到期",
	userExpired:     "用户已到期",
	userStopped:     "用户已自动停用",
	serverQuota80:   "服务器流量达到 80%",
	serverQuota90:   "服务器流量达到 90%",
	serverQuota100:  "服务器流量达到 100%",
	certWarning:     "面板证书续期失败",
	coreDown:        "sing-box 未运行",
	coreRecovered:   "sing-box 已恢复",
	serverOffline:   "服务器离线",
	serverRecovered: "服务器已恢复连接",
}

type Event struct {
	Key            string `json:"key"`
	ServerID       int64  `json:"server_id"`
	Kind           string `json:"kind"`
	Address        string `json:"address"`
	Username       string `json:"username,omitempty"`
	Used           int64  `json:"used"`
	Limit          int64  `json:"limit"`
	Threshold      int    `json:"threshold,omitempty"`
	Deadline       int64  `json:"deadline,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	OfflineSeconds int64  `json:"offline_seconds,omitempty"`
}

func (event Event) message() message {
	title := eventTitles[event.Kind]
	if title == "" {
		title = "boxpanel 测试通知"
	}
	address := event.Address
	if address == "" {
		address = "本机"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "服务器：%s\n", address)
	if event.Username != "" {
		fmt.Fprintf(&text, "用户：%s\n", event.Username)
	}
	if event.Limit > 0 {
		fmt.Fprintf(&text, "用量：%d / %d 字节（%.1f%%）\n", event.Used, event.Limit, percent(event.Used, event.Limit))
	}
	if event.Deadline > 0 {
		fmt.Fprintf(&text, "到期：%s\n", utcString(event.Deadline))
	}
	if event.OfflineSeconds > 0 {
		fmt.Fprintf(&text, "离线时长：%d 秒\n", event.OfflineSeconds)
	}
	fmt.Fprintf(&text, "时间：%s", utcString(event.CreatedAt))
	return message{Title: title, Body: text.String()}
}

func utcString(at int64) string {
	return time.Unix(at, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

func thresholdReached(used, limit int64, threshold int) bool {
	n := int64(threshold)
	return limit > 0 && used >= (limit/100)*n+(limit%100*n+99)/100
}

type eventRecorder struct {
	tx       *sql.Tx
	cfg      db.AlertConfig
	channels map[string]db.AlertChannel
	address  string
	serverID int64
	now      time.Time
}

func recorder(ctx context.Context, tx *sql.Tx, now time.Time) (eventRecorder, error) {
	out := eventRecorder{tx: tx, now: now}
	var err error
	if out.cfg, err = loadConfig(ctx, tx); err != nil {
		return out, err
	}
	if out.channels, err = loadChannels(ctx, tx); err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT id, public_host FROM servers WHERE is_local=1").Scan(&out.serverID, &out.address); err != nil {
		return out, fmt.Errorf("read local server for alerts: %w", err)
	}
	if !rules.ValidHost(out.address) {
		out.address = ""
	}
	return out, nil
}

func (r eventRecorder) record(ctx context.Context, event Event) (bool, error) {
	if !r.cfg.Events[event.Kind] {
		return false, nil
	}
	key := eventDigest(event.Key)
	now := r.now.Unix()
	result, err := r.tx.ExecContext(ctx, `INSERT INTO alert_events
		(event_key,server_id,kind,address,username,used,limit_bytes,threshold,deadline,created_at,offline_seconds)
	VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_key) DO NOTHING`, key, r.serverID, event.Kind, r.address,
		event.Username, event.Used, event.Limit, event.Threshold, event.Deadline, now, event.OfflineSeconds)
	if err != nil {
		return false, fmt.Errorf("record alert: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return false, err
	}
	for _, channel := range channels {
		if cfg := r.channels[channel]; cfg.Enabled && channelReady(channel, cfg) {
			if _, err := r.tx.ExecContext(ctx, `INSERT INTO alert_outbox (event_key,channel,next_at,created_at,updated_at)
				VALUES(?,?,?,?,?)`, key, channel, now, now, now); err != nil {
				return false, fmt.Errorf("queue alert: %w", err)
			}
		}
	}
	return true, nil
}

func eventDigest(key string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

type userUsage struct {
	id, up, down, limit, expiry, epoch, identity int64
	name                                         string
}

const userColumns = "id,name,traffic_up,traffic_down,traffic_limit,expire_at,traffic_epoch,alert_identity"

func (u *userUsage) scan(row interface{ Scan(...any) error }) error {
	return row.Scan(&u.id, &u.name, &u.up, &u.down, &u.limit, &u.expiry, &u.epoch, &u.identity)
}

func (u userUsage) alert(kind, period string, threshold int) Event {
	e := Event{Key: fmt.Sprintf("user:%d:%d:%s:%s", u.id, u.identity, kind, period), Kind: kind,
		Username: u.name, Used: addUsage(u.up, u.down), Limit: u.limit, Threshold: threshold}
	if kind == userExpiring || kind == userExpired {
		e.Deadline = u.expiry
	}
	return e
}

func (r eventRecorder) userEvents(ctx context.Context) error {
	rows, err := r.tx.QueryContext(ctx, "SELECT "+userColumns+" FROM users WHERE enabled=1 AND (traffic_limit>0 OR expire_at>0)")
	if err != nil {
		return fmt.Errorf("read users for alerts: %w", err)
	}
	defer rows.Close()
	var users []userUsage
	for rows.Next() {
		var u userUsage
		if err := u.scan(rows); err != nil {
			return fmt.Errorf("read user usage for alerts: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read alert users: %w", err)
	}

	for _, u := range users {
		if err := r.recordUserUsage(ctx, u); err != nil {
			return err
		}
	}
	return nil
}

func (r eventRecorder) recordUserUsage(ctx context.Context, u userUsage) error {
	used := addUsage(u.up, u.down)
	for _, item := range []struct {
		kind  string
		level int
	}{{userQuota80, 80}, {userQuota100, 100}} {
		if thresholdReached(used, u.limit, item.level) {
			if _, err := r.record(ctx, u.alert(item.kind, fmt.Sprint(u.epoch), item.level)); err != nil {
				return err
			}
		}
	}
	if u.expiry == 0 || u.expiry > r.now.Add(72*time.Hour).Unix() {
		return nil
	}
	kind := userExpiring
	if u.expiry <= r.now.Unix() {
		kind = userExpired
	}
	_, err := r.record(ctx, u.alert(kind, fmt.Sprint(u.expiry), 0))
	return err
}

// UserStopped runs in the local Apply or remote quota transition transaction.
// It records the same event and outbox rows for both paths.
func (s *Service) UserStopped(ctx context.Context, tx *sql.Tx, id int64, why string, now time.Time) error {
	r, err := recorder(ctx, tx, now)
	if err != nil {
		return err
	}
	var u userUsage
	if err := u.scan(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id)); err != nil {
		return fmt.Errorf("read stopped user: %w", err)
	}
	if err := r.recordUserUsage(ctx, u); err != nil {
		return err
	}
	period := u.epoch
	if why == "expired" {
		period = u.expiry
	}
	event := u.alert(userStopped, fmt.Sprintf("%s:%d", why, period), 0)
	if why == "expired" {
		event.Deadline = u.expiry
	}
	_, err = r.record(ctx, event)
	return err
}

func (r eventRecorder) serverKey(kind string, period int64) string {
	return fmt.Sprintf("server:%d:%s:%d", r.serverID, kind, period)
}

// ServerAvailability runs in the same transaction as the node status transition.
func (s *Service) ServerAvailability(ctx context.Context, tx *sql.Tx, id int64, address string, lastSeen, now time.Time, recovered bool) error {
	r, err := recorder(ctx, tx, now)
	if err != nil || !r.cfg.Events[serverOffline] {
		return err
	}
	r.serverID, r.address = id, address
	key := r.serverKey("offline", lastSeen.Unix())
	event := Event{Key: key, Kind: serverOffline}
	if recovered {
		var wasAlerted bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM alert_events WHERE event_key=?)", eventDigest(key)).Scan(&wasAlerted); err != nil {
			return fmt.Errorf("find matching offline alert: %w", err)
		}
		if !wasAlerted {
			return nil
		}
		event = Event{Key: r.serverKey("recovered", lastSeen.Unix()), Kind: serverRecovered, OfflineSeconds: max(0, now.Unix()-lastSeen.Unix())}
	}
	_, err = r.record(ctx, event)
	return err
}

func (r eventRecorder) coreEvent(ctx context.Context, running bool) error {
	state, err := db.GetSetting(ctx, r.tx, db.AlertCore)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	before := state
	if running {
		if state.Alerted {
			if _, err := r.record(ctx, Event{Key: r.serverKey(coreRecovered, state.Incident), Kind: coreRecovered}); err != nil {
				return err
			}
		}
		state.Failures, state.Alerted = 0, false
	} else {
		state.Failures = min(2, state.Failures+1)
		if state.Failures == 2 && !state.Alerted && r.cfg.Events[coreDown] {
			state.Incident++
			state.Alerted, err = r.record(ctx, Event{Key: r.serverKey(coreDown, state.Incident), Kind: coreDown})
			if err != nil {
				return err
			}
		}
	}
	if state == before {
		return nil
	}
	return db.SetSetting(ctx, r.tx, db.AlertCore, state)
}

func (s *Service) evaluate(ctx context.Context, now time.Time) error {
	var cert acme.Status
	if s.opts.Certificate != nil {
		status, err := s.opts.Certificate(ctx)
		s.log(err)
		if err == nil {
			cert = status
		}
	}
	running := s.opts.CoreRunning != nil && s.opts.CoreRunning(ctx)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("evaluate alerts: %w", err)
	}
	defer tx.Rollback()
	r, err := recorder(ctx, tx, now)
	if err != nil {
		return err
	}
	if err := r.userEvents(ctx); err != nil {
		return err
	}
	if err := r.serverBillingEvents(ctx, s.opts.Network != nil); err != nil {
		return err
	}
	if cert.Mode == "acme" && cert.Warning {
		if _, err := r.record(ctx, Event{Key: r.serverKey(certWarning, cert.NotAfter), Kind: certWarning, Deadline: cert.NotAfter}); err != nil {
			return err
		}
	}
	if s.opts.CoreRunning != nil {
		if err := r.coreEvent(ctx, running); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit alert events: %w", err)
	}
	return nil
}

func (r eventRecorder) serverBillingEvents(ctx context.Context, localSupported bool) error {
	if _, err := r.tx.ExecContext(ctx, `DELETE FROM server_traffic_samples WHERE server_id IN
		(SELECT id FROM servers WHERE is_local=0) AND sampled_at<?`, r.now.AddDate(0, 0, -62).UnixNano()); err != nil {
		return fmt.Errorf("prune remote interface samples: %w", err)
	}
	if localSupported {
		if err := r.billingEvents(ctx, true); err != nil {
			return err
		}
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT id,public_host,alert_preferences FROM servers
		WHERE is_local=0 AND status<>'removed' AND json_extract(alert_preferences,'$.quota_gb')>0`)
	if err != nil {
		return fmt.Errorf("list remote billing preferences: %w", err)
	}
	defer rows.Close()
	var servers []billingServer
	for rows.Next() {
		var server billingServer
		if err := rows.Scan(&server.id, &server.address, &server.preferences); err != nil {
			return fmt.Errorf("read remote billing preferences: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate remote billing preferences: %w", err)
	}
	for _, server := range servers {
		r.serverID, r.address = server.id, server.address
		r.cfg, err = decodeConfig(server.preferences)
		if err != nil {
			return err
		}
		if err := r.billingEvents(ctx, false); err != nil {
			return err
		}
	}
	return nil
}

func (r eventRecorder) billingEvents(ctx context.Context, local bool) error {
	if r.cfg.QuotaGB <= 0 {
		return nil
	}
	usage, err := billing(ctx, r.tx, r.serverID, r.cfg, r.now, true)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		kind  string
		level int
	}{{serverQuota80, 80}, {serverQuota90, 90}, {serverQuota100, 100}} {
		if !local && item.level == 90 || !thresholdReached(usage.Used, usage.Quota, item.level) {
			continue
		}
		if _, err := r.record(ctx, Event{Key: r.serverKey(item.kind, usage.StartsAt), Kind: item.kind,
			Used: usage.Used, Limit: usage.Quota, Threshold: item.level}); err != nil {
			return err
		}
	}
	return nil
}
