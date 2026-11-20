package agent

import (
	"container/list"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/reality"
)

var ErrUnauthorized = errors.New("agent authentication failed")

type JoinToken struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
	ServerID  *int64 `json:"server_id,omitempty"`
}

type nonceKey struct {
	id    int64
	value string
}

type nonceEntry struct {
	key     nonceKey
	expires time.Time
}

type Master struct {
	// OnStopped records a quota stop for a user with no local configuration
	// to apply. It shares the transaction and deduplication with local stops.
	OnStopped      func(context.Context, *sql.Tx, int64, string, time.Time) error
	Logger         *slog.Logger
	writer, reader *sql.DB
	now            func() time.Time
	mu             sync.Mutex
	joinWindow     time.Time
	joins          map[string][]time.Time
	nonces         map[nonceKey]struct{}
	nonceOrder     list.List
}

func NewMaster(writer, reader *sql.DB, now func() time.Time) *Master {
	if reader == nil {
		reader = writer
	}
	if now == nil {
		now = time.Now
	}
	return &Master{writer: writer, reader: reader, now: now, Logger: slog.Default(), joins: make(map[string][]time.Time), nonces: make(map[nonceKey]struct{})}
}

func (m *Master) AllowJoin(peer string) (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cutoff := now.Add(-time.Minute)
	if !now.Before(m.joinWindow) {
		for ip, attempts := range m.joins {
			if !attempts[len(attempts)-1].After(cutoff) {
				delete(m.joins, ip)
			}
		}
		m.joinWindow = now.Add(time.Minute)
	}
	attempts, known := m.joins[peer]
	if !known && len(m.joins) >= 4096 {
		return false, retrySeconds(m.joinWindow.Sub(now))
	}
	first := 0
	for first < len(attempts) && !attempts[first].After(cutoff) {
		first++
	}
	attempts = attempts[:copy(attempts, attempts[first:])]
	if len(attempts) >= 10 {
		return false, retrySeconds(attempts[0].Add(time.Minute).Sub(now))
	}
	m.joins[peer] = append(attempts, now)
	return true, 0
}

func retrySeconds(delay time.Duration) int {
	return max(1, int((delay+time.Second-1)/time.Second))
}

