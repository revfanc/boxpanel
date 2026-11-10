package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/subscription"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type setupSystem struct {
	fakeSystem
	ips      sysinfo.PublicIPs
	discover func(context.Context) (sysinfo.PublicIPs, error)
}

func (s *setupSystem) PublicIP(ctx context.Context) (sysinfo.PublicIPs, error) {
	if s.discover != nil {
		return s.discover(ctx)
	}
	return s.ips, s.err
}
func newSetup(t *testing.T, configure func(*Options)) *resourceFixture {
	t.Helper()
	f := newResources(t, true)
	ctx := context.Background()
	for _, err := range []error{db.SetSetting(ctx, f.conn, db.SetupCompleted, false), db.SetSetting(ctx, f.conn, db.SetupStep, "welcome"), db.SetSetting(ctx, f.conn, db.PublicHost, "203.0.113.1")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Dev: true, DB: f.conn, Applier: f.apply, System: &setupSystem{ips: sysinfo.PublicIPs{IPv4: "203.0.113.1", IPv6: "2001:db8::1"}},
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			t.Error("unexpected DNS")
			return nil, errors.New("no network")
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("unexpected TCP")
			return nil, errors.New("no network")
		},
	}
	if configure != nil {
		configure(&opts)
	}
	f.h = New(service, opts)
	return f
}
func TestSetupProgressProtectionValidationAndRollback(t *testing.T) {
	f := newSetup(t, nil)
	first := decodeResult[setupStatus](t, f.call(t, "GET", "setup/status", "", 200))
	if first.Completed || first.Step != "welcome" || !first.Hysteria2 || first.Domain != "" {
		t.Fatal(first)
	}
	for _, endpoint := range []struct{ method, path, body string }{{"GET", "setup/status", ""}, {"PUT", "setup/status", `{"step":"node"}`}, {"POST", "system/port-probe", `{"port":443}`}} {
		requireStatus(t, request(t, f.h, endpoint.method, "https://example.test/api/v1/"+endpoint.path, endpoint.body, nil, nil), 401)
		if endpoint.method != "GET" {
			requireStatus(t, request(t, f.h, endpoint.method, "https://example.test/api/v1/"+endpoint.path, endpoint.body, f.cookie, map[string]string{"Origin": "https://evil.test"}), 403)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"step":"unknown"}`, `{"completed":null}`, `{"completed":"true"}`, `{"step":"node","step":"user"}`, `{"unknown":1}`, `{"hysteria2":null}`, `{"domain":"127.0.0.1"}`, `{"domain":"https://example.com"}`, `{} {}`} {
		f.call(t, "PUT", "setup/status", body, 400)
	}
	for _, step := range []string{"check", "scenario", "node", "user", "firewall", "finish", "firewall", "user", "node", "scenario", "check", "welcome"} {
		s := decodeResult[setupStatus](t, f.call(t, "PUT", "setup/status", `{"step":"`+step+`"}`, 200))
		if s.Step != step || s.Completed {
			t.Fatal(s)
		}
		if got := decodeResult[setupStatus](t, f.call(t, "GET", "setup/status", "", 200)); got != s {
			t.Fatal("progress not persisted")
		}
	}
	s := decodeResult[setupStatus](t, f.call(t, "PUT", "setup/status", `{"completed":true,"hysteria2":false}`, 200))
	if !s.Completed || s.Hysteria2 || s.Step != "welcome" {
		t.Fatal(s)
	}
	if w := f.call(t, "HEAD", "setup/status", "", 200); w.Body.Len() != 0 {
		t.Fatal("HEAD body")
	}
	if w := f.call(t, "POST", "setup/status", `{}`, 405); w.Header().Get("Allow") != "GET, HEAD, PUT" {
		t.Fatal(w.Header())
	}
	f.call(t, "GET", "system/port-probe", "", 405)
	before := resourceSnapshot(t, f.conn)
	if _, err := f.conn.Exec(`CREATE TRIGGER fail_setup BEFORE UPDATE ON settings WHEN NEW.key='setup_step' BEGIN SELECT RAISE(ABORT,'secret-storage-error'); END`); err != nil {
		t.Fatal(err)
	}
	w := f.call(t, "PUT", "setup/status", `{"completed":false,"step":"finish","hysteria2":true}`, 500)
	if strings.Contains(w.Body.String(), "secret-storage-error") || resourceSnapshot(t, f.conn) != before {
		t.Fatal("leak or non-atomic setup write")
	}
	if f.apply.calls != 0 || f.runner.checks != 0 {
		t.Fatal("onboarding metadata should not restart sing-box")
	}
}
func TestSetupDomainAllAnswersMustPointHere(t *testing.T) {
	var answers []netip.Addr
	var dnsErr error
	var lookupName string
	f := newSetup(t, func(opts *Options) {
		opts.LookupIP = func(ctx context.Context, name string) ([]netip.Addr, error) {
			assertSetupDeadline(t, ctx)
			lookupName = name
			return answers, dnsErr
		}
	})
	before := resourceSnapshot(t, f.conn)
	for _, ips := range [][]string{{}, {"203.0.113.2"}, {"203.0.113.1", "127.0.0.1"}, {"203.0.113.1", "2001:db8::2"}, {"198.18.0.1"}} {
		answers = nil
		for _, ip := range ips {
			answers = append(answers, netip.MustParseAddr(ip))
		}
		w := f.call(t, "PUT", "setup/status", `{"domain":"node.example.com","step":"node"}`, 409)
		if !strings.Contains(w.Body.String(), "domain_not_pointing_here") || resourceSnapshot(t, f.conn) != before {
			t.Fatal("invalid DNS changed settings")
		}
	}
	dnsErr = errors.New("private DNS error")
	answers = []netip.Addr{netip.MustParseAddr("203.0.113.1")}
	f.call(t, "PUT", "setup/status", `{"domain":"node.example.com"}`, 409)
	dnsErr = nil
	for _, ips := range [][]string{{"203.0.113.1"}, {"2001:db8::1"}, {"203.0.113.1", "2001:db8::1"}} {
		answers = nil
		for _, ip := range ips {
			answers = append(answers, netip.MustParseAddr(ip))
		}
		s := decodeResult[setupStatus](t, f.call(t, "PUT", "setup/status", `{"domain":" Node.Example.COM ","step":"node","hysteria2":false}`, 200))
		host, err := db.GetSetting(context.Background(), f.conn, db.PublicHost)
		if err != nil || host != "node.example.com" || s.Domain != host || lookupName != host || s.Hysteria2 {
			t.Fatal(s, host, err)
		}
	}
	f.call(t, "PUT", "setup/status", `{"domain":"","step":"node"}`, 200)
	host, err := db.GetSetting(context.Background(), f.conn, db.PublicHost)
	if err != nil || host != "203.0.113.1" {
		t.Fatal(host, err)
	}
	if f.apply.calls != 0 {
		t.Fatal("domain metadata must not bypass or imitate Apply")
	}
}
func assertSetupDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		t.Error("missing shared three-second deadline")
	}
}
func TestPortProbePinsDiscoveredIPsAndClosesConnections(t *testing.T) {
	var mu sync.Mutex
	addresses := map[string]bool{}
	var peers []net.Conn
	f := newSetup(t, func(opts *Options) {
		opts.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			assertSetupDeadline(t, ctx)
			if network != "tcp" {
				t.Error(network)
			}
			conn, peer := net.Pipe()
			mu.Lock()
			addresses[address] = true
			peers = append(peers, peer)
			mu.Unlock()
			return conn, nil
		}
	})
	if err := db.SetSetting(context.Background(), f.conn, db.PublicHost, "attacker.example.com"); err != nil {
		t.Fatal(err)
	}
	w := request(t, f.h, "POST", "https://evil-header.test/api/v1/system/port-probe", `{"port":443}`, f.cookie, map[string]string{"X-Forwarded-Host": "127.0.0.1"})
	requireStatus(t, w, 200)
	if len(addresses) != 2 || !addresses["203.0.113.1:443"] || !addresses["[2001:db8::1]:443"] {
		t.Fatal(addresses)
	}
	for _, peer := range peers {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := peer.Read(make([]byte, 1)); err == nil {
			t.Error("connection not closed")
		}
		_ = peer.Close()
	}
	if strings.Count(w.Body.String(), `"ok":true`) != 2 {
		t.Fatal(w.Body.String())
	}
	if strings.Index(w.Body.String(), `"ip":"203.0.113.1"`) > strings.Index(w.Body.String(), `"ip":"2001:db8::1"`) {
		t.Fatal("port probe results must list IPv4 first")
	}
	for _, body := range []string{`{}`, `{"port":0}`, `{"port":65536}`, `{"port":1.5}`, `{"port":null}`, `{"port":443,"host":"127.0.0.1"}`, `{"port":443,"target":"https://evil.test"}`, `{"port":443,"proto":"udp"}`, `{"port":443,"port":80}`} {
		f.call(t, "POST", "system/port-probe", body, 400)
	}
	if f.apply.calls != 0 {
		t.Fatal("probe wrote resources")
	}
}
func TestPortProbeFailureAndUnsupportedAreHonest(t *testing.T) {
	system := &setupSystem{ips: sysinfo.PublicIPs{IPv4: "203.0.113.1"}}
	f := newSetup(t, func(opts *Options) {
		opts.System = system
		opts.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			assertSetupDeadline(t, ctx)
			return nil, errors.New("secret TCP failure")
		}
	})
	w := f.call(t, "POST", "system/port-probe", `{"port":1}`, 200)
	if !strings.Contains(w.Body.String(), `"ok":false`) || strings.Contains(w.Body.String(), "secret TCP") {
		t.Fatal(w.Body.String())
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "198.18.0.1", "100.64.0.1", "::1", "fe80::1", "invalid", ""} {
		system.ips = sysinfo.PublicIPs{IPv4: ip}
		f.call(t, "POST", "system/port-probe", `{"port":443}`, 500)
	}
	system.err = sysinfo.ErrUnsupported
	f.call(t, "POST", "system/port-probe", `{"port":443}`, 409)
	system.err = nil
	system.discover = func(ctx context.Context) (sysinfo.PublicIPs, error) {
		assertSetupDeadline(t, ctx)
		return sysinfo.PublicIPs{IPv6: "2001:db8::1"}, nil
	}
	f.call(t, "POST", "system/port-probe", `{"port":65535}`, 200)
}
func TestSetupNetworkCancellation(t *testing.T) {
	system := &setupSystem{discover: func(ctx context.Context) (sysinfo.PublicIPs, error) {
		assertSetupDeadline(t, ctx)
		<-ctx.Done()
		return sysinfo.PublicIPs{}, ctx.Err()
	}}
	f := newSetup(t, func(opts *Options) {
		opts.System = system
		opts.LookupIP = func(ctx context.Context, _ string) ([]netip.Addr, error) {
			assertSetupDeadline(t, ctx)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	for _, endpoint := range []struct{ path, body string }{{"setup/status", `{"domain":"node.example.com"}`}, {"system/port-probe", `{"port":443}`}} {
		method := "POST"
		if endpoint.path == "setup/status" {
			method = "PUT"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		r := httptest.NewRequest(method, "https://example.test/api/v1/"+endpoint.path, strings.NewReader(endpoint.body)).WithContext(ctx)
		r.AddCookie(f.cookie)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		start := time.Now()
		f.h.ServeHTTP(w, r)
		cancel()
		requireStatus(t, w, 500)
		if time.Since(start) > time.Second {
			t.Fatal("request cancellation ignored")
		}
	}
}

// The same initialization and DevDBOnly Manager used by serve --dev, starting
// with an empty on-disk database. All network boundaries are replaced.
func TestSetupFreshDevLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	paths := app.Paths{Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "panel.crt"), KeyFile: filepath.Join(dir, "panel.key")}
	initialized, err := app.Initialize(ctx, app.InitOptions{Paths: paths, Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := app.OpenExisting(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "sing-box.json")
	manager, err := singbox.NewManager(conn, singbox.ApplyOptions{Dev: true, DevDBOnly: true, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	store := &subscription.Store{DB: conn}
	opts := Options{Dev: true, DB: conn, Applier: manager, Subscriptions: store, PanelPort: 2053, RealityProbe: func(_ context.Context, server string, _ int) reality.Target {
		return reality.Target{Server: server, Hint: "Use a manual target"}
	}}
	h := New(service, opts)
	credentials, _ := json.Marshal(map[string]string{"username": initialized.Username, "password": initialized.Password})
	w := request(t, h, "POST", "http://localhost/api/v1/auth/login", string(credentials), nil, nil)
	requireStatus(t, w, 200)
	f := &resourceFixture{h: h, conn: conn, cookie: w.Result().Cookies()[0]}
	if s := decodeResult[setupStatus](t, f.call(t, "GET", "setup/status", "", 200)); s.Completed || s.Step != "welcome" {
		t.Fatal(s)
	}
	f.call(t, "PUT", "setup/status", `{"step":"check"}`, 200)
	c := decodeResult[sysinfo.Check](t, f.call(t, "GET", "system/check", "", 200))
	for _, item := range c.Items {
		if item.Level == "error" {
			t.Fatal("dev check prevents onboarding")
		}
	}
	f.call(t, "PUT", "setup/status", `{"step":"scenario"}`, 200)
	f.call(t, "PUT", "setup/status", `{"domain":"","hysteria2":true,"step":"node"}`, 200)
	f.call(t, "POST", "inbounds/suggest", `{"protocol":"vless_reality"}`, 200)
	for _, body := range []string{`{"protocol":"vless_reality","options":{"server_name":"www.example.com"}}`, `{"protocol":"hysteria2"}`} {
		w = f.call(t, "POST", "inbounds", body, 201)
		if w.Header().Get("X-Boxpanel-Apply") != "dev-db-only" {
			t.Fatal("missing development marker")
		}
	}
	f.call(t, "PUT", "setup/status", `{"step":"user"}`, 200)
	f.call(t, "POST", "users", `{"name":"me"}`, 201)
	f.call(t, "PUT", "setup/status", `{"step":"firewall"}`, 200)
	f.call(t, "POST", "system/port-probe", `{"port":443}`, 409)
	f.call(t, "PUT", "setup/status", `{"step":"finish"}`, 200)
	f.call(t, "GET", "users/1/links", "", 409)
	f.call(t, "PUT", "setup/status", `{"completed":true}`, 200)
	// Reconstructing the handler must not reset persisted progress/preferences.
	f.h = New(service, opts)
	if s := decodeResult[setupStatus](t, f.call(t, "GET", "setup/status", "", 200)); !s.Completed || s.Step != "finish" || !s.Hysteria2 {
		t.Fatal(s)
	}
	f.call(t, "POST", "inbounds/2/toggle", `{"enabled":false}`, 200)
	links := decodeResult[subscription.Links](t, f.call(t, "GET", "users/1/links", "", 200))
	if len(links.Nodes) != 1 || links.Subscription["auto"] == "" {
		t.Fatal("Reality-only dev subscription unavailable")
	}
	for _, path := range []string{config, paths.CertFile, paths.KeyFile, filepath.Join(dir, "certs")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("dev created runtime config or certificate")
		}
	}
}

func TestSetupDevDomainDoesNotQueryDNS(t *testing.T) {
	f := newSetup(t, func(opts *Options) { opts.System = sysinfo.New(sysinfo.Options{Dev: true}) })
	f.call(t, "PUT", "setup/status", `{"domain":"node.example.com","step":"node"}`, 409)
	f.call(t, "POST", "system/port-probe", `{"port":443}`, 409)
	if s := decodeResult[setupStatus](t, f.call(t, "GET", "setup/status", "", 200)); s.Step != "welcome" || s.Domain != "" {
		t.Fatal("unsupported check changed setup")
	}
}
