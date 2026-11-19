package alerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

// Network isolates Linux filesystem and route commands. Nil disables sampling.
type Network struct {
	Files  host.Files
	Runner host.Runner
}

var interfacePattern = regexp.MustCompile(`^[^[:space:]/:\\]{1,15}$`)

func validInterface(name string) bool {
	return name == "" || (name != "lo" && name != "." && name != ".." && !strings.ContainsAny(name, "\x00") && len(name) <= 15 && interfacePattern.MatchString(name))
}

func (n Network) interfaces() ([]string, error) {
	counters, err := sysinfo.ReadNetworkCounters(n.Files)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(counters))
	for name := range counters {
		if validInterface(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

func routeDevice(data []byte) string {
	best := ""
	lowest := uint64(math.MaxUint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		var name string
		var metric uint64
		for i := 1; i+1 < len(fields); i++ {
			switch fields[i] {
			case "dev":
				if name == "" {
					name = fields[i+1]
				}
			case "metric":
				metric, _ = strconv.ParseUint(fields[i+1], 10, 64)
			}
		}
		if name != "" && validInterface(name) && metric < lowest {
			best, lowest = name, metric
		}
	}
	return best
}

func (n Network) read(ctx context.Context, name string) (string, sysinfo.NetworkCounters, error) {
	if name == "" {
		for _, family := range []string{"-4", "-6"} {
			out, err := n.Runner.Run(ctx, "ip", family, "route", "show", "default")
			if err != nil {
				return "", sysinfo.NetworkCounters{}, fmt.Errorf("read default route: %w", err)
			}
			if name = routeDevice(out); name != "" {
				break
			}
		}
	}
	counters, err := sysinfo.ReadNetworkCounters(n.Files)
	if err != nil {
		return "", sysinfo.NetworkCounters{}, err
	}
	current, ok := counters[name]
	if !ok {
		return "", sysinfo.NetworkCounters{}, fmt.Errorf("billing interface unavailable")
	}
	return name, current, nil
}

type trafficSample struct {
	at       time.Time
	name     string
	counters sysinfo.NetworkCounters
	rx, tx   int64
}

func counterDelta(previous, current uint64) int64 {
	if current >= previous {
		current -= previous
	}
	return int64(min(current, math.MaxInt64))
}

func (s *Service) sampleTraffic(ctx context.Context, now time.Time) error {
	if s.opts.Network == nil {
		return nil
	}
	if s.pending != nil {
		if err := s.persistSample(ctx); err != nil {
			return err
		}
	}
	cfg, err := loadConfig(ctx, s.reader)
	if err != nil {
		return err
	}
	name, counts, err := s.opts.Network.read(ctx, cfg.Interface)
	if err != nil {
		return err
	}
	next := &trafficSample{at: now, name: name, counters: counts}
	if prev := s.previous; prev != nil {
		if !now.After(prev.at) {
			return nil
		}
		if prev.name == name {
			next.rx = counterDelta(prev.counters.RX, counts.RX)
			next.tx = counterDelta(prev.counters.TX, counts.TX)
		}
	}
	s.pending = next
	return s.persistSample(ctx)
}

func (s *Service) persistSample(ctx context.Context) error {
	sample := s.pending
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write interface sample: %w", err)
	}
	defer tx.Rollback()
	// Retry the same timestamp after a write failure or uncertain commit.
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_traffic_samples (server_id,sampled_at,interface,rx,tx)
		VALUES(?,?,?,?,?) ON CONFLICT(server_id,sampled_at) DO NOTHING`, localServer, sample.at.UnixNano(), sample.name, sample.rx, sample.tx); err != nil {
		return fmt.Errorf("write interface delta: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM server_traffic_samples WHERE server_id=? AND sampled_at<?", localServer, sample.at.AddDate(0, 0, -62).UnixNano()); err != nil {
		return fmt.Errorf("prune old interface samples: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit interface sample: %w", err)
	}
	s.previous, s.pending = sample, nil
	return nil
}

type Billing struct {
	Supported bool    `json:"supported"`
	Interface string  `json:"interface"`
	SampledAt int64   `json:"sampled_at"`
	Stale     bool    `json:"stale"`
	StartsAt  int64   `json:"starts_at"`
	EndsAt    int64   `json:"ends_at"`
	Mode      string  `json:"billing_mode"`
	RX        int64   `json:"rx"`
	TX        int64   `json:"tx"`
	Used      int64   `json:"used"`
	Quota     int64   `json:"quota"`
	Percent   float64 `json:"percent"`
}

func billingPeriod(now time.Time, day int) (time.Time, time.Time) {
	utc := now.UTC()
	start := time.Date(utc.Year(), utc.Month(), day, 0, 0, 0, 0, time.UTC)
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start, start.AddDate(0, 1, 0)
}

func addUsage(a, b int64) int64 {
	return a + min(b, math.MaxInt64-a)
}

func percent(used, quota int64) float64 {
	if quota > 0 {
		return 100 * float64(used) / float64(quota)
	}
	return 0
}

func billing(ctx context.Context, tx *sql.Tx, cfg db.AlertConfig, now time.Time, supported bool) (Billing, error) {
	start, end := billingPeriod(now, cfg.Day)
	result := Billing{Supported: supported, StartsAt: start.Unix(), EndsAt: end.Unix(), Mode: cfg.Mode, Quota: cfg.QuotaGB * bytesPerGB}
	var last int64
	err := tx.QueryRowContext(ctx, `SELECT sampled_at,interface FROM server_traffic_samples
		WHERE server_id=? AND sampled_at<=? ORDER BY sampled_at DESC LIMIT 1`, localServer, now.UnixNano()).Scan(&last, &result.Interface)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("read last interface sample: %w", err)
	}
	if last != 0 {
		sampled := time.Unix(0, last)
		result.SampledAt = sampled.Unix()
		result.Stale = now.Sub(sampled) > 3*time.Minute || cfg.Interface != "" && cfg.Interface != result.Interface
	}

	rows, err := tx.QueryContext(ctx, `SELECT rx,tx FROM server_traffic_samples
		WHERE server_id=? AND sampled_at>=? AND sampled_at<? AND sampled_at<=?`, localServer, start.UnixNano(), end.UnixNano(), now.UnixNano())
	if err != nil {
		return result, fmt.Errorf("read billing cycle: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rx, sent int64
		if err := rows.Scan(&rx, &sent); err != nil {
			return result, fmt.Errorf("read interface usage: %w", err)
		}
		result.RX, result.TX = addUsage(result.RX, rx), addUsage(result.TX, sent)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("read interface samples: %w", err)
	}
	switch cfg.Mode {
	case "both":
		result.Used = addUsage(result.RX, result.TX)
	case "in":
		result.Used = result.RX
	case "max":
		result.Used = max(result.RX, result.TX)
	default:
		result.Used = result.TX
	}
	result.Percent = percent(result.Used, result.Quota)
	return result, nil
}
