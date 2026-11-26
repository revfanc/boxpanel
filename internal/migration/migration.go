// Package migration coordinates the durable cutover between two master panels.
package migration

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/filelock"
)

const Prefix = "/api/migration/v1"
const Lifetime = 30 * time.Minute

var (
	ErrToken     = errors.New("迁移令牌无效、已用或过期，请在旧主控生成新命令")
	ErrBusy      = errors.New("主控正在处理其他操作，请稍后重试迁移")
	ErrVersion   = errors.New("新旧主控的 boxpanel 版本须一致，请使用旧主控生成的命令安装")
	ErrAddress   = errors.New("无法确定新主控的不同公网 IP，请使用 --public-ip 指定新机的公网地址")
	ErrReady     = errors.New("新主控的 HTTPS 就绪检查未通过，请检查云安全组、面板端口和证书")
	ErrCommitted = errors.New("迁移已确认，请在新主控继续操作")
)

type Service struct {
	DB, ReadDB       *sql.DB
	DataDir, Version string
	Pin              string
	Signer           crypto.Signer
	StopCore         func(context.Context) error
	BeforeClaim      func(context.Context) error
	Now              func() time.Time
}

type Grant struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	SourceURL string `json:"source_url"`
	MasterPin string `json:"master_pin"`
	Version   string `json:"version"`
}

type Claim struct {
	Token    string `json:"token"`
	Secret   string `json:"secret"`
	PublicIP string `json:"public_ip,omitempty"`
	Version  string `json:"version"`
}

type Ready struct {
	Secret    string `json:"secret"`
	TargetURL string `json:"target_url"`
}

type Status struct {
	Phase       string `json:"phase"`
	TargetURL   string `json:"target_url,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Migrated    int    `json:"migrated"`
	Total       int    `json:"total"`
	CoreStopped bool   `json:"core_stopped"`
	Waiting     []Node `json:"waiting,omitempty"`
}

type Node struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var encoding = base64.RawURLEncoding.Strict()

func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate migration secret: %w", err)
	}
	return encoding.EncodeToString(b[:]), nil
}

func ValidSecret(value string) bool {
	if len(value) != 43 {
		return false
	}
	b, err := encoding.DecodeString(value)
	return err == nil && len(b) == 32
}

func exchangeID(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return encoding.EncodeToString(digest[:])
}

func matches(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// BeginWrite shares a gate with other writers. Claim takes the exclusive gate
// before freezing, so acknowledged changes finish before the snapshot begins.
func (s *Service) BeginWrite(ctx context.Context) (func(), error) {
	return beginWrite(ctx, s.ReadDB, s.DataDir, s.now)
}

// BeginWrite is also used by the administrative CLI with the same data dir.
func BeginWrite(ctx context.Context, q db.SettingsDB, dir string) (func(), error) {
	return beginWrite(ctx, q, dir, time.Now)
}

func beginWrite(ctx context.Context, q db.SettingsDB, dir string, now func() time.Time) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	release, err := filelock.AcquireShared(lockCtx, filepath.Clean(dir)+".migration.lock")
	if err != nil {
		return nil, fmt.Errorf("lock migration writer: %w", err)
	}
	readOnly, err := db.ReadOnlyMigration(ctx, q, now())
	if err != nil || readOnly {
		release()
		if readOnly {
			err = db.ErrMigrationReadOnly
		}
		return nil, err
	}
	return release, nil
}

func (s *Service) Maintenance(ctx context.Context) (func(), bool) {
	release, err := s.BeginWrite(ctx)
	return release, err == nil
}

func (s *Service) exclusive(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return filelock.Acquire(ctx, filepath.Clean(s.DataDir)+".migration.lock")
}

func (s *Service) Issue(ctx context.Context, source string) (Grant, error) {
	source, err := agent.NormalizeMasterURL(source)
	if err != nil {
		return Grant{}, err
	}
	release, err := s.BeginWrite(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer release()
	token, err := NewSecret()
	if err != nil {
		return Grant{}, err
	}
	state := db.MasterMigration{Phase: db.MigrationIssued, TokenHash: exchangeID(token), ExpiresAt: s.now().Add(Lifetime).Unix(), SourceURL: source}
	if err := db.SetSetting(ctx, s.DB, db.MigrationOutgoing, state); err != nil {
		return Grant{}, fmt.Errorf("store migration grant: %w", err)
	}
	return Grant{token, state.ExpiresAt, source, s.Pin, s.Version}, nil
}

func targetAddress(source, address string) (string, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !agent.ValidPublicIP(ip) {
		return "", ErrAddress
	}
	u, err := url.Parse(source)
	if err != nil || ip.Unmap().String() == u.Hostname() {
		return "", ErrAddress
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	u.Host = net.JoinHostPort(ip.Unmap().String(), port)
	return agent.NormalizeMasterURL(u.String())
}

func (s *Service) Claim(ctx context.Context, input Claim, peer string) (db.MasterMigration, error) {
	if !ValidSecret(input.Token) || !ValidSecret(input.Secret) {
		return db.MasterMigration{}, ErrToken
	}
	issued, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationOutgoing)
	if err != nil {
		return db.MasterMigration{}, err
	}
	digest := exchangeID(input.Token)
	if issued.Phase != db.MigrationIssued || issued.Expired(s.now()) || !matches(issued.TokenHash, digest) {
		return db.MasterMigration{}, ErrToken
	}
	release, err := s.exclusive(ctx)
	if err != nil {
		return db.MasterMigration{}, ErrBusy
	}
	defer release()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.MasterMigration{}, fmt.Errorf("begin migration claim: %w", err)
	}
	defer tx.Rollback()
	out, err := db.LoadMasterMigration(ctx, tx, db.MigrationOutgoing)
	if err != nil {
		return out, err
	}
	if out.Phase != db.MigrationIssued || out.Expired(s.now()) || !matches(out.TokenHash, digest) {
		return out, ErrToken
	}
	if input.Version != s.Version {
		return out, ErrVersion
	}
	if s.BeforeClaim != nil {
		if err := s.BeforeClaim(ctx); err != nil {
			return out, err
		}
	}
	if input.PublicIP != "" {
		peer = input.PublicIP
	}
	target, err := targetAddress(out.SourceURL, peer)
	if err != nil {
		return out, err
	}
	out.Phase, out.TokenHash, out.ID, out.TargetURL = db.MigrationFrozen, "", exchangeID(input.Secret), target
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM servers WHERE is_local=0 AND status<>'removed' AND agent_public_key IS NOT NULL`).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count registered nodes: %w", err)
	}
	if err := db.SetSetting(ctx, tx, db.MigrationOutgoing, out); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("freeze source master: %w", err)
	}
	return out, nil
}

