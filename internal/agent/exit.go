package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
)

var ErrExitRemoved = errors.New("server removed; clear its managed exit links")

type ExitReport struct {
	Probe *exitlink.Probe  `json:"probe,omitempty"`
	Usage *exitlink.Totals `json:"usage,omitempty"`
	Ports string           `json:"ports,omitempty"`
}

func (r *ExitReport) Valid(protocol int) bool {
	if r == nil {
		return true
	}
	if protocol < ExitProtocol || r.Usage != nil && !r.Usage.Valid() {
		return false
	}
	if r.Ports != "" {
		if _, err := exitlink.DecodePorts(r.Ports); err != nil {
			return false
		}
	}
	p := r.Probe
	return p == nil || p.ID > 0 && p.Version > 0 && p.CheckedAt > 0 && p.Available != nil &&
		(p.Failures == 2 && !*p.Available || p.Failures >= 0 && p.Failures < 2 && *p.Available)
}

func (m *Master) recordExitReport(ctx context.Context, tx *sql.Tx, entryID int64, report *ExitReport, now time.Time) error {
	if report == nil {
		return nil
	}
	if report.Ports != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE servers SET exit_ports=?,exit_ports_at=? WHERE id=?", report.Ports, now.Unix(), entryID); err != nil {
			return fmt.Errorf("store node exit port inventory: %w", err)
		}
	}
	if report.Usage != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET exit_traffic_up=max(exit_traffic_up,?),
			exit_traffic_down=max(exit_traffic_down,?) WHERE id=?`, report.Usage.Up, report.Usage.Down, entryID); err != nil {
			return fmt.Errorf("store node exit totals: %w", err)
		}
	}
	if p := report.Probe; p != nil && p.CheckedAt >= now.Add(-exitlink.ProbeExpires).Unix() && p.CheckedAt <= now.Unix()+maxClockSkew {
		return db.RecordExitProbe(ctx, tx, entryID, *p, func(targetIP string, incident int64, recovered bool) error {
			if m.OnExitAvailability == nil {
				return nil
			}
			return m.OnExitAvailability(ctx, tx, entryID, targetIP, time.Unix(incident, 0), now, recovered)
		})
	}
	return nil
}

func replaceExitMirror(ctx context.Context, tx *sql.Tx, cfg *exitlink.Configuration) error {
	if cfg == nil || cfg.Empty() {
		if err := db.DeleteSetting(ctx, tx, db.AgentExit); err != nil {
			return err
		}
		return db.DeleteSetting(ctx, tx, db.ExitProbe)
	}
	probe, err := db.GetSetting(ctx, tx, db.ExitProbe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cfg.Outgoing == nil || cfg.Outgoing.Revision != probe.Revision {
		if err := db.DeleteSetting(ctx, tx, db.ExitProbe); err != nil {
			return err
		}
	}
	return db.SetSetting(ctx, tx, db.AgentExit, *cfg)
}

func (n *Node) exitReport(ctx context.Context) *ExitReport {
	report := &ExitReport{}
	if n.ExitPorts != nil {
		read, cancel := context.WithTimeout(ctx, time.Second)
		sockets, err := n.ExitPorts(read)
		cancel()
		if err == nil {
			var ports []int
			for s := range sockets {
				ports = append(ports, s.Port)
			}
			report.Ports = exitlink.EncodePorts(ports)
		}
	}
	if p, err := db.GetSetting(ctx, n.Reader, db.ExitProbe); err == nil && p.Available != nil {
		report.Probe = &p
	}
	if s, err := db.GetSetting(ctx, n.Reader, db.ExitUsage); err == nil {
		report.Usage = &s.Total
	}
	if report.Ports == "" && report.Probe == nil && report.Usage == nil {
		return nil
	}
	return report
}

func (n *Node) clearRemovedExit(ctx context.Context) error {
	if _, err := db.GetSetting(ctx, n.Reader, db.AgentExit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return n.Manager.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := n.verifyIdentity(ctx, tx); err != nil {
			return err
		}
		return replaceExitMirror(ctx, tx, nil)
	})
}
