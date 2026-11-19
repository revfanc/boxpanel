package singbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/host"
)

var ErrUnsupported = errors.New("sing-box management requires Linux without --dev")
var ErrApplying = errors.New("configuration apply is in progress; refresh shortly")

// Apply uses at most 12s for work and 12s for recovery, below HTTP write timeout.
const ApplyTimeout = 12 * time.Second
const RecoveryTimeout = 12 * time.Second

var ErrNoConfig = errors.New("no applied sing-box configuration")

type ApplyFiles interface {
	host.Files
	Rename(string, string) error
}
type FirewallSync interface {
	Sync(context.Context, firewall.Plan, firewall.Plan) error
}

type ApplyOptions struct {
	ReadDB *sql.DB // independent WAL reader for diagnostics
	// ApplyTimeout and RecoveryTimeout can only shorten the production budgets.
	ApplyTimeout, RecoveryTimeout time.Duration
	OS                            string
	Dev                           bool
	// DevDBOnly is an explicit development substitute for T3 resource writes.
	// It requires Dev, accepts non-nil mutations only, and never applies config.
	DevDBOnly      bool
	Binary, Config string
	Runner         host.Runner
	Controller     Controller
	Firewall       FirewallSync
	Files          ApplyFiles
	Now            func() time.Time
	// HealthTimeout may shorten tests; production never waits more than 5s.
	HealthTimeout time.Duration
}
type Manager struct {
	db                            *sql.DB
	readDB                        *sql.DB
	applyTimeout, recoveryTimeout time.Duration
	binary, config                string
	runner                        host.Runner
	controller                    Controller
	firewall                      FirewallSync
	files                         ApplyFiles
	now                           func() time.Time
	healthTimeout                 time.Duration
	unsupported                   bool
	devDBOnly                     bool
	mu                            sync.Mutex
}

type ApplyError struct {
	Code              string
	Detail            string // already redacted, never raw command output
	RollbackFailed    bool
	DatabaseUncertain bool
	cause             error
}

func (e *ApplyError) Error() string {
	if e.DatabaseUncertain {
		return "apply config: database outcome is unknown; keep the backup and recovery journal, repair database access and retry boxpanel apply"
	}
	if e.RollbackFailed {
		return "apply config: recovery failed; database changes were not committed; inspect service, backup and firewall, then retry boxpanel apply"
	}
	return "apply config: " + e.Code + "; changes were not committed; inspect the redacted diagnostic and retry boxpanel apply"
}
func (e *ApplyError) Unwrap() error { return e.cause }