func (s *Service) Snapshot(ctx context.Context, state db.MasterMigration) (*os.File, error) {
	f, err := os.CreateTemp("", "boxpanel-migration-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("open private migration snapshot: %w", err)
	}
	// The file stays accessible to this request and is reclaimed if it exits.
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, fmt.Errorf("unlink migration snapshot: %w", err)
	}
	err = backup.WriteSnapshot(ctx, f, s.DB, filepath.Join(s.DataDir, "certs"), s.Version, s.now())
	if err == nil {
		_, err = f.Seek(0, 0)
	}
	if err == nil && state.Expired(s.now()) {
		err = ErrToken
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Service) Abort(ctx context.Context, secret string) error {
	if !ValidSecret(secret) {
		return ErrToken
	}
	_, err := s.updateOutgoing(ctx, exchangeID(secret), func(out *db.MasterMigration) error {
		if out.Phase == db.MigrationRetired {
			return ErrCommitted
		}
		*out = db.MasterMigration{}
		return nil
	})
	return err
}

// Cancel lets the administrator end an unfinished migration on the source,
// for example after the target machine was destroyed or killed before it could
// report failure. The exchange ID is discarded, so a target that is still
// running is refused at ready and rolls itself back. A committed retirement
// cannot be cancelled.
func (s *Service) Cancel(ctx context.Context) error {
	release, err := s.exclusive(ctx)
	if err != nil {
		return ErrBusy
	}
	defer release()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration cancel: %w", err)
	}
	defer tx.Rollback()
	state, err := db.LoadMasterMigration(ctx, tx, db.MigrationOutgoing)
	if err != nil {
		return err
	}
	if state.Phase == db.MigrationRetired {
		return ErrCommitted
	}
	if !state.Pending() {
		return nil
	}
	if err := db.SetSetting(ctx, tx, db.MigrationOutgoing, db.MasterMigration{}); err != nil {
		return err
	}
	return tx.Commit()
}