func (m *Master) IssueToken(ctx context.Context, name string) (JoinToken, string, error) {
	row := JoinToken{Name: name, ExpiresAt: m.now().Add(time.Hour).Unix()}
	token, err := auth.RandomToken(32)
	if err != nil {
		return row, "", fmt.Errorf("generate join token: %w", err)
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return row, "", fmt.Errorf("begin token issuance: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM agent_join_tokens WHERE expires_at<=?", m.now().Unix()); err != nil {
		return row, "", fmt.Errorf("prune expired join tokens: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "INSERT INTO agent_join_tokens (token_hash,name,expires_at) VALUES (?,?,?) RETURNING id",
		hash[:], name, row.ExpiresAt).Scan(&row.ID); err != nil {
		return row, "", fmt.Errorf("store join token digest: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return row, "", fmt.Errorf("commit token issuance: %w", err)
	}
	return row, token, nil
}

func (m *Master) Tokens(ctx context.Context) ([]JoinToken, error) {
	rows, err := m.reader.QueryContext(ctx, "SELECT id,name,expires_at FROM agent_join_tokens WHERE expires_at>? AND server_id IS NULL ORDER BY id DESC", m.now().Unix())
	if err != nil {
		return nil, fmt.Errorf("list join tokens: %w", err)
	}
	defer rows.Close()
	tokens := []JoinToken{}
	for rows.Next() {
		var token JoinToken
		if err := rows.Scan(&token.ID, &token.Name, &token.ExpiresAt); err != nil {
			return nil, fmt.Errorf("read join token metadata: %w", err)
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (m *Master) Token(ctx context.Context, id int64) (JoinToken, error) {
	var token JoinToken
	if err := m.reader.QueryRowContext(ctx, `SELECT id,name,expires_at,server_id FROM agent_join_tokens
		WHERE id=? AND expires_at>? AND (server_id IS NULL OR EXISTS
			(SELECT 1 FROM servers WHERE id=server_id AND status<>'removed'))`, id, m.now().Unix()).
		Scan(&token.ID, &token.Name, &token.ExpiresAt, &token.ServerID); err != nil {
		return token, fmt.Errorf("read join token status: %w", err)
	}
	return token, nil
}

func (m *Master) Revoke(ctx context.Context, id int64) error {
	if _, err := m.writer.ExecContext(ctx, "DELETE FROM agent_join_tokens WHERE id=? AND server_id IS NULL", id); err != nil {
		return fmt.Errorf("revoke join token: %w", err)
	}
	return nil
}

func ValidMetadata(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxBytes && !strings.ContainsFunc(value, unicode.IsControl)
}

var ErrRegistration = errors.New("invalid agent registration metadata")
var ErrPublicIP = errors.New("节点需要公网 IP；位于 NAT 后请用 --public-ip 指定")

// Special-purpose ranges not already excluded by reality.PublicIP. IPv6 node
// addresses must be in the global unicast allocation and outside special use.
var specialNodeRanges = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var globalNodeIPv6 = netip.MustParsePrefix("2000::/3")

func validNodeIP(ip netip.Addr) bool {
	if ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !reality.PublicIP(ip) || ip.Is6() && !globalNodeIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range specialNodeRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (m *Master) Register(ctx context.Context, in Registration, peer string) (Identity, error) {
	if decodeBytes(in.Token, 32) == nil || decodeBytes(in.PublicKey, ed25519.PublicKeySize) == nil {
		return Identity{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(in.Token))
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("begin agent registration: %w", err)
	}
	defer tx.Rollback()
	now := m.now().Unix()
	var tokenID int64
	var name string
	err = tx.QueryRowContext(ctx, `SELECT id,name FROM agent_join_tokens WHERE token_hash=? AND expires_at>? AND server_id IS NULL`,
		hash[:], now).Scan(&tokenID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, fmt.Errorf("find usable join token: %w", err)
	}
	if !ValidMetadata(in.Hostname, 255) || !ValidMetadata(in.Version, 128) {
		return Identity{}, ErrRegistration
	}
	address := in.PublicIP
	if address == "" {
		address = peer
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || !validNodeIP(ip) {
		return Identity{}, ErrPublicIP
	}
	var result Identity
	if err := tx.QueryRowContext(ctx, `INSERT INTO servers (name, public_host, version, agent_public_key, created_at, updated_at)
		SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM servers WHERE agent_public_key=?)
		RETURNING id`, name, ip.Unmap().String(), in.Version, in.PublicKey, now, now, in.PublicKey).Scan(&result.ServerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Identity{}, ErrUnauthorized
		}
		return Identity{}, fmt.Errorf("create registered server: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE agent_join_tokens SET server_id=? WHERE id=?", result.ServerID, tokenID); err != nil {
		return Identity{}, fmt.Errorf("consume join token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, fmt.Errorf("commit agent registration: %w", err)
	}
	return result, nil
}

type authenticated struct {
	id        int64
	publicKey string
}

func (m *Master) Authenticate(ctx context.Context, method, path string, header http.Header, body []byte) (authenticated, error) {
	for _, key := range []string{HeaderServer, HeaderTimestamp, HeaderNonce, HeaderSignature} {
		if len(header.Values(key)) != 1 {
			return authenticated{}, ErrUnauthorized
		}
	}
	id, idErr := strconv.ParseInt(header.Get(HeaderServer), 10, 64)
	timestamp := header.Get(HeaderTimestamp)
	sent, timeErr := strconv.ParseInt(timestamp, 10, 64)
	nonce := header.Get(HeaderNonce)
	signature := decodeBytes(header.Get(HeaderSignature), ed25519.SignatureSize)
	now := m.now()
	if idErr != nil || id <= 0 || timeErr != nil || sent < now.Unix()-maxClockSkew || sent > now.Unix()+maxClockSkew ||
		decodeBytes(nonce, 16) == nil || signature == nil {
		return authenticated{}, ErrUnauthorized
	}
	var identity authenticated
	err := m.reader.QueryRowContext(ctx, "SELECT id,agent_public_key FROM servers WHERE id=? AND is_local=0 AND status<>'removed' AND agent_public_key IS NOT NULL", id).
		Scan(&identity.id, &identity.publicKey)
	if errors.Is(err, sql.ErrNoRows) {
		return authenticated{}, ErrUnauthorized
	}
	if err != nil {
		return authenticated{}, fmt.Errorf("read agent public key: %w", err)
	}
	key := decodeBytes(identity.publicKey, ed25519.PublicKeySize)
	if key == nil || !ed25519.Verify(key, message(method, path, timestamp, nonce, body), signature) {
		return authenticated{}, ErrUnauthorized
	}
	if !m.acceptNonce(nonceKey{id, nonce}, sent) {
		return authenticated{}, ErrUnauthorized
	}
	return identity, nil
}

func (m *Master) acceptNonce(key nonceKey, sent int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if sent < now.Unix()-maxClockSkew || sent > now.Unix()+maxClockSkew {
		return false
	}
	for entry := m.nonceOrder.Front(); entry != nil; entry = m.nonceOrder.Front() {
		item := entry.Value.(nonceEntry)
		if now.Before(item.expires) {
			break
		}
		delete(m.nonces, item.key)
		m.nonceOrder.Remove(entry)
	}
	// Keep the full replay window even at capacity. Only authenticated
	// requests can occupy slots, and replay attempts never extend a slot.
	if _, exists := m.nonces[key]; exists || len(m.nonces) >= 65536 {
		return false
	}
	m.nonces[key] = struct{}{}
	m.nonceOrder.PushBack(nonceEntry{key, now.Add(nonceLifetime)})
	return true
}

func (m *Master) RecordHeartbeat(ctx context.Context, identity authenticated, in Heartbeat) (HeartbeatReply, error) {
	var reply HeartbeatReply
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return reply, fmt.Errorf("begin agent heartbeat: %w", err)
	}
	defer tx.Rollback()
	now := m.now().Unix()
	reply.DesiredGeneration, err = m.refreshDesired(ctx, tx, identity, now)
	if err != nil {
		return reply, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE servers SET status='online',last_seen_at=?,updated_at=?,version=?,singbox_active=?,uptime=?,
		applied_generation=coalesce(?,applied_generation),sync_error=CASE WHEN ? IS NULL THEN sync_error ELSE ? END
		WHERE id=?`, now, now, in.Version, in.SingboxActive, in.Uptime, in.AppliedGeneration, in.AppliedGeneration, in.SyncError, identity.id)
	if err != nil {
		return reply, fmt.Errorf("record agent heartbeat: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return reply, fmt.Errorf("commit agent heartbeat: %w", err)
	}
	return reply, nil
}

func (m *Master) Remove(ctx context.Context, id int64) error {
	result, err := m.writer.ExecContext(ctx, `UPDATE servers SET status='removed',agent_public_key=NULL,updated_at=?
		WHERE id=? AND is_local=0 AND status<>'removed'`, m.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("remove server identity: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check server removal: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (m *Master) MarkOffline(ctx context.Context) error {
	now := m.now()
	_, err := m.writer.ExecContext(ctx, `UPDATE servers SET status='offline',updated_at=?
		WHERE is_local=0 AND status='online' AND last_seen_at<?`, now.Unix(), now.Add(-OfflineAfter).Unix())
	if err != nil {
		return fmt.Errorf("mark inactive agents offline: %w", err)
	}
	return nil
}

func (m *Master) Run(ctx context.Context, logger *slog.Logger) {
	tick := time.NewTicker(HeartbeatInterval)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := m.MarkOffline(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("无法更新节点离线状态，请检查主控数据库权限。")
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}
