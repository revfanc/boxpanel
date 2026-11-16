// Package traffic provides approximate per-user accounting using the pinned
// core's local Clash API. It never writes sing-box configuration.
package traffic

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
)

type Connection struct {
	ID       string   `json:"id"`
	Start    string   `json:"start"`
	Chains   []string `json:"chains"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
}
type FetchFunc func(context.Context, int, string) ([]Connection, error)
type account struct{ id, epoch int64 }
type amount struct{ up, down int64 }
type sample struct {
	account
	amount
	start string
}

type Collector struct {
	writer, reader *sql.DB
	fetch          FetchFunc
	mu             sync.Mutex
	previous       map[string]sample
	pending        map[account]amount
	batchID        string
	staged         map[account]amount
	// born marks when this collector started. A connection first seen now but
	// opened earlier survived a panel restart: its bytes were counted before.
	born time.Time
}

func NewCollector(writer, reader *sql.DB, fetch FetchFunc) *Collector {
	if fetch == nil {
		fetch = fetchConnections
	}
	return &Collector{writer: writer, reader: reader, fetch: fetch, previous: map[string]sample{}, pending: map[account]amount{}, born: time.Now()}
}

func fetchConnections(ctx context.Context, port int, secret string) ([]Connection, error) {
	if port < 1 || port > 65535 || secret == "" {
		return nil, fmt.Errorf("read traffic: invalid local controller settings")
	}
	// Ignore proxy environment variables and redirects: the bearer secret must
	// only ever reach this exact loopback controller.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/connections", nil)
	if err != nil {
		return nil, fmt.Errorf("prepare traffic request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read traffic: local controller unavailable; check sing-box status")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read traffic: controller rejected request; check sing-box status")
	}
	const limit = 16 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, fmt.Errorf("read traffic: incomplete or oversized response")
	}
	var body struct {
		Connections json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("read traffic: invalid controller response")
	}
	var connections []Connection
	if len(body.Connections) == 0 || json.Unmarshal(body.Connections, &connections) != nil {
		return nil, fmt.Errorf("read traffic: missing or invalid connections list")
	}
	return connections, nil
}

func userID(chains []string) int64 {
	var found int64
	for _, tag := range chains {
		if !strings.HasPrefix(tag, "direct-u") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(tag, "direct-u"), 10, 64)
		if err != nil || id <= 0 || tag != "direct-u"+strconv.FormatInt(id, 10) || found != 0 && found != id {
			return 0
		}
		found = id
	}
	return found
}

// openedBefore reports whether a Clash API start timestamp precedes t.
// Unparseable values count as new so no traffic is silently dropped.
func openedBefore(start string, t time.Time) bool {
	opened, err := time.Parse(time.RFC3339Nano, start)
	return err == nil && opened.Before(t)
}

func add(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// Poll only retains live connection baselines. The first observation counts
// bytes already transferred; start and counter rollback distinguish reused IDs.
func (c *Collector) Poll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("read accounting snapshot: %w", err)
	}
	defer tx.Rollback()
	port, err := db.GetSetting(ctx, tx, db.ClashAPIPort)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} // setup has not applied a core yet
	if err != nil {
		return err
	}
	secret, err := db.GetSetting(ctx, tx, db.ClashAPISecret)
	if err != nil {
		return err
	}
	epochs, err := readEpochs(ctx, tx)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("close accounting snapshot: %w", err)
	}
	connections, err := c.fetch(ctx, port, secret)
	if err != nil {
		return err
	}
	next := make(map[string]sample, len(connections))
	for _, conn := range connections {
		id := userID(conn.Chains)
		key, known := epochs[id]
		if !known || conn.ID == "" || conn.Upload < 0 || conn.Download < 0 {
			continue
		}
		if _, duplicate := next[conn.ID]; duplicate {
			return fmt.Errorf("read traffic: duplicate connection ID")
		}
		current := sample{account: key, amount: amount{conn.Upload, conn.Download}, start: conn.Start}
		next[conn.ID] = current
	}
	for id, current := range next {
		delta := current.amount
		if _, seen := c.previous[id]; !seen && openedBefore(current.start, c.born) {
			delta = amount{} // baseline only: avoid recounting after a panel restart
		}
		if old, ok := c.previous[id]; ok && old.id == current.id && old.start == current.start && current.up >= old.up && current.down >= old.down {
			delta = amount{current.up - old.up, current.down - old.down}
			if old.epoch != current.epoch {
				delta = amount{}
			}
			// A surviving connection spanning a reset is rebased without old bytes.
		}
		pending := c.pending[current.account]
		c.pending[current.account] = amount{add(pending.up, delta.up), add(pending.down, delta.down)}
	}
	c.previous = next
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
