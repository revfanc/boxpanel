package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
	"github.com/revfanc/boxpanel/internal/traffic"
)

type ExitMonitor struct {
	Writer, Reader *sql.DB
	Manager        interface {
		Apply(context.Context, singbox.Mutation) error
	}
	DetectIP        sysinfo.IPProbe
	ManageAddresses bool
	nextIPProbe     time.Time
	nextRuleRefresh map[string]time.Time
	fetchRules      func(context.Context, string) (exitlink.DestinationRules, error)
	BootID          string
	Logger          *slog.Logger
	Maintenance     func(context.Context) (func(), bool)
	OnTransition    func(context.Context, *sql.Tx, int64, string, time.Time, time.Time, bool) error

	// Test seams for the loopback controller and kernel.
	probe    func(context.Context, int, string, exitlink.Revision) (bool, error)
	counters func(context.Context) (exitlink.Totals, bool, error)
	now      func() time.Time
}

func (m ExitMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(exitlink.ProbeEvery)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *ExitMonitor) tick(ctx context.Context) {
	if m.Maintenance != nil {
		release, ok := m.Maintenance(ctx)
		if !ok {
			return
		}
		defer release()
	}
	if now := m.currentTime(); m.DetectIP != nil && !now.Before(m.nextIPProbe) {
		m.nextIPProbe = now.Add(5 * time.Minute)
		if err := m.samplePublicIP(ctx); err != nil && ctx.Err() == nil && m.Logger != nil {
			m.Logger.Warn("公网 IP 探测未完成，请检查服务器出站 HTTPS 和 DNS。系统将在五分钟后重试。")
		}
	}
	if m.ManageAddresses {
		if err := m.ReconcilePublicIPs(ctx); err != nil && ctx.Err() == nil && m.Logger != nil {
			m.Logger.Warn("公网 IP 变更尚未生效，请检查 sing-box 和防火墙。系统将在下一轮重试。")
		}
	}
	if err := m.sampleUsage(ctx); err != nil && ctx.Err() == nil && m.Logger != nil {
		m.Logger.Warn("出口流量采集未完成。请检查 nftables、内核计数器和数据目录权限。")
	}
	if err := m.checkLink(ctx); err != nil && ctx.Err() == nil && m.Logger != nil {
		m.Logger.Warn("未能应用出口探测结果。请检查入口服务器的同步和 sing-box 状态。")
	}
	if err := m.refreshExitRules(ctx); err != nil && ctx.Err() == nil && m.Logger != nil {
		m.Logger.Warn("部分出口规则更新失败，受影响的预设继续使用已有规则。请检查规则源连接和 sing-box 状态。", "error", err)
	}
}

