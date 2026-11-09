package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const SessionLifetime = 7 * 24 * time.Hour

var (
	ErrCredentials = errors.New("invalid credentials")
	ErrSession     = errors.New("invalid or expired session")
	ErrUsername    = errors.New("username already exists")
	ErrBusy        = errors.New("password verification busy")
)

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "too many failed login attempts" }

type Admin struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
}

type Session struct {
	Admin
	Token     string
	ExpiresAt time.Time
}

type Service struct {
	db        *sql.DB
	now       func() time.Time
	limiter   *Limiter
	dummyHash string
	// Serialize expensive hashes, bounding memory and preventing concurrent
	// attempts from slipping past the per-IP fifth-failure lock.
	hashing chan struct{}
}

func New(conn *sql.DB, now func() time.Time) (*Service, error) {
	if now == nil {
		now = time.Now
	}
	secret, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(secret)
	if err != nil {
		return nil, err
	}
	return &Service{db: conn, now: now, limiter: NewLimiter(now), dummyHash: hash, hashing: make(chan struct{}, 1)}, nil
}

func (s *Service) Login(ctx context.Context, ip, username, password, previousToken string) (Session, error) {
	if retry := s.limiter.RetryAfter(ip); retry > 0 {
		return Session{}, &RateLimitError{retry}
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		return Session{}, ErrBusy
	}
	// Recheck after acquiring the hash slot in case another attempt just failed.
	if retry := s.limiter.RetryAfter(ip); retry > 0 {
		return Session{}, &RateLimitError{retry}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin login: %w", err)
	}
	defer tx.Rollback()
	var admin Admin
	hash := ""
	err = tx.QueryRowContext(ctx, "SELECT id, username, password_hash FROM admins WHERE username = ?", username).Scan(&admin.ID, &admin.Username, &hash)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return Session{}, fmt.Errorf("read login administrator: %w", err)
	}
	if missing {
		hash = s.dummyHash
	}
	match, err := VerifyPassword(hash, password)
	if err != nil {
		return Session{}, fmt.Errorf("verify login password: %w", err)
	}
	if !match || missing {
		if retry := s.limiter.Failure(ip); retry > 0 {
			return Session{}, &RateLimitError{retry}
		}
		return Session{}, ErrCredentials
	}
	token, err := RandomToken(32)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	expires := now.Add(SessionLifetime)
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ? OR (id = ? AND admin_id = ?)", now.Unix(), previousToken, admin.ID); err != nil {
		return Session{}, fmt.Errorf("clean login sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (id, admin_id, expires_at, created_at) VALUES (?, ?, ?, ?)", token, admin.ID, expires.Unix(), now.Unix()); err != nil {
		return Session{}, fmt.Errorf("create login session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit login: %w", err)
	}
	s.limiter.Success(ip)
	return Session{Admin: admin, Token: token, ExpiresAt: expires}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Admin, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return Admin{}, ErrSession
	}
	var admin Admin
	err = s.db.QueryRowContext(ctx, `SELECT a.id, a.username FROM sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.id = ? AND s.expires_at > ?`, token, s.now().Unix()).Scan(&admin.ID, &admin.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, ErrSession
	}
	if err != nil {
		return Admin{}, fmt.Errorf("authenticate session: %w", err)
	}
	return admin, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ChangeCredentials rechecks the session inside the write transaction, so a
// concurrent logout/reset cannot authorize a later credential change.
func (s *Service) ChangeCredentials(ctx context.Context, token, username, oldPassword, newPassword string) error {
	if username != "" && !ValidUsername(username) || username == "" && !ValidPassword(newPassword) {
		return fmt.Errorf("change credentials: invalid username or password")
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		return ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential change: %w", err)
	}
	defer tx.Rollback()
	var id int64
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT a.id, a.password_hash FROM admins a JOIN sessions s ON s.admin_id = a.id
		WHERE s.id = ? AND s.expires_at > ?`, token, s.now().Unix()).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSession
	}
	if err != nil {
		return fmt.Errorf("read credential change administrator: %w", err)
	}
	if username != "" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admins WHERE username = ? AND id != ?", username, id).Scan(&count); err != nil {
			return fmt.Errorf("check username availability: %w", err)
		}
		if count > 0 {
			return ErrUsername
		}
		_, err = tx.ExecContext(ctx, "UPDATE admins SET username = ? WHERE id = ?", username, id)
	} else {
		match, verifyErr := VerifyPassword(hash, oldPassword)
		if verifyErr != nil {
			return fmt.Errorf("verify old password: %w", verifyErr)
		}
		if !match {
			return ErrCredentials
		}
		hash, err = HashPassword(newPassword)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE admins SET password_hash = ? WHERE id = ?", hash, id)
	}
	if err != nil {
		return fmt.Errorf("update administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id = ?", id); err != nil {
		return fmt.Errorf("revoke administrator sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential change: %w", err)
	}
	return nil
}
