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
	"maps"
	"math"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type FetchFunc func(context.Context) ([]firewall.Counter, error)
type account struct{ id, epoch int64 }
type amount struct{ up, down int64 }

type Collector struct {
	writer, reader  *sql.DB
	fetch           FetchFunc
	mu              sync.Mutex
	previous        map[int64]db.TrafficSample
	initialized     bool
	pending         map[account]amount
	batchID         string
	staged          map[account]amount
	bootID          string
	checkpointDirty bool
}

func NewCollector(writer, reader *sql.DB, fetch FetchFunc) *Collector {
	if fetch == nil {
		fetch = func(ctx context.Context) ([]firewall.Counter, error) { return firewall.ReadCounters(ctx, nil) }
	}
	return &Collector{writer: writer, reader: reader, fetch: fetch, pending: map[account]amount{}}
}

// NewNodeCollector resumes kernel accounting across a node process restart.
func NewNodeCollector(writer, reader *sql.DB, fetch FetchFunc, bootID string) *Collector {
	c := NewCollector(writer, reader, fetch)
	c.bootID = bootID
	return c
}

// AddBytes adds two nonnegative counters without overflowing an int64.
func AddBytes(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// Poll reads cumulative kernel counters after releasing the WAL snapshot.
// The first successful sample establishes a baseline if no checkpoint exists.
// New users count from zero; surviving users spanning an epoch change establish a new baseline.
func (c *Collector) Poll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.poll(ctx)
}

func (c *Collector) poll(ctx context.Context) error {
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
	previous, initialized := c.previous, c.initialized
	checkpointNeeded := false
	if !initialized && c.bootID != "" {
		checkpoint, err := db.GetSetting(ctx, tx, db.AgentTrafficCheckpoint)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		initialized = err == nil
		if checkpoint.BootID == c.bootID {
			previous = checkpoint.Samples
		} else {
			checkpointNeeded = true
		}
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
	next := make(map[int64]db.TrafficSample, len(counters))
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
		next[counter.UserID] = db.TrafficSample{Epoch: key.epoch, Up: counter.Upload, Down: counter.Download}
	}
	for id, current := range next {
		delta := amount{current.Up, current.Down}
		if !initialized {
			delta = amount{}
		} else if old, ok := previous[id]; ok {
			// Counter recreation can affect either direction independently.
			if current.Up >= old.Up {
				delta.up -= old.Up
			}
			if current.Down >= old.Down {
				delta.down -= old.Down
			}
			if old.Epoch != current.Epoch {
				delta = amount{}
			}
		}
		if delta.up == 0 && delta.down == 0 {
			continue
		}
		key := account{id, current.Epoch}
		pending := c.pending[key]
		c.pending[key] = amount{AddBytes(pending.up, delta.up), AddBytes(pending.down, delta.down)}
	}
	if c.bootID != "" && (checkpointNeeded || !maps.Equal(previous, next)) {
		c.checkpointDirty = true
	}
	c.previous = next
	c.initialized = true
	return nil
}

// ApplyMirror commits pending usage with the mirror so retiring a user can
// preserve that user's final counters. A failed apply keeps the pending usage.
func (c *Collector) ApplyMirror(ctx context.Context, applier Applier, mutation singbox.Mutation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sampling failure must not block the node from applying master policy.
	_ = c.poll(ctx)
	err := applier.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if len(c.pending) > 0 || c.checkpointDirty {
			if err := c.writePending(ctx, tx); err != nil {
				return err
			}
		}
		return mutation(ctx, tx)
	})
	if err == nil {
		c.clearPending()
	}
	return err
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
	if c.checkpointDirty {
		if err := db.SetSetting(ctx, tx, db.AgentTrafficCheckpoint, db.TrafficCheckpoint{BootID: c.bootID, Samples: c.previous}); err != nil {
			return err
		}
	}
	c.batchID, c.staged = batchID, staged
	return nil
}
func (c *Collector) clearPending() {
	clear(c.pending)
	c.batchID = ""
	c.staged = nil
	c.checkpointDirty = false
}
func (c *Collector) Flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 && !c.checkpointDirty {
		return nil
	}
	conn, err := c.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve traffic flush connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin traffic flush: %w", err)
	}
	defer tx.Rollback()
	if err = c.writePending(ctx, tx); err != nil {
		return err
	}
	if err = db.Commit(conn, tx); err != nil {
		return fmt.Errorf("commit traffic flush: %w", err)
	}
	c.clearPending()
	return nil
}
