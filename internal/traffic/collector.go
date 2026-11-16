// Package traffic accounts per-user Linux nftables counters.
// It never writes sing-box configuration.
package traffic

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
)

type FetchFunc func(context.Context) ([]firewall.Counter, error)
type account struct{ id, epoch int64 }
type amount struct{ up, down int64 }
type sample struct {
	account
	amount
}

type Collector struct {
	writer, reader *sql.DB
	fetch          FetchFunc
	mu             sync.Mutex
	previous       map[int64]sample
	initialized    bool
	pending        map[account]amount
	batchID        string
	staged         map[account]amount
}

func NewCollector(writer, reader *sql.DB, fetch FetchFunc) *Collector {
	if fetch == nil {
		fetch = func(ctx context.Context) ([]firewall.Counter, error) { return firewall.ReadCounters(ctx, nil) }
	}
	return &Collector{writer: writer, reader: reader, fetch: fetch, previous: map[int64]sample{}, pending: map[account]amount{}}
}

func add(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// Poll reads cumulative kernel counters after releasing the WAL snapshot.
// The first successful sample establishes a process baseline. New users count
// from zero; surviving users spanning an epoch change establish a new baseline.
func (c *Collector) Poll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("read accounting snapshot: %w", err)
	}
	defer tx.Rollback()
	active, err := db.GetSetting(ctx, tx, db.AppliedUsers)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	epochs, err := readEpochs(ctx, tx)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("close accounting snapshot: %w", err)
	}
	var counters []firewall.Counter
	if len(active) > 0 {
		counters, err = c.fetch(ctx)
		if err != nil {
			return err
		}
	}
	next := make(map[int64]sample, len(counters))
	for _, counter := range counters {
		key, known := epochs[counter.UserID]
		if !known {
			continue
		}
		if counter.Upload < 0 || counter.Download < 0 {
			return fmt.Errorf("read traffic: negative kernel counter; retry apply")
		}
		if _, duplicate := next[counter.UserID]; duplicate {
			return fmt.Errorf("read traffic: duplicate user counter")
		}
		next[counter.UserID] = sample{account: key, amount: amount{counter.Upload, counter.Download}}
	}
	for id, current := range next {
		delta := current.amount
		if !c.initialized {
			delta = amount{}
		} else if old, ok := c.previous[id]; ok {
			// Counter recreation can affect either direction independently.
			if current.up >= old.up {
				delta.up -= old.up
			}
			if current.down >= old.down {
				delta.down -= old.down
			}
			if old.epoch != current.epoch {
				delta = amount{}
			}
		}
		if delta.up == 0 && delta.down == 0 {
			continue
		}
		pending := c.pending[current.account]
		c.pending[current.account] = amount{add(pending.up, delta.up), add(pending.down, delta.down)}
	}
	c.previous = next
	c.initialized = true
	return nil
}
func readEpochs(ctx context.Context, tx *sql.Tx) (map[int64]account, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,traffic_epoch FROM users")
	if err != nil {
		return nil, fmt.Errorf("read accounting epochs: %w", err)
	}
	defer rows.Close()
	result := map[int64]account{}
	for rows.Next() {
		var id, epoch int64
		if err := rows.Scan(&id, &epoch); err != nil {
			return nil, fmt.Errorf("scan accounting epoch: %w", err)
		}
		result[id] = account{id, epoch}
	}
	return result, rows.Err()
}

// writePending must be called with mu held. The epoch comparison also handles
// resets committed by another process. Saturating arithmetic keeps SQLite from
// promoting overflowing integers to REAL.
func (c *Collector) writePending(ctx context.Context, tx *sql.Tx) error {
	// If the previous COMMIT outcome was uncertain, first resolve the batch
	// marker inside this transaction. Poll may have collected more since then.
	if c.batchID != "" {
		committed, err := db.GetSetting(ctx, tx, db.TrafficBatch)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if committed == c.batchID {
			for key, delta := range c.staged {
				current := c.pending[key]
				c.pending[key] = amount{max(0, current.up-delta.up), max(0, current.down-delta.down)}
			}
		}
		c.batchID = ""
		c.staged = nil
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("identify traffic batch: %w", err)
	}
	batchID := base64.RawURLEncoding.EncodeToString(random[:])
	staged := make(map[account]amount, len(c.pending))
	for key, delta := range c.pending {
		staged[key] = delta
		if _, err := tx.ExecContext(ctx, `UPDATE users SET
   traffic_up=min(traffic_up,9223372036854775807-?)+?,
   traffic_down=min(traffic_down,9223372036854775807-?)+?
   WHERE id=? AND traffic_epoch=?`, delta.up, delta.up, delta.down, delta.down, key.id, key.epoch); err != nil {
			return fmt.Errorf("persist traffic increments: %w", err)
		}
	}
	if err := db.SetSetting(ctx, tx, db.TrafficBatch, batchID); err != nil {
		return err
	}
	c.batchID, c.staged = batchID, staged
	return nil
}
func (c *Collector) clearPending() {
	clear(c.pending)
	c.batchID = ""
	c.staged = nil
}
func (c *Collector) Flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	tx, err := c.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin traffic flush: %w", err)
	}
	defer tx.Rollback()
	if err = c.writePending(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit traffic flush: %w", err)
	}
	c.clearPending()
	return nil
}
