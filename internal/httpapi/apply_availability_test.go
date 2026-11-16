package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/subscription"
)

type pausedApplyRunner struct {
	*resourceRunner
	phase   string
	entered chan struct{}
	release chan struct{}
}

func (p *pausedApplyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == p.phase {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.resourceRunner.Run(ctx, name, args...)
}

func TestApplyReadAvailability(t *testing.T) {
	for _, phase := range []string{"restart", "is-active"} {
		t.Run(phase, func(t *testing.T) {
			f, store, _, _ := newSubscriptionAPI(t)
			f.call(t, "POST", "users", `{"name":"existing"}`, 201)
			f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","options":{"server_name":"www.example.com"}}`, 201)
			if err := db.SetSetting(context.Background(), f.conn, db.SetupCompleted, true); err != nil {
				t.Fatal(err)
			}
			if err := db.SetSetting(context.Background(), f.conn, db.SetupStep, "finish"); err != nil {
				t.Fatal(err)
			}
			reader, err := db.OpenReader(context.Background(), filepath.Join(databaseDirectory(t, f.conn), "api.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			store.DB = reader
			var token string
			if err := f.conn.QueryRow("SELECT sub_token FROM users WHERE id=1").Scan(&token); err != nil {
				t.Fatal(err)
			}
			service, err := auth.NewWithReader(f.conn, reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			paused := &pausedApplyRunner{resourceRunner: f.runner, phase: phase, entered: make(chan struct{}, 1), release: make(chan struct{})}
			defer close(paused.release)
			manager, err := singbox.NewManager(f.conn, singbox.ApplyOptions{OS: "linux", Config: f.config, ReadDB: reader, Runner: paused, Firewall: f.firewall})
			if err != nil {
				t.Fatal(err)
			}
			api := New(service, Options{DB: f.conn, ReadDB: reader, Singbox: manager, Applier: manager, Subscriptions: store, Logger: logger})
			h := &subscription.Handler{Store: store, Next: api, Logger: logger, Limiter: &subscription.Limiter{}}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(t, h, "POST", "https://example.test/api/v1/users", `{"name":"limited","traffic_limit":1610612736}`, f.cookie, nil)
			}()
			select {
			case <-paused.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("apply never reached pause")
			}
			maxLatency := time.Duration(0)
			for _, path := range []string{"/subscribe/" + token + "?format=singbox", "/api/v1/auth/me", "/api/v1/users", "/api/v1/settings", "/api/v1/setup/status", "/api/v1/rules", "/api/v1/inbounds", "/api/v1/users/1/links", "/api/v1/rules/preview"} {
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil).WithContext(ctx)
				r.AddCookie(f.cookie)
				w := httptest.NewRecorder()
				start := time.Now()
				h.ServeHTTP(w, r)
				maxLatency = max(maxLatency, time.Since(start))
				cancel()
				if w.Code != 200 || time.Since(start) >= time.Second {
					t.Errorf("read %s blocked/failed during %s: status=%d duration=%s", strings.Split(path, "?")[0][:min(10, len(strings.Split(path, "?")[0]))], phase, w.Code, time.Since(start))
				}
				if path == "/api/v1/users" && strings.Contains(w.Body.String(), "limited") {
					t.Error("uncommitted user became visible")
				}
			}
			t.Logf("%s: longest read during uncommitted Apply = %s", phase, maxLatency)
			for _, path := range []string{"singbox/logs", "singbox/config"} {
				start := time.Now()
				w := request(t, h, "GET", "https://example.test/api/v1/"+path, "", f.cookie, nil)
				if w.Code != 503 || time.Since(start) >= time.Second {
					t.Fatalf("diagnostic blocked: %s %d", path, w.Code)
				}
			}
			// Finish the real Apply and verify a positive quota is a valid active user.
			paused.release <- struct{}{}
			w := <-done
			if w.Code != 201 {
				t.Fatalf("limited user create: %d %s", w.Code, w.Body.String())
			}
			raw, err := os.ReadFile(f.config)
			if err != nil || !strings.Contains(string(raw), `"name": "limited"`) || !strings.Contains(string(raw), `"name": "existing"`) {
				t.Fatal("quota user or existing user missing from applied config")
			}
			if !strings.Contains(w.Body.String(), `"status":"active"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func databaseDirectory(t *testing.T, conn *sql.DB) string {
	t.Helper()
	var seq int
	var name, path string
	if err := conn.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(path)
}

// This is a real HTTP server: a client must receive JSON 503 before WriteTimeout,
// after synchronous recovery, rather than EOF/network_error or late success.
func TestLimitedUserApplyTimeoutHTTP(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	f.call(t, "POST", "users", `{"name":"existing"}`, 201)
	before, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &deadlineApplyRunner{resourceRunner: f.runner}
	manager, err := singbox.NewManager(f.conn, singbox.ApplyOptions{OS: "linux", Config: f.config, Runner: runner, Firewall: f.firewall, ApplyTimeout: 50 * time.Millisecond, RecoveryTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(service, Options{DB: f.conn, Applier: manager, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	server := httptest.NewUnstartedServer(h)
	server.Config.WriteTimeout = 2 * time.Second
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	r, err := http.NewRequest("POST", server.URL+"/api/v1/users", bytes.NewBufferString(`{"name":"limited","traffic_limit":1610612736}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.cookie)
	start := time.Now()
	response, err := client.Do(r)
	if err != nil {
		t.Fatalf("got network error instead of 503: %v", err)
	}
	defer response.Body.Close()
	var body struct {
		Error struct{ Code, Message, Hint string }
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 503 || body.Error.Code != "singbox_apply_timeout" || body.Error.Hint == "" || time.Since(start) >= server.Config.WriteTimeout {
		t.Fatalf("timeout response: %d %+v", response.StatusCode, body)
	}
	var count int
	if err := f.conn.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatal("timed out user was committed")
	}
	after, err := os.ReadFile(f.config)
	if err != nil || !bytes.Equal(before, after) || runner.restarts != 2 {
		t.Fatal("timeout did not restore original core config")
	}
}

type deadlineApplyRunner struct {
	*resourceRunner
	delayed bool
}

func (r *deadlineApplyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "restart" && !r.delayed {
		r.delayed = true
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.resourceRunner.Run(ctx, name, args...)
}

func TestReadPoolKeepsMetadataWritesOnWriter(t *testing.T) {
	f, store, _, _ := newSubscriptionAPI(t)
	reader, err := db.OpenReader(context.Background(), filepath.Join(databaseDirectory(t, f.conn), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	store.DB = reader
	service, err := auth.NewWithReader(f.conn, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h = New(service, Options{DB: f.conn, ReadDB: reader, Applier: f.apply, Subscriptions: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := db.SetSetting(context.Background(), f.conn, db.SetupCompleted, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), f.conn, db.SetupStep, "welcome"); err != nil {
		t.Fatal(err)
	}
	f.call(t, "PUT", "setup/status", `{"completed":true,"step":"finish"}`, 200)
	f.call(t, "PUT", "settings", `{"panel_name":"Updated"}`, 200)
	f.call(t, "PUT", "settings", `{"block_bittorrent":false}`, 200)
	f.call(t, "PUT", "rules", `{"preset":"global","source":"metacubex","update_interval":"1d","custom_direct":[],"custom_proxy":[],"dns":{"remote":"https://1.1.1.1/dns-query","local":"https://223.5.5.5/dns-query","fakeip":false},"ipv6":false,"block_ads":false,"mirror":""}`, 200)
	f.call(t, "POST", "users", `{"name":"limited","traffic_limit":1610612736}`, 201)
}

// Scale the old 30s HTTP window down to reproduce the transport failure without
// a 30s test: successful Apply finishes after the socket deadline, so the client
// sees an error even though the user was committed. The bounded 503 test above
// verifies the opposite ordering used by production.
func TestHTTPWriteTimeoutCanHideCommittedUser(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	runner := &slowSuccessApplyRunner{resourceRunner: f.runner}
	manager, err := singbox.NewManager(f.conn, singbox.ApplyOptions{OS: "linux", Config: f.config, Runner: runner, Firewall: f.firewall, ApplyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(New(service, Options{DB: f.conn, Applier: manager, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/api/v1/users", bytes.NewBufferString(`{"name":"limited","traffic_limit":1610612736}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.cookie)
	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Do(r)
	if err == nil {
		response.Body.Close()
		t.Fatal("expected expired HTTP write deadline")
	}
	var count int
	if err := f.conn.QueryRow("SELECT count(*) FROM users WHERE name='limited' AND traffic_limit=1610612736").Scan(&count); err != nil || count != 1 {
		t.Fatal("did not reproduce successful write hidden by HTTP timeout")
	}
}

type slowSuccessApplyRunner struct{ *resourceRunner }

func (r *slowSuccessApplyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "restart" {
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.resourceRunner.Run(ctx, name, args...)
}
