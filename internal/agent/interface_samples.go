package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

var ErrInvalidInterfaceSamples = errors.New("invalid node interface samples")

func (m *Master) RecordInterfaceSamples(ctx context.Context, identity authenticated, report InterfaceSamples) error {
	if report.Samples == nil || len(report.Samples) > MaxInterfaceSamples {
		return ErrInvalidInterfaceSamples
	}
	now := m.now()
	latest := now.Add(maxClockSkew * time.Second).UnixNano()
	for _, sample := range report.Samples {
		if sample.SampledAt <= 0 || sample.SampledAt > latest || sample.RX < 0 || sample.TX < 0 || !sysinfo.ValidNetworkInterface(sample.Interface) {
			return ErrInvalidInterfaceSamples
		}
	}
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent interface report: %w", err)
	}
	defer tx.Rollback()
	if err := requireNodeProtocol(ctx, tx, identity); err != nil {
		return err
	}
	cutoff := now.AddDate(0, 0, -62).UnixNano()
	for _, sample := range report.Samples {
		if sample.SampledAt < cutoff {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO server_traffic_samples (server_id,sampled_at,interface,rx,tx)
			VALUES (?,?,?,?,?) ON CONFLICT(server_id,sampled_at) DO NOTHING`, identity.id, sample.SampledAt, sample.Interface, sample.RX, sample.TX); err != nil {
			return fmt.Errorf("store agent interface sample: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_traffic_samples WHERE server_id=? AND sampled_at<?`, identity.id, cutoff); err != nil {
		return fmt.Errorf("prune old agent interface samples: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent interface report: %w", err)
	}
	return nil
}

func (n *Node) readInterfaceBatch(ctx context.Context) ([]InterfaceSample, error) {
	tx, err := n.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin node interface snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return nil, err
	}
	ack, err := db.GetSetting(ctx, tx, db.AgentInterfaceAck)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sampled_at,interface,rx,tx FROM server_traffic_samples
		WHERE server_id=(SELECT id FROM servers WHERE is_local=1) AND sampled_at>?
		ORDER BY sampled_at LIMIT ?`, ack, MaxInterfaceSamples)
	if err != nil {
		return nil, fmt.Errorf("read pending node interface samples: %w", err)
	}
	defer rows.Close()
	samples := []InterfaceSample{}
	for rows.Next() {
		var sample InterfaceSample
		if err := rows.Scan(&sample.SampledAt, &sample.Interface, &sample.RX, &sample.TX); err != nil {
			return nil, fmt.Errorf("scan node interface sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node interface samples: %w", err)
	}
	return samples, nil
}

func (n *Node) reportInterfaceSamples(ctx context.Context) error {
	for {
		samples, err := n.readInterfaceBatch(ctx)
		if err != nil {
			return err
		}
		if len(samples) == 0 && n.interfaceEndpointReady {
			return nil
		}
		// Even with JSON escaping, a 15-byte device name and three int64s
		// fit in the 256 KiB limit for 1000 samples.
		if err := n.Client.ReportInterfaceSamples(ctx, n.Master.ServerID, n.Key, InterfaceSamples{Samples: samples}); err != nil {
			return err
		}
		n.interfaceEndpointReady = true
		if len(samples) == 0 {
			return nil
		}
		if err := n.acknowledgeInterfaces(ctx, samples[len(samples)-1].SampledAt); err != nil {
			return err
		}
		if len(samples) < MaxInterfaceSamples {
			return nil
		}
	}
}

func (n *Node) acknowledgeInterfaces(ctx context.Context, timestamp int64) error {
	tx, err := n.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin acknowledging node interface samples: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return err
	}
	ack, err := db.GetSetting(ctx, tx, db.AgentInterfaceAck)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if timestamp <= ack {
		return nil
	}
	if err := db.SetSetting(ctx, tx, db.AgentInterfaceAck, timestamp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit acknowledged node interface samples: %w", err)
	}
	return nil
}

func (n *Node) setInterfacePreference(ctx context.Context, name string) error {
	if n.interfacePreference != nil && *n.interfacePreference == name {
		return nil
	}
	if name != "" && !sysinfo.ValidNetworkInterface(name) {
		return fmt.Errorf("master returned an invalid interface preference")
	}
	tx, err := n.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin updating node interface preference: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE servers SET alert_preferences=json_set(alert_preferences,'$.interface',?) WHERE is_local=1`, name); err != nil {
		return fmt.Errorf("save node interface preference: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit node interface preference: %w", err)
	}
	n.interfacePreference = &name
	return nil
}