func NewManager(conn *sql.DB, opts ApplyOptions) (*Manager, error) {
	if opts.DevDBOnly && !opts.Dev {
		return nil, fmt.Errorf("database-only apply is development-only; enable --dev")
	}
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Binary == "" {
		opts.Binary = DefaultBinary
	}
	if opts.Config == "" {
		return nil, fmt.Errorf("apply config: provide a configuration path")
	}
	config, err := filepath.Abs(opts.Config)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if opts.Runner == nil {
		opts.Runner = host.Native{}
	}
	if opts.Files == nil {
		opts.Files = nativeApplyFiles{}
	}
	if opts.Controller == nil {
		opts.Controller = NewSystemd(SystemdOptions{OS: opts.OS, Dev: opts.Dev, Runner: opts.Runner})
	}
	if opts.Firewall == nil {
		opts.Firewall = firewall.New(firewall.Options{OS: opts.OS, Dev: opts.Dev, Runner: opts.Runner})
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HealthTimeout <= 0 || opts.HealthTimeout > 5*time.Second {
		opts.HealthTimeout = 5 * time.Second
	}
	if opts.ReadDB == nil {
		opts.ReadDB = conn
	}
	if opts.ApplyTimeout <= 0 || opts.ApplyTimeout > ApplyTimeout {
		opts.ApplyTimeout = ApplyTimeout
	}
	if opts.RecoveryTimeout <= 0 || opts.RecoveryTimeout > RecoveryTimeout {
		opts.RecoveryTimeout = RecoveryTimeout
	}
	return &Manager{db: conn, readDB: opts.ReadDB, applyTimeout: opts.ApplyTimeout, recoveryTimeout: opts.RecoveryTimeout, binary: opts.Binary, config: config, runner: opts.Runner, controller: opts.Controller, firewall: opts.Firewall, files: opts.Files, now: opts.Now, healthTimeout: opts.HealthTimeout, unsupported: opts.Dev || opts.OS != "linux", devDBOnly: opts.DevDBOnly}, nil
}

// Mutation must use only tx for DB access. Do not commit, rollback, or perform
// external side effects in it. Every config-affecting write must enter here.
type Mutation func(context.Context, *sql.Tx) error

func (m *Manager) Restart(ctx context.Context) error { return m.Apply(ctx, nil) }

// Startup is deliberately the same path as CLI/API apply. Fresh installations
// without a binary and configuration can still start the panel for setup.
func (m *Manager) Startup(ctx context.Context) error {
	if m.unsupported {
		return nil
	}
	if _, err := os.Stat(m.binary); errors.Is(err, fs.ErrNotExist) {
		for _, suffix := range []string{"", ".pending"} {
			if _, e := m.files.ReadFile(m.config + suffix); !errors.Is(e, fs.ErrNotExist) {
				return fmt.Errorf("managed config exists but binary is missing; restore sing-box before starting the panel")
			}
		}
		var count int
		if err := m.db.QueryRowContext(ctx, "SELECT count(*) FROM inbounds WHERE enabled=1").Scan(&count); err != nil {
			return fmt.Errorf("inspect startup inbounds: %w", err)
		}
		if count == 0 {
			return nil
		}
		return fmt.Errorf("enabled inbounds require sing-box; run boxpanel singbox install and retry")
	}
	return m.Apply(ctx, nil)
}

type pendingApply struct {
	ID        string        `json:"id"`
	HadConfig bool          `json:"had_config"`
	Previous  firewall.Plan `json:"previous"`
	Next      firewall.Plan `json:"next"`
}

func (m *Manager) Apply(ctx context.Context, mutate Mutation) (result error) {
	// Queueing, flock and work share one deadline; recovery retains an independent
	// bounded deadline even when the request is cancelled. Never detach a write.
	recoveryBase, stopRecovery := context.WithTimeout(context.WithoutCancel(ctx), m.applyTimeout+m.recoveryTimeout)
	defer stopRecovery()
	ctx, cancelWork := context.WithTimeout(ctx, m.applyTimeout)
	defer cancelWork()
	defer func() {
		if result != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			var e *ApplyError
			if errors.As(result, &e) {
				e.Code = "singbox_apply_timeout"
			} else {
				result = &ApplyError{Code: "singbox_apply_timeout", cause: result}
			}
		}
	}()
	if m.devDBOnly && mutate != nil {
		return m.applyDevelopmentDBOnly(ctx, mutate)
	}
	if m.unsupported {
		return ErrUnsupported
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	unlock, err := lockApply(ctx, m.config+".lock")
	if err != nil {
		return fmt.Errorf("lock config apply: %w", err)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep the SQLite write lock through recovery even if the HTTP caller leaves.
	// Cancellation must not make database/sql automatically release it early.
	txctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	connection, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve config connection: %w", err)
	}
	defer connection.Close()
	tx, err := connection.BeginTx(txctx, nil)
	if err != nil {
		return fmt.Errorf("begin config transaction: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			discardConnection(connection)
			result = &ApplyError{Code: "singbox_rollback_failed", RollbackFailed: true, DatabaseUncertain: true, cause: errors.Join(result, e)}
		}
	}()
	if err := m.recoverPending(recoveryBase, tx); err != nil {
		return err
	}
	_, oldSecrets, err := snapshot(ctx, tx)
	if err != nil {
		return err
	}
	previous, err := storedPlan(ctx, tx)
	if err != nil {
		return err
	}
	if mutate != nil {
		if err := mutate(ctx, tx); err != nil {
			return fmt.Errorf("mutate config transaction: %w", err)
		}
	}
	current, secrets, err := snapshot(ctx, tx)
	if err != nil {
		return err
	}
	secrets = append(secrets, oldSecrets...)
	desired, err := planFor(current)
	if err != nil {
		return &ApplyError{Code: "singbox_check_failed", cause: err}
	}
	opts, err := m.runtimeOptions(ctx, tx, current)
	if err != nil {
		return &ApplyError{Code: "singbox_check_failed", cause: err}
	}
	if opts.UserRoutingMarks && len(desired.Ports) > 0 {
		for _, u := range current.Users {
			if confgen.UserEligible(u, opts.Now.Unix()) {
				desired.TrafficUsers = append(desired.TrafficUsers, u.ID)
			}
		}
	}
	secrets = append(secrets, opts.ClashAPISecret)
	config, err := confgen.Build(current, opts)
	if err != nil {
		return &ApplyError{Code: "singbox_check_failed", cause: err}
	}
	oldConfig, err := m.files.ReadFile(m.config)
	hadConfig := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read previous config: %w", err)
	}
	secrets = append(secrets, jsonStrings(oldConfig)...)
	if backup, e := m.files.ReadFile(m.config + ".bak"); e == nil {
		secrets = append(secrets, jsonStrings(backup)...)
	}
	// A data-only change (traffic limit, expiry, remark, traffic reset…) that
	// leaves the generated config and firewall plan identical must not restart
	// the core: a restart drops every other user's connections. Only commit the
	// DB change when the running core is already active with this exact config.
	// A nil mutation is an explicit restart request and always restarts.
	if mutate != nil && hadConfig && bytes.Equal(config, oldConfig) && reflect.DeepEqual(previous, desired) && m.CoreActive(ctx) {
		if err := m.commitUnchanged(ctx, recoveryBase, connection, tx, current, opts, desired); err != nil {
			return err
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if e := m.files.Remove(m.config + ".tmp"); e != nil && !errors.Is(e, fs.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove temporary config: %w", e))
		}
	}()
	if err := m.files.WriteFile(m.config+".tmp", config, 0600); err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	output, err := m.runner.Run(ctx, m.binary, "check", "-c", m.config+".tmp")
	if err != nil {
		return &ApplyError{Code: "singbox_check_failed", Detail: redactText(string(output), secrets), cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	pending := pendingApply{ID: id, HadConfig: hadConfig, Previous: previous, Next: desired}
	if hadConfig {
		if err := m.files.WriteFile(m.config+".bak", oldConfig, 0600); err != nil {
			return fmt.Errorf("backup config: %w", err)
		}
	}
	journal, _ := json.Marshal(pending)
	if err := m.files.WriteFile(m.config+".pending", journal, 0600); err != nil {
		return fmt.Errorf("record pending apply: %w", err)
	}
	// From this point every error must restore both external state and DB.
	fail := func(code string, cause error) error {
		recoveryCtx, stop := context.WithTimeout(recoveryBase, m.recoveryTimeout)
		defer stop()
		recovery := m.restore(recoveryCtx, pending)
		diagnosticCtx, stopDiagnostic := context.WithTimeout(recoveryCtx, time.Second)
		lines, _ := m.journal(diagnosticCtx, 30)
		stopDiagnostic()
		if recovery == nil {
			recovery = m.removePending()
		}
		return &ApplyError{Code: code, Detail: redactText(lines, secrets), RollbackFailed: recovery != nil, cause: errors.Join(cause, recovery)}
	}
	if err := m.files.Rename(m.config+".tmp", m.config); err != nil {
		return fail("singbox_apply_failed", err)
	}
	if err := m.restartHealthy(ctx); err != nil {
		return fail("singbox_restart_failed", err)
	}
	if err := m.firewall.Sync(ctx, previous, desired); err != nil {
		return fail("singbox_firewall_failed", err)
	}
	if err := m.recordApplied(ctx, tx, current, opts, desired); err != nil {
		return fail("singbox_apply_failed", err)
	}
	if err := db.SetSetting(ctx, tx, db.ApplyGeneration, id); err != nil {
		return fail("singbox_apply_failed", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("singbox_apply_failed", err)
	}
	if err := tx.Commit(); err != nil {
		// database/sql marks Tx done even if SQLite rejects COMMIT (e.g. a
		// deferred foreign key). Reserve this connection so no other request can
		// receive its still-open transaction; close it to force rollback.
		discardConnection(connection)
		recoveryCtx, stop := context.WithTimeout(recoveryBase, min(5*time.Second, m.recoveryTimeout))
		generation, readErr := db.GetSetting(recoveryCtx, m.db, db.ApplyGeneration)
		stop()
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			// Commit outcome is unknown: keep the journal for startup to resolve;
			// do not overwrite possibly committed state with an old backup.
			return &ApplyError{Code: "singbox_apply_failed", RollbackFailed: true, DatabaseUncertain: true, cause: errors.Join(err, readErr)}
		}
		if generation != id {
			return fail("singbox_apply_failed", err)
		}
		// If the committed generation is visible on a fresh connection, all
		// external state already matches it and the operation did commit.
	}
	// A committed generation makes this journal harmless if deletion fails.
	// The next apply removes it without restoring a committed configuration.
	_ = m.removePending()
	return nil
}

// CoreActive reports whether the core unit is currently active.
func (m *Manager) CoreActive(ctx context.Context) bool {
	output, err := m.runner.Run(ctx, "systemctl", "is-active", "--", SingboxUnit)
	return err == nil && ctx.Err() == nil && stringTrim(output) == "active"
}

// commitUnchanged records the same bookkeeping as a full apply (applied users,
// generation) without touching files, the core or the firewall.
func (m *Manager) commitUnchanged(ctx, recoveryBase context.Context, connection *sql.Conn, tx *sql.Tx, current confgen.Snapshot, opts confgen.Options, desired firewall.Plan) error {
	if err := m.recordApplied(ctx, tx, current, opts, desired); err != nil {
		return &ApplyError{Code: "singbox_apply_failed", cause: fmt.Errorf("record unchanged config: %w", err)}
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	if err := db.SetSetting(ctx, tx, db.ApplyGeneration, id); err != nil {
		return &ApplyError{Code: "singbox_apply_failed", cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		// Just as for a full apply, SQLite can reject COMMIT while leaving the
		// driver transaction open. Do not return that connection to the pool.
		discardConnection(connection)
		recoveryCtx, stop := context.WithTimeout(recoveryBase, min(5*time.Second, m.recoveryTimeout))
		defer stop()
		generation, readErr := db.GetSetting(recoveryCtx, m.db, db.ApplyGeneration)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return &ApplyError{Code: "singbox_apply_failed", RollbackFailed: true, DatabaseUncertain: true, cause: errors.Join(err, readErr)}
		}
		if generation != id {
			return &ApplyError{Code: "singbox_apply_failed", cause: fmt.Errorf("commit unchanged config: %w", err)}
		}
	}
	return nil
}

func (m *Manager) recordApplied(ctx context.Context, tx *sql.Tx, current confgen.Snapshot, opts confgen.Options, desired firewall.Plan) error {
	raw, _ := json.Marshal(desired)
	if err := db.SetSetting(ctx, tx, db.AppliedFirewall, json.RawMessage(raw)); err != nil {
		return err
	}
	active := []int64{}
	if len(desired.Ports) > 0 {
		for _, u := range current.Users {
			if confgen.UserEligible(u, opts.Now.Unix()) {
				active = append(active, u.ID)
			}
		}
	}
	if err := db.SetSetting(ctx, tx, db.AppliedUsers, active); err != nil {
		return err
	}
	return nil
}

func (m *Manager) restartHealthy(ctx context.Context) error {
	if err := m.controller.Restart(ctx, SingboxUnit); err != nil {
		return fmt.Errorf("restart core: %w", err)
	}
	health, cancel := context.WithTimeout(ctx, m.healthTimeout)
	defer cancel()
	for {
		if m.CoreActive(health) {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-health.Done():
			timer.Stop()
			return fmt.Errorf("core did not become active within five seconds: %w", health.Err())
		case <-timer.C:
		}
	}
}

func (m *Manager) restore(ctx context.Context, p pendingApply) error {
	var fileErr, serviceErr error
	if p.HadConfig {
		backup, err := m.files.ReadFile(m.config + ".bak")
		if err == nil {
			err = m.files.WriteFile(m.config, backup, 0600)
		}
		fileErr = err
		if err == nil {
			serviceErr = m.restartHealthy(ctx)
		} else {
			serviceErr = m.controller.Stop(ctx, SingboxUnit)
		}
	} else {
		fileErr = m.files.Remove(m.config)
		if errors.Is(fileErr, fs.ErrNotExist) {
			fileErr = nil
		}
		serviceErr = m.controller.Stop(ctx, SingboxUnit)
	}
	firewallErr := m.firewall.Sync(ctx, unionPlan(p.Previous, p.Next), p.Previous)
	return errors.Join(fileErr, serviceErr, firewallErr)
}
func (m *Manager) recoverPending(ctx context.Context, tx *sql.Tx) error {
	raw, err := m.files.ReadFile(m.config + ".pending")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read apply recovery journal: %w", err)
	}
	var p pendingApply
	if json.Unmarshal(raw, &p) != nil || p.ID == "" || p.Previous.Validate() != nil || p.Next.Validate() != nil {
		return fmt.Errorf("invalid recovery journal; restore the configuration backup and firewall before retrying")
	}
	generation, err := db.GetSetting(ctx, tx, db.ApplyGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if generation != p.ID {
		recovery, cancel := context.WithTimeout(ctx, m.recoveryTimeout)
		defer cancel()
		if err := m.restore(recovery, p); err != nil {
			return &ApplyError{Code: "singbox_recovery_failed", RollbackFailed: true, cause: err}
		}
	}
	return m.removePending()
}
func (m *Manager) removePending() error {
	err := m.files.Remove(m.config + ".pending")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
func unionPlan(a, b firewall.Plan) firewall.Plan {
	p := firewall.Plan{Ports: slices.Clone(a.Ports), Hops: slices.Clone(a.Hops), TrafficUsers: slices.Clone(a.TrafficUsers)}
	for _, port := range b.Ports {
		if !slices.Contains(p.Ports, port) {
			p.Ports = append(p.Ports, port)
		}
	}
	for _, hop := range b.Hops {
		if !slices.Contains(p.Hops, hop) {
			p.Hops = append(p.Hops, hop)
		}
	}
	for _, id := range b.TrafficUsers {
		if !slices.Contains(p.TrafficUsers, id) {
			p.TrafficUsers = append(p.TrafficUsers, id)
		}
	}
	return p
}
func stringTrim(b []byte) string { return strings.TrimSpace(string(b)) }
func randomID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate apply generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Returning ErrBadConn from Raw discards the reserved driver connection.
// Closing SQLite rolls back any transaction left open by a failed COMMIT.
func discardConnection(connection *sql.Conn) {
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
}

// lock keeps queued requests inside the same apply budget.
func (m *Manager) lock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for config apply: %w", err)
		}
		if m.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for config apply: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
