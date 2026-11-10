package acme

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
)

type issueFunc func(context.Context, string) (Material, error)

func (f issueFunc) Issue(ctx context.Context, name string) (Material, error) { return f(ctx, name) }

type fixture struct {
	m            *Manager
	conn         *sql.DB
	now          time.Time
	calls, opens int
	fail         error
	life         time.Duration
	logs         bytes.Buffer
	ca           *x509.Certificate
	key          *ecdsa.PrivateKey
	before       func(context.Context, string)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Now().Truncate(time.Second), life: 160 * time.Hour}
	f.ca, f.key = testCA(t)
	dir := t.TempDir()
	var err error
	f.conn, err = db.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.conn.Close() })
	for key, value := range map[db.SettingKey[string]]string{db.PublicHost: "panel.example.com", db.PanelTLSMode: "acme"} {
		if err := db.SetSetting(context.Background(), f.conn, key, value); err != nil {
			t.Fatal(err)
		}
	}
	certificate, key, err := certs.PanelHost("panel.example.com", f.now)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "original.crt"), filepath.Join(dir, "original.key")
	if err := os.WriteFile(certFile, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	f.m, err = New(context.Background(), f.conn, Options{Dir: filepath.Join(dir, "certs"), CertFile: certFile, KeyFile: keyFile, Directory: "http://127.0.0.1:1/directory", Now: func() time.Time { return f.now }, Logger: slog.New(slog.NewTextHandler(&f.logs, nil)), OpenPort: func(context.Context) error { f.opens++; return nil }, Issuer: issueFunc(func(ctx context.Context, name string) (Material, error) {
		f.calls++
		if f.before != nil {
			f.before(ctx, name)
		}
		if f.fail != nil {
			return Material{}, f.fail
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return Material{}, err
		}
		csr := &x509.CertificateRequest{PublicKey: &key.PublicKey, DNSNames: []string{name}}
		if ip := net.ParseIP(name); ip != nil {
			csr.DNSNames = nil
			csr.IPAddresses = []net.IP{ip}
		}
		certificate := signRequest(t, f.ca, f.key, csr, f.now, f.life)
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return Material{}, err
		}
		return Material{certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) tick(t *testing.T, force bool) Status {
	t.Helper()
	if err := f.m.Tick(context.Background(), force); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenewalHalfLifetimeHotSwapAndRestart(t *testing.T) {
	for _, life := range []time.Duration{160 * time.Hour, 90 * 24 * time.Hour} {
		t.Run(life.String(), func(t *testing.T) {
			f := newFixture(t)
			f.life = life
			original, _ := os.ReadFile(f.m.opts.CertFile)
			if f.calls != 0 {
				t.Fatal("construction contacted CA")
			}
			initial, _ := f.m.GetCertificate(nil)
			status := f.tick(t, false)
			if status.Type != "acme" || status.Failures != 0 || status.LastSuccess != f.now.Unix() || f.opens != 1 {
				t.Fatal(status, f.opens)
			}
			first, _ := f.m.GetCertificate(nil)
			if bytes.Equal(first.Certificate[0], initial.Certificate[0]) {
				t.Fatal("certificate not replaced")
			}
			f.now = f.now.Add(life/2 - time.Second)
			f.tick(t, false)
			if f.calls != 1 {
				t.Fatal("premature renewal")
			}
			f.now = f.now.Add(time.Second)
			f.tick(t, false)
			second, _ := f.m.GetCertificate(nil)
			if f.calls != 2 || bytes.Equal(second.Certificate[0], first.Certificate[0]) {
				t.Fatal("half-life renewal did not hot swap")
			}
			reloaded, err := New(context.Background(), f.conn, f.m.opts)
			if err != nil {
				t.Fatal(err)
			}
			loaded, _ := reloaded.GetCertificate(nil)
			if !bytes.Equal(loaded.Certificate[0], second.Certificate[0]) {
				t.Fatal("restart lost active certificate")
			}
			if err := reloaded.Tick(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if f.calls != 2 {
				t.Fatal("restart issued unnecessarily")
			}
			after, _ := os.ReadFile(f.m.opts.CertFile)
			if !bytes.Equal(after, original) {
				t.Fatal("original self-signed overwritten")
			}
			entries, _ := os.ReadDir(f.m.opts.Dir)
			for _, entry := range entries {
				info, _ := entry.Info()
				if info.Mode().Perm() != 0600 {
					t.Fatal("material is not private", entry.Name())
				}
			}
		})
	}
}

func TestFailedIssuanceFallbackBackoffAndWarning(t *testing.T) {
	f := newFixture(t)
	f.fail = ErrPort
	s := f.tick(t, false)
	if s.Type != "self_signed" || s.Mode != "acme" || s.LastError != "acme_port_unavailable" || s.Hint == "" || s.NextAttempt != f.now.Add(5*time.Minute).Unix() {
		t.Fatal(s)
	}
	f.tick(t, false)
	if f.calls != 1 {
		t.Fatal("ignored retry delay")
	}
	// Backoff persists across service restarts, rather than hitting CA each start.
	m, err := New(context.Background(), f.conn, f.m.opts)
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	f.now = f.now.Add(5 * time.Minute)
	s = f.tick(t, false)
	if s.Failures != 2 || s.NextAttempt != f.now.Add(10*time.Minute).Unix() {
		t.Fatal(s)
	}
	f.fail = errors.New("secret-token-password-private-key")
	for range 8 {
		f.now = time.Unix(s.NextAttempt, 0)
		s = f.tick(t, false)
	}
	if s.NextAttempt != f.now.Add(6*time.Hour).Unix() {
		t.Fatal("backoff is not capped", s)
	}
	if strings.Contains(f.logs.String(), "secret-token") {
		t.Fatal("raw CA error logged")
	}
	f.fail = nil
	s = f.tick(t, true)
	f.now = time.Unix(s.NotAfter, 0).Add(-23 * time.Hour)
	f.fail = errors.New("offline")
	s = f.tick(t, false)
	first, _ := f.m.GetCertificate(nil)
	if s.Type != "acme" || s.Warning {
		t.Fatal("should keep valid certificate after one failure", s)
	}
	f.now = time.Unix(s.NextAttempt, 0)
	s = f.tick(t, false)
	second, _ := f.m.GetCertificate(nil)
	if !s.Warning || s.Type != "acme" || !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("missing expiry warning or discarded valid cert", s)
	}
}

func TestSettingsChangesDiscardStaleOrderAndSwitchBack(t *testing.T) {
	f := newFixture(t)
	f.tick(t, false)
	f.before = func(context.Context, string) {
		if err := db.SetSetting(context.Background(), f.conn, db.PublicHost, "new.example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.Tick(context.Background(), true); err == nil {
		t.Fatal("stale issuance accepted")
	}
	pair, _ := f.m.GetCertificate(nil)
	if pair.Leaf.VerifyHostname("panel.example.com") != nil {
		t.Fatal("old active certificate lost")
	}
	f.before = nil
	s := f.tick(t, false)
	if s.Host != "new.example.com" || s.Type != "acme" {
		t.Fatal(s)
	}
	if err := db.SetSetting(context.Background(), f.conn, db.PanelTLSMode, "self_signed"); err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	s = f.tick(t, false)
	if s.Type != "self_signed" || f.calls != calls {
		t.Fatal(s)
	}
	pair, _ = f.m.GetCertificate(nil)
	if pair.Leaf.VerifyHostname("new.example.com") != nil {
		t.Fatal("fallback host mismatch")
	}
	if err := f.m.Request(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Request(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	f.tick(t, false)
	if f.m.requested.Load() {
		t.Fatal("worker left an already serviced manual request pending")
	}
	f.tick(t, false)
	if f.calls != calls+1 {
		t.Fatal("manual request must issue exactly once")
	}
}

func TestTLSHandshakeSeesHotReplacement(t *testing.T) {
	f := newFixture(t)
	initial, _ := f.m.GetCertificate(nil)
	roots := x509.NewCertPool()
	roots.AddCert(initial.Leaf)
	roots.AddCert(f.ca)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: f.m.GetCertificate}
	server.StartTLS()
	defer server.Close()
	get := func() []byte {
		transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "panel.example.com"}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: time.Second}
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.TLS.PeerCertificates[0].Raw
	}
	before := get()
	f.tick(t, false)
	after := get()
	if bytes.Equal(before, after) {
		t.Fatal("same running HTTPS server did not hot swap")
	}
}

func TestDevDoesNotIssueOrWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	m, err := New(context.Background(), nil, Options{Dev: true, Dir: dir, Issuer: issueFunc(func(context.Context, string) (Material, error) { t.Fatal("dev issued"); return Material{}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := m.Request(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dev created certificates")
	}
}

func TestWorkerCancelsOrderOnSettingsChange(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	f.m.opts.Issuer = issueFunc(func(ctx context.Context, _ string) (Material, error) {
		close(started)
		<-ctx.Done()
		return Material{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker did not stop")
		}
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := f.m.Request(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), f.conn, db.PanelTLSMode, "self_signed"); err != nil {
		t.Fatal(err)
	}
	f.m.Changed()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("mode change did not cancel issuance")
		case <-ticker.C:
			s, err := f.m.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !s.Issuing && s.Mode == "self_signed" && s.Type == "self_signed" {
				return
			}
		}
	}
}
