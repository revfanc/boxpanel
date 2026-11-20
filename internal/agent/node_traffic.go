package agent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/revfanc/boxpanel/internal/db"
)

type nodeCounter struct {
	TrafficCounter
	retired bool
}

func (n *Node) trafficSnapshot(ctx context.Context) ([]nodeCounter, error) {
	tx, err := n.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin node traffic snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id,epoch,up,down,1 FROM agent_retired_traffic
		UNION ALL SELECT id,traffic_epoch,traffic_up,traffic_down,0 FROM users WHERE traffic_up>0 OR traffic_down>0
		ORDER BY 2,1`)
	if err != nil {
		return nil, fmt.Errorf("read node traffic snapshot: %w", err)
	}
	defer rows.Close()
	var counters []nodeCounter
	for rows.Next() {
		var c nodeCounter
		if err := rows.Scan(&c.UserID, &c.Epoch, &c.Up, &c.Down, &c.retired); err != nil {
			return nil, fmt.Errorf("scan node traffic snapshot: %w", err)
		}
		if c.retired || n.reportedTraffic[c.UserID] != c.TrafficCounter {
			counters = append(counters, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node traffic snapshot: %w", err)
	}
	return counters, nil
}

func (n *Node) reportTraffic(ctx context.Context) error {
	counters, err := n.trafficSnapshot(ctx)
	if err != nil {
		return err
	}
	// Each encoded counter plus its separator fits within 120 bytes, even
	// with four full-width int64s. Leave room for the JSON envelope.
	const batchLimit = min(MaxTrafficUsers, (MaxTrafficBody-32)/120)
	for len(counters) > 0 {
		seen := make(map[int64]struct{})
		count := 0
		for count < min(len(counters), batchLimit) {
			id := counters[count].UserID
			if _, duplicate := seen[id]; duplicate {
				break
			}
			seen[id] = struct{}{}
			count++
		}
		if err := n.sendTrafficBatch(ctx, counters[:count]); err != nil {
			return err
		}
		counters = counters[count:]
	}
	return nil
}

func (n *Node) sendTrafficBatch(ctx context.Context, batch []nodeCounter) error {
	report := TrafficReport{Counters: make([]TrafficCounter, 0, len(batch))}
	var retired []nodeCounter
	for _, c := range batch {
		if n.reportedTraffic[c.UserID] != c.TrafficCounter {
			report.Counters = append(report.Counters, c.TrafficCounter)
		}
		if c.retired {
			retired = append(retired, c)
		}
	}
	if len(report.Counters) > 0 {
		if err := n.Client.ReportTraffic(ctx, n.Master.ServerID, n.Key, report); err != nil {
			return err
		}
	}
	if len(retired) > 0 {
		if err := n.acknowledgeRetired(ctx, retired); err != nil {
			return err
		}
	}
	if n.reportedTraffic == nil {
		n.reportedTraffic = make(map[int64]TrafficCounter)
	}
	for _, c := range batch {
		if c.retired {
			delete(n.reportedTraffic, c.UserID)
		} else {
			n.reportedTraffic[c.UserID] = c.TrafficCounter
		}
	}
	return nil
}

func (n *Node) acknowledgeRetired(ctx context.Context, counters []nodeCounter) error {
	conn, err := n.Writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve node traffic connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retiring acknowledged node counters: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return err
	}
	for _, c := range counters {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_retired_traffic WHERE user_id=? AND epoch=?`, c.UserID, c.Epoch); err != nil {
			return fmt.Errorf("delete acknowledged node counter: %w", err)
		}
	}
	if err := db.Commit(conn, tx); err != nil {
		return fmt.Errorf("commit acknowledged node counters: %w", err)
	}
	return nil
}
