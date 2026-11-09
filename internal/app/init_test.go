package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{Database: filepath.Join(dir, "data", "boxpanel.db"), CertFile: filepath.Join(dir, "tls", "panel.crt"), KeyFile: filepath.Join(dir, "keys", "panel.key")}
}

func TestInitializePersistentStateAndReset(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	probeCalled := false
	opts := InitOptions{Paths: paths, Probe: func(ctx context.Context) (netip.Addr, error) {
		probeCalled = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("probe missing deadline")
		}
		return netip.MustParseAddr("203.0.113.5"), nil
	}}
	result, err := Initialize(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !probeCalled || result.WebPort < 10000 || result.WebPort > 60000 || result.TLSMode != "self_signed" {
		t.Fatal("invalid initialization defaults")
	}
	conn, err := OpenExisting(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cfg, err := LoadConfig(ctx, conn)
	if err != nil || cfg != result.Config {
		t.Fatalf("settings did not persist: %v", err)
	}
	sub, err := db.GetSetting(ctx, conn, db.SubPath)
	pattern := regexp.MustCompile(`^[a-z0-9]{12}$`)
	if err != nil || !pattern.MatchString(sub) || !pattern.MatchString(cfg.WebPath) || sub == cfg.WebPath {
		t.Fatal("invalid random paths")
	}
	completed, err := db.GetSetting(ctx, conn, db.SetupCompleted)
	if err != nil || completed {
		t.Fatal("setup should be incomplete")
	}
	rules, err := db.GetSetting(ctx, conn, db.Rules)
	if err != nil || rules.Preset != "bypass_cn" || len(rules.CustomDirect) != 0 {
		t.Fatal("invalid default rules")
	}
	var hash string
	if err := conn.QueryRow("SELECT password_hash FROM admins").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if match, err := auth.VerifyPassword(hash, result.Password); err != nil || !match {
		t.Fatal("initial credential hash invalid")
	}
	for _, table := range []string{"inbounds", "users", "sessions"} {
		var count int
		if err := conn.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("init created later-task business data")
		}
	}
	pair, err := tls.LoadX509KeyPair(paths.CertFile, paths.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.VerifyHostname("203.0.113.5") != nil {
		t.Fatal("public IP missing from SAN")
	}
	for _, name := range []string{paths.Database, paths.CertFile, paths.KeyFile} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe file permissions: %s", name)
		}
	}
	opts.Probe = func(context.Context) (netip.Addr, error) { t.Error("reinit called probe"); return netip.Addr{}, nil }
	if _, err := Initialize(ctx, opts); !errors.Is(err, ErrInitialized) {
		t.Fatalf("reinit: %v", err)
	}
	if got, _ := AdminUsername(ctx, conn); got != result.Username {
		t.Fatal("reinit changed admin")
	}
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(ctx, "127.0.0.1", result.Username, result.Password, "")
	if err != nil {
		t.Fatal(err)
	}
	reset, err := ResetAdmin(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Username == result.Username || reset.Password == result.Password {
		t.Fatal("reset reused credentials")
	}
	if _, err := service.Authenticate(ctx, session.Token); !errors.Is(err, auth.ErrSession) {
		t.Fatal("reset left session valid")
	}
	if _, err := service.Login(ctx, "127.0.0.1", reset.Username, reset.Password, ""); err != nil {
		t.Fatal("reset credentials do not work", err)
	}
	if cfg2, _ := LoadConfig(ctx, conn); cfg2 != cfg {
		t.Fatal("reset changed settings")
	}
}

func TestInitializationFailureRollsBackAndPreservesFiles(t *testing.T) {
	paths := testPaths(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Dir(paths.CertFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.CertFile, []byte("existing certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := InitOptions{Paths: paths, Port: 24567, PublicIP: "127.0.0.1", Probe: func(context.Context) (netip.Addr, error) {
		t.Error("explicit IP called network")
		return netip.Addr{}, nil
	}}
	if _, err := Initialize(ctx, opts); err == nil {
		t.Fatal("overwrote certificate")
	}
	data, _ := os.ReadFile(paths.CertFile)
	if string(data) != "existing certificate" {
		t.Fatal("existing certificate modified")
	}
	if _, err := os.Stat(paths.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed init left newly created key")
	}
	conn, err := OpenExisting(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow("SELECT (SELECT count(*) FROM admins)+(SELECT count(*) FROM settings)").Scan(&count); err != nil || count != 0 {
		t.Fatal("initialization was not transactional")
	}
	conn.Close()
	if err := os.Remove(paths.CertFile); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(ctx, opts); err != nil {
		t.Fatal("retry after failure", err)
	}
}

func TestInitializationProbeFailureAndDev(t *testing.T) {
	paths := testPaths(t)
	opts := InitOptions{Paths: paths, Probe: func(context.Context) (netip.Addr, error) {
		return netip.Addr{}, errors.New("probe unavailable; use --public-ip")
	}}
	if _, err := Initialize(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--public-ip") {
		t.Fatal("probe failure lacks recovery hint")
	}
	opts.Dev = true
	result, err := Initialize(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.URL() != "http://127.0.0.1:2053/" {
		t.Fatal("invalid dev defaults", result.URL())
	}
	if _, err := os.Stat(paths.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dev created TLS key")
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if conn, err := OpenExisting(context.Background(), missing); err == nil {
		conn.Close()
		t.Fatal("opened missing database")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only command created database")
	}
}

func TestConcurrentInitialization(t *testing.T) {
	opts := InitOptions{Paths: testPaths(t), PublicIP: "::1", Port: 24568}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := Initialize(context.Background(), opts); results <- err }()
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrInitialized) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatal("concurrent init replaced state")
	}
	if (Config{WebPort: 24568, PublicHost: "::1", WebPath: "panel", TLSMode: "self_signed"}).URL() != "https://[::1]:24568/panel/" {
		t.Fatal("IPv6 URL missing brackets")
	}
}