// clearExpired persists the automatic unfreeze that readers already apply.
func (s *Service) clearExpired(ctx context.Context) {
	release, err := s.exclusive(ctx)
	if err != nil {
		return
	}
	defer release()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	state, err := db.LoadMasterMigration(ctx, tx, db.MigrationOutgoing)
	if err != nil || !state.Expired(s.now()) {
		return
	}
	if db.SetSetting(ctx, tx, db.MigrationOutgoing, db.MasterMigration{}) == nil {
		_ = tx.Commit()
	}
}

func (s *Service) updateOutgoing(ctx context.Context, id string, mutate func(*db.MasterMigration) error) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin source migration update: %w", err)
	}
	defer tx.Rollback()
	state, err := db.LoadMasterMigration(ctx, tx, db.MigrationOutgoing)
	if err != nil || !matches(state.ID, id) {
		return false, err
	}
	if err := mutate(&state); err != nil {
		return false, err
	}
	if err := db.SetSetting(ctx, tx, db.MigrationOutgoing, state); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Service) Ready(ctx context.Context, input Ready) error {
	if !ValidSecret(input.Secret) {
		return ErrToken
	}
	id := exchangeID(input.Secret)
	state, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationOutgoing)
	if err != nil {
		return err
	}
	if !matches(state.ID, id) || state.TargetURL != input.TargetURL || state.Expired(s.now()) {
		return ErrToken
	}
	if state.Phase == db.MigrationRetired {
		return nil
	}
	if state.Phase != db.MigrationFrozen {
		return ErrToken
	}
	status, err := FetchStatus(ctx, state.TargetURL, s.Pin, id)
	if err != nil || status.Phase != db.MigrationIncoming || status.TargetURL != state.TargetURL {
		return ErrReady
	}
	release, err := s.exclusive(ctx)
	if err != nil {
		return ErrBusy
	}
	changed, err := s.updateOutgoing(ctx, id, func(out *db.MasterMigration) error {
		if out.Phase == db.MigrationRetired {
			return nil
		}
		if out.Phase != db.MigrationFrozen || out.Expired(s.now()) {
			return ErrToken
		}
		// Persist the decision first. Restart recovery cannot restart the old
		// inbounds after this commit, even if stopping them is interrupted.
		out.Phase = db.MigrationRetired
		out.ExpiresAt = 0
		return nil
	})
	release()
	if err == nil && !changed {
		return ErrToken
	}
	if err == nil {
		s.stopRetiredCore(ctx, id)
	}
	return err
}

func (s *Service) stopRetiredCore(ctx context.Context, id string) {
	if s.StopCore != nil && s.StopCore(ctx) == nil {
		_, _ = s.updateOutgoing(ctx, id, func(out *db.MasterMigration) error {
			out.CoreStopped = true
			return nil
		})
	}
}

func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	out, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationOutgoing)
	if err != nil {
		return
	}
	if out.Expired(s.now()) {
		s.clearExpired(ctx)
		return
	}
	if out.Phase == db.MigrationRetired {
		if !out.CoreStopped {
			s.stopRetiredCore(ctx, out.ID)
		}
		if remote, err := FetchStatus(ctx, out.TargetURL, s.Pin, out.ID); err == nil && remote.Phase == db.MigrationActive &&
			remote.Migrated >= 0 && remote.Migrated <= remote.Total && remote.Total <= out.Total &&
			(out.Migrated != remote.Migrated || out.Total != remote.Total) {
			_, _ = s.updateOutgoing(ctx, out.ID, func(out *db.MasterMigration) error {
				out.Migrated, out.Total = remote.Migrated, remote.Total
				return nil
			})
		}
		return
	}
	in, err := db.LoadMasterMigration(ctx, s.ReadDB, db.MigrationReceived)
	if err == nil && in.Phase == db.MigrationIncoming {
		if remote, err := FetchStatus(ctx, in.SourceURL, s.Pin, in.ID); err == nil && remote.Phase == db.MigrationRetired {
			_ = Activate(ctx, s.DB, in.ID)
		}
	}
}