func (m ExitMonitor) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m ExitMonitor) checkLink(ctx context.Context) error {
	config, err := db.ReadLocalExit(ctx, m.Reader)
	if err != nil || config.Outgoing == nil {
		return err
	}
	revision := config.Outgoing.Revision
	port, err := db.GetSetting(ctx, m.Reader, db.ClashAPIPort)
	if err != nil {
		return err
	}
	secret, err := db.GetSetting(ctx, m.Reader, db.ClashAPISecret)
	if err != nil {
		return err
	}
	before, err := db.GetSetting(ctx, m.Reader, db.ExitProbe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	probe := m.probe
	if probe == nil {
		probe = probeExit
	}
	success, err := probe(ctx, port, secret, revision)
	if err != nil {
		return err
	}
	now := m.currentTime()
	result := before.WithResult(revision, now, success)
	update := func(result exitlink.Probe) singbox.Mutation {
		return func(ctx context.Context, tx *sql.Tx) error {
			local, err := db.LocalServerID(ctx, tx)
			if err != nil {
				return err
			}
			current, err := db.LocalExitConfig(ctx, tx, local)
			if err != nil {
				return err
			}
			if current.Outgoing == nil || current.Outgoing.Revision != revision {
				return errObsoleteExitProbe
			}
			prior, err := db.GetSetting(ctx, tx, db.ExitProbe)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if prior.Revision == revision && prior.CheckedAt >= result.CheckedAt {
				return errObsoleteExitProbe
			}
			if err := db.SetSetting(ctx, tx, db.ExitProbe, result); err != nil {
				return err
			}
			return db.RecordExitProbe(ctx, tx, local, result, func(ip string, incident int64, recovered bool) error {
				if m.OnTransition == nil {
					return nil
				}
				return m.OnTransition(ctx, tx, local, ip, time.Unix(incident, 0), now, recovered)
			})
		}
	}
	if result.Available != nil && result.PolicyDown != !*result.Available {
		applied := result
		applied.PolicyDown = !*result.Available
		err = m.Manager.Apply(ctx, update(applied))
		if err != nil && !errors.Is(err, errObsoleteExitProbe) {
			// Report the observed outage even when the core rejects the route
			// change. Leave the previous policy effective and retry next tick.
			err = errors.Join(err, m.update(ctx, update(result)))
		}
	} else {
		err = m.update(ctx, update(result))
	}
	if errors.Is(err, errObsoleteExitProbe) {
		return nil
	}
	return err
}

var errObsoleteExitProbe = errors.New("exit probe superseded by a newer configuration or sample")

func probeExit(ctx context.Context, port int, secret string, revision exitlink.Revision) (bool, error) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, target := range []string{"https://cp.cloudflare.com/generate_204", "https://www.gstatic.com/generate_204"} {
		u := url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port), Path: "/proxies/" + revision.ProbeTag() + "/delay"}
		u.RawQuery = url.Values{"url": {target}, "timeout": {"4000"}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return false, fmt.Errorf("create local exit probe: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Errorf("request local exit probe: %w", err)
		}
		var result struct {
			Delay int `json:"delay"`
		}
		decoded := json.NewDecoder(io.LimitReader(resp.Body, 2048)).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && decoded == nil && result.Delay > 0 {
			return true, nil
		}
		if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusGatewayTimeout {
			return false, fmt.Errorf("invalid local exit probe response: HTTP %d", resp.StatusCode)
		}
	}
	return false, nil
}

func (m ExitMonitor) sampleUsage(ctx context.Context) error {
	ever, err := db.GetSetting(ctx, m.Reader, db.ExitEver)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || !ever {
		return err
	}
	fetch := m.counters
	if fetch == nil {
		fetch = func(ctx context.Context) (exitlink.Totals, bool, error) { return firewall.ReadExitCounters(ctx, nil) }
	}
	sampleCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	raw, found, err := fetch(sampleCtx)
	stop()
	if err != nil || !found {
		return err
	}
	if !raw.Valid() {
		return fmt.Errorf("read exit accounting: negative counter")
	}
	return m.update(ctx, func(ctx context.Context, tx *sql.Tx) error {
		checkpoint, err := db.GetSetting(ctx, tx, db.ExitUsage)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if checkpoint.BootID == m.BootID && checkpoint.Raw == raw && err == nil {
			return nil
		}
		delta := raw
		if checkpoint.BootID == m.BootID {
			if raw.Up >= checkpoint.Raw.Up {
				delta.Up -= checkpoint.Raw.Up
			}
			if raw.Down >= checkpoint.Raw.Down {
				delta.Down -= checkpoint.Raw.Down
			}
		}
		total := exitlink.Totals{Up: traffic.AddBytes(checkpoint.Total.Up, delta.Up), Down: traffic.AddBytes(checkpoint.Total.Down, delta.Down)}
		if err := db.SetSetting(ctx, tx, db.ExitUsage, db.ExitCheckpoint{BootID: m.BootID, Raw: raw, Total: total}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE servers SET exit_traffic_up=?,exit_traffic_down=? WHERE is_local=1", total.Up, total.Down); err != nil {
			return fmt.Errorf("record local exit counters: %w", err)
		}
		return nil
	})
}

func (m ExitMonitor) update(ctx context.Context, update singbox.Mutation) error {
	tx, err := m.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin exit monitor transaction: %w", err)
	}
	defer tx.Rollback()
	if err := update(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit exit monitor transaction: %w", err)
	}
	return nil
}
