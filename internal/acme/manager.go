package acme

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
)

// Status contains public metadata only. Mode is desired policy; Type is the
// certificate actually served, so a fallback never appears as trusted TLS.
type Status struct {
	Mode        string `json:"mode"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Issuer      string `json:"issuer"`
	NotBefore   int64  `json:"not_before"`
	NotAfter    int64  `json:"not_after"`
	LastAttempt int64  `json:"last_attempt"`
	LastSuccess int64  `json:"last_success"`
	LastError   string `json:"last_error"`
	Hint        string `json:"hint"`
	Failures    int    `json:"failures"`
	NextAttempt int64  `json:"next_attempt"`
	Issuing     bool   `json:"issuing"`
	Warning     bool   `json:"warning"`
}

type savedState struct {
	Status
	File      string `json:"file"`
	Directory string `json:"directory"`
}

type Options struct {
	Dir, CertFile, KeyFile, Directory string
	Dev                               bool
	Now                               func() time.Time
	Issuer                            Issuer
	OpenPort                          func(context.Context) error
	Logger                            *slog.Logger
}

type Manager struct {
	db        *sql.DB
	opts      Options
	work      sync.Mutex
	mu        sync.Mutex
	state     savedState
	cancel    context.CancelFunc
	requested atomic.Bool
	wake      chan struct{}
	current   atomic.Pointer[tls.Certificate]
}

func New(ctx context.Context, conn *sql.DB, opts Options) (*Manager, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Directory == "" {
		opts.Directory = DefaultDirectory
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := ValidateDirectory(opts.Directory); err != nil {
		return nil, err
	}
	if opts.Issuer == nil {
		opts.Issuer = &Client{Dir: opts.Dir, Directory: opts.Directory}
	}
	m := &Manager{db: conn, opts: opts, wake: make(chan struct{}, 1)}
	if opts.Dev {
		m.state.Status = Status{Mode: "http", Type: "http"}
		return m, nil
	}
	if err := os.MkdirAll(opts.Dir, 0700); err != nil {
		return nil, fmt.Errorf("create certificate directory: %w", err)
	}
	state, err := readState(ctx, conn)
	if err != nil {
		return nil, err
	}
	m.state = state
	// No network during construction: HTTPS is available while the worker
	// performs its first issuance. Persisted backoff survives process restarts.
	if err := m.reconcileLocal(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func readState(ctx context.Context, q db.SettingsDB) (savedState, error) {
	var state savedState
	raw, err := db.GetSetting(ctx, q, db.PanelCertificate)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("read certificate state: %w", err)
	}
	return state, nil
}

func StoredStatus(ctx context.Context, q db.SettingsDB) (Status, error) {
	s, err := readState(ctx, q)
	return s.Status, err
}

// ActiveFile is used only by the local readiness CLI. Never expose it via API.
func ActiveFile(ctx context.Context, q db.SettingsDB, dir, fallback string) (string, error) {
	s, err := readState(ctx, q)
	if err != nil {
		return "", err
	}
	if s.File == "" {
		return fallback, nil
	}
	if !validFile(s.File) {
		return "", fmt.Errorf("invalid active certificate filename; restore certificate state")
	}
	return filepath.Join(dir, s.File), nil
}

func validFile(name string) bool {
	if len(name) != len("panel-")+64+len(".pem") || filepath.Base(name) != name || !strings.HasPrefix(name, "panel-") || !strings.HasSuffix(name, ".pem") {
		return false
	}
	for _, c := range name[6:70] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func desired(ctx context.Context, q db.SettingsDB) (string, string, error) {
	name, err := db.GetSetting(ctx, q, db.PublicHost)
	if err != nil {
		return "", "", err
	}
	mode, err := db.GetSetting(ctx, q, db.PanelTLSMode)
	if err != nil {
		return "", "", err
	}
	if !rules.ValidHost(name) || (mode != "acme" && mode != "self_signed") {
		return "", "", fmt.Errorf("invalid panel TLS settings; restore public_host and panel_tls_mode")
	}
	return name, mode, nil
}

func parseMaterial(material Material, name string, now time.Time) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair(material.Certificate, material.Key)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse TLS leaf: %w", err)
	}
	if err := leaf.VerifyHostname(name); err != nil {
		return nil, fmt.Errorf("validate TLS host: %w", err)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("TLS certificate is outside its validity period")
	}
	if leaf.IsCA {
		return nil, fmt.Errorf("TLS leaf must not be a CA")
	}
	allowed := len(leaf.ExtKeyUsage) == 0
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("TLS certificate cannot authenticate a server")
	}
	pair.Leaf = leaf
	return &pair, nil
}

func (m *Manager) reconcileLocal(ctx context.Context) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("read TLS policy: %w", err)
	}
	name, mode, err := desired(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		return err
	}
	m.mu.Lock()
	state := m.state
	m.mu.Unlock()
	if state.Host == name && state.Mode == mode && state.Directory == m.opts.Directory && validFile(state.File) {
		data, err := os.ReadFile(filepath.Join(m.opts.Dir, state.File))
		if err == nil {
			pair, err := parseMaterial(Material{data, data}, name, m.opts.Now())
			if err == nil {
				m.current.Store(pair)
				return nil
			}
		}
	}
	// Changing host/mode invalidates old retry policy and stale certificates.
	if state.Host != name || state.Mode != mode || state.Directory != m.opts.Directory {
		state = savedState{Status: Status{Host: name, Mode: mode}, Directory: m.opts.Directory}
	}
	certificate, certErr := os.ReadFile(m.opts.CertFile)
	key, keyErr := os.ReadFile(m.opts.KeyFile)
	if certErr != nil || keyErr != nil {
		return fmt.Errorf("load original panel certificate; restore --cert-file and --key-file")
	}
	material := Material{certificate, key}
	pair, err := parseMaterial(material, name, m.opts.Now())
	if err != nil {
		material.Certificate, material.Key, err = certs.PanelHost(name, m.opts.Now())
		if err != nil {
			return err
		}
		pair, err = parseMaterial(material, name, m.opts.Now())
		if err != nil {
			return err
		}
	}
	state.Type = "self_signed"
	return m.activate(ctx, state, material, pair)
}

// activate saves one immutable, content-addressed bundle before committing its
// reference. A crash cannot expose a certificate paired with a different key.
// Recheck desired policy under the DB write lock to discard stale issuance.
func (m *Manager) activate(ctx context.Context, state savedState, material Material, pair *tls.Certificate) error {
	data := append(append([]byte{}, material.Certificate...), material.Key...)
	state.File = fmt.Sprintf("panel-%x.pem", sha256.Sum256(data))
	if err := privateFile(filepath.Join(m.opts.Dir, state.File), data); err != nil {
		return err
	}
	state.Issuer = pair.Leaf.Issuer.String()
	state.NotBefore = pair.Leaf.NotBefore.Unix()
	state.NotAfter = pair.Leaf.NotAfter.Unix()
	return m.persist(ctx, state, pair)
}

func (m *Manager) persist(ctx context.Context, state savedState, pair *tls.Certificate) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save certificate state: %w", err)
	}
	defer tx.Rollback()
	name, mode, err := desired(ctx, tx)
	if err != nil {
		return err
	}
	if name != state.Host || mode != state.Mode {
		return fmt.Errorf("certificate policy changed; retry with the current public_host")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode certificate state: %w", err)
	}
	if err = db.SetSetting(ctx, tx, db.PanelCertificate, json.RawMessage(raw)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit certificate state: %w", err)
	}
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	if pair != nil {
		m.current.Store(pair)
	}
	return nil
}

func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	pair := m.current.Load()
	if pair == nil {
		return nil, fmt.Errorf("panel certificate unavailable")
	}
	return pair, nil
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	status := m.state.Status
	status.Issuing = m.cancel != nil || m.requested.Load()
	m.mu.Unlock()
	if m.opts.Dev {
		return status, nil
	}
	mode, err := db.GetSetting(ctx, m.db, db.PanelTLSMode)
	if err != nil {
		return status, err
	}
	status.Mode = mode
	status.Warning = status.Failures >= 2 && status.NotAfter != 0 && time.Unix(status.NotAfter, 0).Sub(m.opts.Now()) < 24*time.Hour
	return status, nil
}

// Changed cancels any in-flight order after settings commit. The worker then
// rereads DB; no network call is ever made while a settings transaction is open.
func (m *Manager) Changed() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) Request(ctx context.Context) error {
	if m.opts.Dev {
		return ErrDisabled
	}
	if !m.work.TryLock() {
		return ErrBusy
	}
	defer m.work.Unlock()
	if m.requested.Load() {
		return ErrBusy
	}
	if err := db.SetSetting(ctx, m.db, db.PanelTLSMode, "acme"); err != nil {
		return err
	}
	m.requested.Store(true)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	if m.opts.Dev {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.Tick(ctx, false); err != nil && ctx.Err() == nil {
			m.opts.Logger.Warn("证书维护未完成，请在设置中查看状态并检查证书目录权限。")
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

// Tick makes renewal scheduling deterministic with an injected clock/issuer.
func (m *Manager) Tick(ctx context.Context, force bool) error {
	m.work.Lock()
	defer m.work.Unlock()
	if m.opts.Dev {
		return nil
	}
	// Consume the request while holding the same lock as Request. Taking it
	// before this lock can leave a queued force behind an automatic issuance,
	// causing a second unnecessary order on the next wake.
	force = m.requested.Swap(false) || force
	if err := m.reconcileLocal(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	state := m.state
	m.mu.Unlock()
	if state.Mode != "acme" {
		return nil
	}
	now := m.opts.Now()
	due := state.Type != "acme" || !now.Before(time.Unix(state.NotBefore, 0).Add(time.Duration(state.NotAfter-state.NotBefore)*time.Second/2))
	if !force && (!due || now.Unix() < state.NextAttempt) {
		return nil
	}
	attempt, cancel := context.WithTimeout(ctx, 2*time.Minute)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); m.cancel = nil; m.mu.Unlock() }()
	state.LastAttempt = now.Unix()
	var material Material
	var err error
	if m.opts.OpenPort != nil {
		err = m.opts.OpenPort(attempt)
	}
	if err == nil {
		material, err = m.opts.Issuer.Issue(attempt, state.Host)
	}
	var pair *tls.Certificate
	if err == nil {
		pair, err = parseMaterial(material, state.Host, m.opts.Now())
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		next := state
		next.Type = "acme"
		next.LastSuccess = m.opts.Now().Unix()
		next.LastError = ""
		next.Hint = ""
		next.Failures = 0
		next.NextAttempt = 0
		err = m.activate(ctx, next, material, pair)
		if err == nil {
			return nil
		}
	}
	if err != nil {
		state.Failures++
		if state.Failures > 16 {
			state.Failures = 16
		}
		delay := 5 * time.Minute * time.Duration(1<<uint(state.Failures-1))
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
		state.NextAttempt = m.opts.Now().Add(delay).Unix()
		state.LastError, state.Hint = Failure(err)
		m.opts.Logger.Warn("可信证书申请失败，保留当前 HTTPS 证书。", "code", state.LastError, "hint", state.Hint)
		return m.persist(ctx, state, nil)
	}
	return nil
}

// CA/command errors can contain challenge tokens. Only fixed codes and hints
// cross the logging, database and API boundaries.
func Failure(err error) (string, string) {
	switch {
	case errors.Is(err, ErrPort):
		return "acme_port_unavailable", "TCP 80 被占用或无法绑定。请停止占用 80 的服务、检查绑定权限，再重新申请；仅签发和续期期间需要监听 80。"
	case errors.Is(err, ErrHost):
		return "acme_invalid_host", "请将节点地址设为本机公网 IP 或解析到本机的域名；私网、回环和保留地址不能申请可信证书。"
	default:
		return "acme_issue_failed", "请放行本机防火墙和云安全组 TCP 80，确认域名 A/AAAA 均指向本机且关闭 CDN，并检查出站 HTTPS、系统时间和证书目录权限；CA 限流时请等待自动重试。"
	}
}
