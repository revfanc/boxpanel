package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestPasswordHash(t *testing.T) {
	const password = "test-password-123"
	a, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, hashPrefix) || strings.Contains(a, password) {
		t.Fatal("hash must use random salt and PHC argon2id format")
	}
	for _, candidate := range []string{password, "wrong", ""} {
		match, err := VerifyPassword(a, candidate)
		if err != nil || match != (candidate == password) {
			t.Fatalf("password verification: match=%v err=%v", match, err)
		}
	}
	for _, malformed := range []string{"", "$argon2i$v=19$m=65536,t=3,p=4$AA$AA", strings.Replace(a, "65536", "4294967295", 1), a + "$extra", hashPrefix + "bad$bad", strings.Replace(a, "v=19", "v=16", 1)} {
		if match, err := VerifyPassword(malformed, password); err == nil || match {
			t.Fatal("accepted malformed hash")
		}
	}
	for _, invalid := range []string{"short", strings.Repeat("x", 1025), string([]byte{0xff})} {
		if _, err := HashPassword(invalid); err == nil {
			t.Fatal("accepted invalid password")
		}
	}
	if !ValidPassword("一二三四五六七八九十") {
		t.Fatal("password minimum counts characters")
	}
}

func newTestService(t *testing.T, now func() time.Time) (*Service, *sql.DB) {
	t.Helper()
	conn, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hash, err := HashPassword("original-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO admins (username,password_hash,created_at) VALUES ('admin',?,0)", hash); err != nil {
		t.Fatal(err)
	}
	service, err := New(conn, now)
	if err != nil {
		t.Fatal(err)
	}
	return service, conn
}

func TestSessionLifecycleAndCredentialRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	s, conn := newTestService(t, func() time.Time { return now })
	login := func(previous string) Session {
		t.Helper()
		session, err := s.Login(ctx, "127.0.0.1", "admin", "original-password", previous)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	first := login("")
	decoded, err := base64.RawURLEncoding.DecodeString(first.Token)
	if err != nil || len(decoded) != 32 || !first.ExpiresAt.Equal(now.Add(SessionLifetime)) {
		t.Fatal("invalid session token or expiration")
	}
	second := login(first.Token)
	if first.Token == second.Token {
		t.Fatal("session fixation")
	}
	if _, err := s.Authenticate(ctx, first.Token); !errors.Is(err, ErrSession) {
		t.Fatal("old cookie still accepted")
	}
	// A new service sees the same persisted sessions after a server restart.
	restarted, err := New(conn, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if admin, err := restarted.Authenticate(ctx, second.Token); err != nil || admin.Username != "admin" {
		t.Fatalf("persisted session: %v", err)
	}
	now = second.ExpiresAt
	if _, err := s.Authenticate(ctx, second.Token); !errors.Is(err, ErrSession) {
		t.Fatal("accepted session at exact expiry")
	}
	third := login("")
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired session cleanup: count=%d err=%v", count, err)
	}
	if err := s.Logout(ctx, third.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, third.Token); !errors.Is(err, ErrSession) {
		t.Fatal("logout did not revoke")
	}
	fourth, fifth := login(""), login("")
	if err := s.ChangeCredentials(ctx, fourth.Token, "", "wrong", "replacement-password"); !errors.Is(err, ErrCredentials) {
		t.Fatal("wrong old password accepted")
	}
	if err := s.ChangeCredentials(ctx, fourth.Token, "", "original-password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{fourth.Token, fifth.Token} {
		if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrSession) {
			t.Fatal("password update did not revoke all sessions")
		}
	}
	if err := s.ChangeCredentials(ctx, fifth.Token, "attacker", "", ""); !errors.Is(err, ErrSession) {
		t.Fatal("revoked session changed credentials")
	}
	if _, err := s.Login(ctx, "127.0.0.1", "admin", "original-password", ""); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password accepted")
	}
	updated, err := s.Login(ctx, "127.0.0.1", "admin", "replacement-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeCredentials(ctx, updated.Token, "renamed", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, updated.Token); !errors.Is(err, ErrSession) {
		t.Fatal("username change did not revoke")
	}
	if _, err := s.Login(ctx, "127.0.0.1", "renamed", "replacement-password", ""); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	now := time.Unix(1700000000, 0)
	s, _ := newTestService(t, func() time.Time { return now })
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		username := "admin"
		if i%2 == 0 {
			username = "missing"
		}
		_, err := s.Login(ctx, "192.0.2.1", username, "incorrect", "")
		if i < 5 && !errors.Is(err, ErrCredentials) {
			t.Fatalf("failure %d: %v", i, err)
		}
		if i == 5 {
			var rate *RateLimitError
			if !errors.As(err, &rate) || rate.RetryAfter != 15*time.Minute {
				t.Fatalf("fifth failure: %v", err)
			}
		}
	}
	if _, err := s.Login(ctx, "192.0.2.1", "admin", "original-password", ""); err == nil {
		t.Fatal("correct credentials bypassed lock")
	}
	if _, err := s.Login(ctx, "192.0.2.2", "admin", "original-password", ""); err != nil {
		t.Fatal("other IP blocked", err)
	}
	now = now.Add(15 * time.Minute)
	if _, err := s.Login(ctx, "192.0.2.1", "admin", "original-password", ""); err != nil {
		t.Fatal("lock did not expire", err)
	}
	s.hashing <- struct{}{}
	if _, err := s.Login(ctx, "192.0.2.1", "admin", "original-password", ""); !errors.Is(err, ErrBusy) {
		t.Fatal("hash concurrency not bounded")
	}
	<-s.hashing
}

func TestLimiterExpirySuccessAndConcurrency(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := NewLimiter(func() time.Time { return now })
	l.Failure("a")
	l.Failure("a")
	l.Success("a")
	for range 4 {
		if l.Failure("a") != 0 {
			t.Fatal("success did not reset counter")
		}
	}
	now = now.Add(15 * time.Minute)
	if l.Failure("a") != 0 {
		t.Fatal("inactive failures did not expire")
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); l.Failure("b") }()
	}
	wg.Wait()
	if l.RetryAfter("b") != 15*time.Minute || l.RetryAfter("c") != 0 {
		t.Fatal("concurrent failures not counted by IP")
	}
	now = now.Add(15 * time.Minute)
	if l.RetryAfter("b") != 0 || len(l.entries) != 0 {
		t.Fatal("expired entries not collected")
	}
}
