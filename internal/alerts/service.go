// Package alerts collects local billing usage, records alerts and delivers notifications.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/db"
)

const bytesPerGB = 1_000_000_000
const maxQuotaGB = math.MaxInt64 / bytesPerGB

type Options struct {
	Network     *Network
	CoreRunning func(context.Context) bool
	Certificate func(context.Context) (acme.Status, error)
	Logger      *slog.Logger
	Now         func() time.Time
}

type Service struct {
	writer, reader *sql.DB
	opts           Options
	sender         *httpsSender
	previous       *trafficSample
	pending        *trafficSample
}

func New(writer, reader *sql.DB, opts Options) *Service {
	if reader == nil {
		reader = writer
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{writer: writer, reader: reader, opts: opts, sender: newHTTPSSender()}
}

func loadConfig(ctx context.Context, q db.SettingsDB) (db.AlertConfig, error) {
	var cfg db.AlertConfig
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT alert_preferences FROM servers WHERE is_local=1").Scan(&raw); err != nil {
		return cfg, fmt.Errorf("read local server alert settings: %w", err)
	}
	if strings.TrimSpace(raw) == "null" {
		return cfg, fmt.Errorf("decode local server alert settings: expected an object")
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("decode local server alert settings: %w", err)
	}
	if cfg.Events == nil {
		cfg.Events = make(map[string]bool, len(eventTitles))
	}
	for kind := range eventTitles {
		if _, ok := cfg.Events[kind]; !ok {
			cfg.Events[kind] = true
		}
	}
	return cfg, nil
}

func loadChannels(ctx context.Context, q db.SettingsDB) (map[string]db.AlertChannel, error) {
	cfg, err := db.GetSetting(ctx, q, db.AlertChannels)
	if errors.Is(err, sql.ErrNoRows) {
		cfg, err = make(map[string]db.AlertChannel), nil
	}
	if err == nil && cfg[Bark].URL == "" {
		bark := cfg[Bark]
		bark.URL = DefaultBarkURL
		cfg[Bark] = bark
	}
	return cfg, err
}

type Settings struct {
	Config     db.AlertConfig         `json:"config"`
	Channels   map[string]ChannelView `json:"channels"`
	Interfaces []string               `json:"interfaces"`
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	out := Settings{Channels: make(map[string]ChannelView, len(channels)), Interfaces: []string{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("read alert settings: %w", err)
	}
	defer tx.Rollback()
	if out.Config, err = loadConfig(ctx, tx); err != nil {
		return out, err
	}
	saved, err := loadChannels(ctx, tx)
	if err != nil {
		return out, err
	}
	for _, channel := range channels {
		out.Channels[channel] = channelView(saved[channel])
	}
	tx.Rollback()
	if s.opts.Network != nil {
		if names, err := s.opts.Network.interfaces(); err == nil {
			out.Interfaces = names
		}
	}
	return out, nil
}

type ConfigUpdate struct {
	Interface *string          `json:"interface"`
	QuotaGB   *int64           `json:"quota_gb"`
	Day       *int             `json:"billing_day"`
	Mode      *string          `json:"billing_mode"`
	Events    map[string]*bool `json:"events"`
}

func (s *Service) SaveConfig(ctx context.Context, in ConfigUpdate) (db.AlertConfig, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return db.AlertConfig{}, fmt.Errorf("write alert settings: %w", err)
	}
	defer tx.Rollback()
	cfg, err := loadConfig(ctx, tx)
	if err != nil {
		return cfg, err
	}
	if in.Interface != nil {
		cfg.Interface = *in.Interface
	}
	if !validInterface(cfg.Interface) {
		return cfg, invalid("interface")
	}
	if in.QuotaGB != nil {
		cfg.QuotaGB = *in.QuotaGB
	}
	if cfg.QuotaGB < 0 || cfg.QuotaGB > maxQuotaGB {
		return cfg, invalid("quota_gb")
	}
	if in.Day != nil {
		cfg.Day = *in.Day
	}
	if cfg.Day < 1 || cfg.Day > 28 {
		return cfg, invalid("billing_day")
	}
	if in.Mode != nil {
		cfg.Mode = *in.Mode
	}
	switch cfg.Mode {
	case "out", "both", "max", "in":
	default:
		return cfg, invalid("billing_mode")
	}
	for kind, value := range in.Events {
		if _, ok := eventTitles[kind]; !ok || value == nil {
			return cfg, invalid("events")
		}
		cfg.Events[kind] = *value
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, fmt.Errorf("encode local server alert settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE servers SET alert_preferences=?, updated_at=unixepoch() WHERE is_local=1", string(raw)); err != nil {
		return cfg, fmt.Errorf("save local server alert settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return cfg, fmt.Errorf("commit alert settings: %w", err)
	}
	return cfg, nil
}

func (s *Service) SaveChannel(ctx context.Context, channel string, in ChannelUpdate) (ChannelView, error) {
	if !KnownChannel(channel) {
		return ChannelView{}, invalid("channel")
	}
	// DNS must finish before acquiring SQLite's write lock.
	if channel == Bark && in.URL != nil {
		next, err := updateChannel(Bark, db.AlertChannel{}, ChannelUpdate{URL: in.URL})
		if err != nil {
			return ChannelView{}, err
		}
		if err := s.sender.validateBark(ctx, next.URL); err != nil {
			return ChannelView{}, invalid("bark_destination")
		}
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return ChannelView{}, fmt.Errorf("write notification channel: %w", err)
	}
	defer tx.Rollback()
	all, err := loadChannels(ctx, tx)
	if err != nil {
		return ChannelView{}, err
	}
	cfg, err := updateChannel(channel, all[channel], in)
	if err != nil {
		return ChannelView{}, err
	}
	all[channel] = cfg
	if err := db.SetSetting(ctx, tx, db.AlertChannels, all); err != nil {
		return ChannelView{}, err
	}
	if !cfg.Enabled || !channelReady(channel, cfg) {
		if _, err := tx.ExecContext(ctx, `UPDATE alert_outbox SET status='failed', error='channel_disabled', updated_at=?
			WHERE channel=? AND status='pending' AND (event_key IS NOT NULL OR ?)`, s.opts.Now().Unix(), channel, !channelReady(channel, cfg)); err != nil {
			return ChannelView{}, fmt.Errorf("close disabled channel notifications: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ChannelView{}, fmt.Errorf("commit notification channel: %w", err)
	}
	return channelView(cfg), nil
}

func (s *Service) log(err error) {
	if err != nil {
		s.opts.Logger.Warn("告警任务未完成；将自动重试，请检查数据库、网卡与系统服务状态。")
	}
}

// Run isolates sampling and each channel so an unresponsive provider cannot
// delay another channel or the dashboard fallback.
func (s *Service) Run(ctx context.Context) {
	defer s.sender.client.CloseIdleConnections()
	done := make(chan struct{}, len(channels))
	for _, channel := range channels {
		go func() {
			defer func() { done <- struct{}{} }()
			s.deliverLoop(ctx, channel)
		}()
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for ctx.Err() == nil {
		now := s.opts.Now()
		s.log(s.sampleTraffic(ctx, now))
		s.log(s.evaluate(ctx, now))
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	for range channels {
		<-done
	}
}
