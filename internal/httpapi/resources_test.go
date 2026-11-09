package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

// The handlers use the real Manager with fake system boundaries. This proves
// they enter Apply, keep a single transaction, and really roll back on failure.
type resourceRunner struct {
	checks, restarts       int
	failCheck, failRestart bool
}

func (f *resourceRunner) LookPath(name string) (string, error) { return name, nil }
func (f *resourceRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "check" {
		f.checks++
		if f.failCheck {
			return []byte("password=DO-NOT-LEAK"), errors.New("secret-check-error")
		}
		return nil, nil
	}
	if name == "journalctl" {
		return []byte("password=DO-NOT-LEAK"), nil
	}
	if len(args) > 0 && args[0] == "restart" {
		f.restarts++
		if f.failRestart {
			f.failRestart = false
			return nil, errors.New("secret-restart-error")
		}
	}
	return []byte("active\n"), nil
}

type resourceFirewall struct{ plan firewall.Plan }

func (f *resourceFirewall) Sync(_ context.Context, _, next firewall.Plan) error {
	f.plan = next
	return nil
}

type recordingApplier struct {
	manager *singbox.Manager
	calls   int
}

func (a *recordingApplier) Apply(ctx context.Context, mutation singbox.Mutation) error {
	a.calls++
	return a.manager.Apply(ctx, mutation)
}

type resourceFixture struct {
	h        http.Handler
	conn     *sql.DB
	cookie   *http.Cookie
	runner   *resourceRunner
	firewall *resourceFirewall
	apply    *recordingApplier
	logs     *bytes.Buffer
	config   string
	busy     map[sysinfo.Socket]bool
}

func newResources(t *testing.T, dev bool) *resourceFixture {
	t.Helper()
	_, conn, logs := testAPI(t, dev)
	ctx := context.Background()
	for _, err := range []error{db.SetSetting(ctx, conn, db.BlockBittorrent, true), db.SetSetting(ctx, conn, db.WebPort, 2053), db.SetSetting(ctx, conn, db.ClashAPIPort, 61000), db.SetSetting(ctx, conn, db.ClashAPISecret, "fixed-test-control-secret")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	f := &resourceFixture{conn: conn, runner: &resourceRunner{}, firewall: &resourceFirewall{}, logs: logs, config: filepath.Join(t.TempDir(), "sing-box.json"), busy: map[sysinfo.Socket]bool{}}
	manager, err := singbox.NewManager(conn, singbox.ApplyOptions{OS: "linux", Dev: dev, DevDBOnly: dev, Config: f.config, Runner: f.runner, Firewall: f.firewall})
	if err != nil {
		t.Fatal(err)
	}
	f.apply = &recordingApplier{manager: manager}
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h = New(service, Options{Dev: dev, DB: conn, Applier: f.apply, Singbox: manager, PanelPort: 2053, Logger: slog.New(slog.NewTextHandler(logs, nil)), ListeningPorts: func(context.Context) (map[sysinfo.Socket]bool, error) { return f.busy, nil }, RealityProbe: func(_ context.Context, server string, _ int) reality.Target {
		return reality.Target{Server: server, TLS13: true, H2: true, OK: true, LatencyMS: 10}
	}})
	w := request(t, f.h, "POST", "https://example.test/api/v1/auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, w, 200)
	f.cookie = w.Result().Cookies()[0]
	return f
}
func (f *resourceFixture) call(t *testing.T, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := request(t, f.h, method, "https://example.test/api/v1/"+path, body, f.cookie, nil)
	requireStatus(t, w, status)
	return w
}
func (f *resourceFixture) write(t *testing.T, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	calls, checks := f.apply.calls, f.runner.checks
	w := f.call(t, method, path, body, status)
	if f.apply.calls != calls+1 || f.runner.checks != checks+1 {
		t.Fatalf("write bypassed Apply/check: %s %s", method, path)
	}
	if status < 300 && w.Header().Get("X-Boxpanel-Apply") != "applied" {
		t.Fatal("missing apply marker")
	}
	return w
}
func decodeResult[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUserHandlersEveryWriteAppliesAndRedacts(t *testing.T) {
	f := newResources(t, false)
	if body := f.call(t, "GET", "users", "", 200).Body.String(); strings.TrimSpace(body) != "[]" {
		t.Fatal(body)
	}
	u := decodeResult[userView](t, f.write(t, "POST", "users", `{"name":"alice","remark":"test","traffic_limit":1000}`, 201))
	if u.ID != 1 || u.Status != "active" || !u.Enabled || u.TrafficLimit != 1000 {
		t.Fatalf("user=%+v", u)
	}
	var uuid, password, token string
	if err := f.conn.QueryRow("SELECT uuid,password,sub_token FROM users WHERE id=1").Scan(&uuid, &password, &token); err != nil {
		t.Fatal(err)
	}
	if len(uuid) != 36 || uuid[14] != '4' || !strings.Contains("89ab", uuid[19:20]) {
		t.Fatal("invalid v4 UUID")
	}
	for _, secret := range []string{password, token} {
		raw, err := base64.RawURLEncoding.DecodeString(secret)
		if err != nil || len(raw) != 24 {
			t.Fatal("invalid random credential")
		}
	}
	for _, path := range []string{"users", "users/1"} {
		w := f.call(t, "GET", path, "", 200)
		for _, secret := range []string{uuid, password, token} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("credential leaked")
			}
		}
	}
	u = decodeResult[userView](t, f.write(t, "PUT", "users/1", `{"name":"alice_new","remark":"updated","expire_at":1}`, 200))
	if u.Status != "expired" || u.Name != "alice_new" {
		t.Fatal(u)
	}
	u = decodeResult[userView](t, f.write(t, "POST", "users/1/toggle", `{"enabled":false}`, 200))
	if u.Status != "disabled" {
		t.Fatal(u)
	}
	f.write(t, "POST", "users/1/reset-token", "", 200)
	var nextUUID, nextPassword, nextToken string
	if err := f.conn.QueryRow("SELECT uuid,password,sub_token FROM users WHERE id=1").Scan(&nextUUID, &nextPassword, &nextToken); err != nil {
		t.Fatal(err)
	}
	if nextToken == token || nextUUID != uuid || nextPassword != password {
		t.Fatal("token reset changed wrong credentials")
	}
	f.write(t, "POST", "users/1/reset-credentials", "", 200)
	if err := f.conn.QueryRow("SELECT uuid,password,sub_token FROM users WHERE id=1").Scan(&nextUUID, &nextPassword, &token); err != nil {
		t.Fatal(err)
	}
	if nextUUID == uuid || nextPassword == password || token != nextToken {
		t.Fatal("credentials reset changed wrong credentials")
	}
	f.write(t, "DELETE", "users/1", "", 204)
	f.call(t, "GET", "users/1", "", 404)
	for _, secret := range []string{uuid, password, token, nextUUID, nextPassword} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("secret logged")
		}
	}
}
func TestInboundHandlersEveryWriteAppliesAndFirewallFollows(t *testing.T) {
	f := newResources(t, false)
	in := decodeResult[inboundRecord](t, f.write(t, "POST", "inbounds", `{"protocol":"vless_reality"}`, 201))
	if in.Port != 443 || in.Tag != "vless-reality-443" || !in.Enabled {
		t.Fatal(in)
	}
	stored, err := readInbound(context.Background(), f.conn, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	var options confgen.RealityOptions
	if err := json.Unmarshal(stored.Options, &options); err != nil {
		t.Fatal(err)
	}
	public, err := reality.PublicKey(options.PrivateKey)
	if err != nil || public != options.PublicKey {
		t.Fatal("bad generated keypair")
	}
	for _, path := range []string{"inbounds", "inbounds/1"} {
		w := f.call(t, "GET", path, "", 200)
		if strings.Contains(w.Body.String(), options.PrivateKey) || strings.Contains(w.Body.String(), "private_key") {
			t.Fatal("private key leaked")
		}
	}
	f.busy[sysinfo.Socket{Port: 443, Proto: "tcp"}] = true // own running listener must remain editable
	f.write(t, "PUT", "inbounds/1", `{"remark":"renamed","options":{"fingerprint":"firefox"}}`, 200)
	updated, err := readInbound(context.Background(), f.conn, 1)
	if err != nil {
		t.Fatal(err)
	}
	var opts confgen.RealityOptions
	_ = json.Unmarshal(updated.Options, &opts)
	if opts.PrivateKey != options.PrivateKey || opts.Fingerprint != "firefox" {
		t.Fatal("partial options lost secret")
	}
	f.write(t, "POST", "inbounds/1/toggle", `{"enabled":false}`, 200)
	if len(f.firewall.plan.Ports) != 0 {
		t.Fatal("disabled port still open")
	}
	delete(f.busy, sysinfo.Socket{Port: 443, Proto: "tcp"})
	f.write(t, "POST", "inbounds/1/toggle", `{"enabled":true}`, 200)
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","options":{"obfs_password":"obfs-PRIVATE-test","port_hopping":{"enabled":true}}}`, 201)
	if len(f.firewall.plan.Hops) != 1 || len(f.firewall.plan.Ports) != 2 {
		t.Fatal(f.firewall.plan)
	}
	w := f.call(t, "GET", "inbounds/2", "", 200)
	if strings.Contains(w.Body.String(), "obfs-PRIVATE-test") || strings.Contains(w.Body.String(), "obfs_password") {
		t.Fatal("obfs leaked")
	}
	f.write(t, "DELETE", "inbounds/2", "", 204)
	if len(f.firewall.plan.Hops) != 0 || len(f.firewall.plan.Ports) != 1 {
		t.Fatal("deleted hop not removed")
	}
	f.write(t, "DELETE", "inbounds/1", "", 204)
	f.call(t, "GET", "inbounds/1", "", 404)
}

func TestResourceValidationConflictsProtectionAndScope(t *testing.T) {
	f := newResources(t, false)
	for _, path := range []string{"users", "users/1", "inbounds", "inbounds/1", "reality/recommend"} {
		requireStatus(t, request(t, f.h, "GET", "https://example.test/api/v1/"+path, "", nil, nil), 401)
	}
	for _, tt := range []struct{ method, path, body string }{
		{"POST", "users", `{"name":"alice"}`}, {"PUT", "users/1", `{}`}, {"DELETE", "users/1", ""}, {"POST", "users/1/toggle", `{"enabled":true}`}, {"POST", "users/1/reset-token", ""}, {"POST", "users/1/reset-credentials", ""},
		{"POST", "inbounds", `{"protocol":"hysteria2"}`}, {"PUT", "inbounds/1", `{}`}, {"DELETE", "inbounds/1", ""}, {"POST", "inbounds/1/toggle", `{"enabled":true}`}, {"POST", "inbounds/suggest", `{"protocol":"hysteria2"}`}, {"POST", "reality/check", `{"server":"www.example.com"}`},
	} {
		before := f.apply.calls
		requireStatus(t, request(t, f.h, tt.method, "https://example.test/api/v1/"+tt.path, tt.body, nil, nil), 401)
		requireStatus(t, request(t, f.h, tt.method, "https://example.test/api/v1/"+tt.path, tt.body, f.cookie, map[string]string{"Origin": "https://evil.test"}), 403)
		if f.apply.calls != before {
			t.Fatal("unprotected write")
		}
	}
	for _, body := range []string{`null`, `[]`, `{"name":null}`, `{"name":"bad/name"}`, `{"name":""}`, `{"name":"a","traffic_limit":-1}`, `{"name":"a","expire_at":-1}`, `{"name":"a","uuid":"injected"}`, `{"name":"a","name":"b"}`, `{"name":"a","enabled":false}`, `{"name":"a"} {}`, strings.Repeat("x", 9000)} {
		f.call(t, "POST", "users", body, 400)
	}
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.call(t, "POST", "users", `{"name":"alice"}`, 409)
	f.write(t, "POST", "users", `{"name":"bob"}`, 201)
	f.call(t, "PUT", "users/2", `{"name":"alice"}`, 409)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":1}`, `{"enabled":true,"other":1}`} {
		f.call(t, "POST", "users/1/toggle", body, 400)
		f.call(t, "POST", "inbounds/1/toggle", body, 400)
	}
	for _, body := range []string{`{}`, `{"protocol":"tuic"}`, `{"protocol":"hysteria2","port":0}`, `{"protocol":"hysteria2","port":65536}`, `{"protocol":"hysteria2","tag":"bad/name"}`, `{"protocol":"hysteria2","options":{"sni":null}}`, `{"protocol":"hysteria2","options":{"unknown":1}}`, `{"protocol":"hysteria2","options":{"cert_mode":"acme"}}`, `{"protocol":"hysteria2","options":{"up_mbps":-1}}`, `{"protocol":"hysteria2","options":{"port_hopping":{"start":30,"end":20}}}`, `{"protocol":"vless_reality","options":{"private_key":"secret-invalid"}}`, `{"protocol":"vless_reality","options":{"short_ids":["xx"]}}`} {
		f.call(t, "POST", "inbounds", body, 400)
	}
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2"}`, 201)
	for _, body := range []string{`{"protocol":"hysteria2","port":8443}`, `{"protocol":"hysteria2","port":2053}`, `{"protocol":"vless_reality","port":61000}`, `{"protocol":"hysteria2","port":9000,"tag":"hysteria2-8443"}`} {
		f.call(t, "POST", "inbounds", body, 409)
	}
	f.busy[sysinfo.Socket{Port: 9000, Proto: "udp"}] = true
	f.call(t, "POST", "inbounds", `{"protocol":"hysteria2","port":9000}`, 409)
	f.call(t, "PUT", "inbounds/1", `{"protocol":"vless_reality"}`, 400)
	for _, path := range []string{"users/0", "users/-1", "users/no", "inbounds/999999999999999999999"} {
		f.call(t, "GET", path, "", 400)
	}
	for _, path := range []string{"users/999", "inbounds/999"} {
		f.call(t, "GET", path, "", 404)
		f.call(t, "PUT", path, `{}`, 404)
		f.call(t, "DELETE", path, "", 404)
	}
	for _, path := range []string{"users", "inbounds", "users/1", "inbounds/1", "reality/recommend"} {
		w := f.call(t, "HEAD", path, "", 200)
		if w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	w := f.call(t, "PATCH", "users/1", `{}`, 405)
	if w.Header().Get("Allow") != "GET, HEAD, PUT, DELETE" {
		t.Fatal(w.Header())
	}
	f.call(t, "GET", "inbounds/suggest", "", 405)
	for _, path := range []string{"setup/status", "singbox/upgrade"} {
		f.call(t, "GET", path, "", 404)
	}
	if strings.Contains(f.logs.String(), "secret-invalid") {
		t.Fatal("invalid credential logged")
	}
}

func resourceSnapshot(t *testing.T, conn *sql.DB) string {
	t.Helper()
	var result strings.Builder
	for _, table := range []string{"users", "inbounds", "settings"} {
		rows, err := conn.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(values))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(values)
			result.Write(encoded)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return result.String()
}
func TestEveryResourceWriteRollsBackOnCheckFailure(t *testing.T) {
	f := newResources(t, false)
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality"}`, 201)
	before := resourceSnapshot(t, f.conn)
	oldConfig, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.failCheck = true
	for _, tt := range []struct{ method, path, body string }{
		{"POST", "users", `{"name":"bob"}`}, {"PUT", "users/1", `{"name":"changed"}`}, {"DELETE", "users/1", ""}, {"POST", "users/1/toggle", `{"enabled":false}`}, {"POST", "users/1/reset-token", ""}, {"POST", "users/1/reset-credentials", ""},
		{"POST", "inbounds", `{"protocol":"hysteria2"}`}, {"PUT", "inbounds/1", `{"port":9443}`}, {"DELETE", "inbounds/1", ""}, {"POST", "inbounds/1/toggle", `{"enabled":false}`},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			w := f.write(t, tt.method, tt.path, tt.body, 422)
			if strings.Contains(w.Body.String(), "DO-NOT-LEAK") || strings.Contains(w.Body.String(), "secret-check-error") {
				t.Fatal("check error leaked")
			}
			if got := resourceSnapshot(t, f.conn); got != before {
				t.Fatal("failed mutation committed")
			}
			got, err := os.ReadFile(f.config)
			if err != nil || !bytes.Equal(oldConfig, got) {
				t.Fatal("failed check changed config")
			}
		})
	}
	f.runner.failCheck = false
	f.runner.failRestart = true
	f.write(t, "PUT", "users/1", `{"remark":"restart fails"}`, 500)
	if resourceSnapshot(t, f.conn) != before {
		t.Fatal("restart failure committed")
	}
	got, err := os.ReadFile(f.config)
	if err != nil || !bytes.Equal(oldConfig, got) {
		t.Fatal("restart failure did not restore file")
	}
}

func TestTypicalWritesWithSingleConnectionDoNotDeadlock(t *testing.T) {
	f := newResources(t, false)
	if f.conn.Stats().MaxOpenConnections != 1 {
		t.Fatal("test requires single connection")
	}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "users", `{"name":"alice"}`, 201},
		{"POST", "inbounds", `{"protocol":"hysteria2"}`, 201},
		{"PUT", "inbounds/1", `{"port":9001}`, 200},
		{"PUT", "users/1", `{"traffic_limit":1024}`, 200},
		{"POST", "users/1/reset-credentials", "", 200},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := httptest.NewRequest(tt.method, "https://example.test/api/v1/"+tt.path, strings.NewReader(tt.body)).WithContext(ctx)
		r.AddCookie(f.cookie)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { f.h.ServeHTTP(w, r); close(done) }()
		select {
		case <-done:
			requireStatus(t, w, tt.status)
		case <-ctx.Done():
			cancel()
			t.Fatal("typical write deadlocked with MaxOpenConns(1)")
		}
		cancel()
	}
}

func TestSuggestionsProbesAndDevelopmentMode(t *testing.T) {
	f := newResources(t, true)
	before := resourceSnapshot(t, f.conn)
	for _, protocol := range []string{"vless_reality", "hysteria2"} {
		w := f.call(t, "POST", "inbounds/suggest", fmt.Sprintf(`{"protocol":%q}`, protocol), 200)
		if strings.Contains(w.Body.String(), "private_key") || strings.Contains(w.Body.String(), "public_key") {
			t.Fatal("suggest exposed unusable credentials")
		}
		// Suggest is directly usable as create input; fresh credentials remain on server.
		f.call(t, "POST", "inbounds", w.Body.String(), 201)
	}
	if f.apply.calls != 2 || f.runner.checks != 0 || f.runner.restarts != 0 {
		t.Fatal("dev substitute executed core")
	}
	if after := resourceSnapshot(t, f.conn); after == before {
		t.Fatal("dev writes not saved")
	}
	if _, err := os.Stat(f.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dev wrote configuration")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.config), "certs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dev wrote certificates")
	}
	before = resourceSnapshot(t, f.conn)
	w := f.call(t, "POST", "inbounds/suggest", `{"protocol":"hysteria2"}`, 200)
	var suggestion struct{ Port int }
	_ = json.Unmarshal(w.Body.Bytes(), &suggestion)
	if suggestion.Port == 8443 || suggestion.Port == 443 || suggestion.Port == 2053 {
		t.Fatal("suggest reserved port")
	}
	f.call(t, "GET", "reality/recommend", "", 200)
	f.call(t, "POST", "reality/check", `{"server":"www.example.com","port":8443}`, 200)
	if resourceSnapshot(t, f.conn) != before || f.apply.calls != 2 {
		t.Fatal("read-only operation mutated DB")
	}
	w = f.call(t, "POST", "users", `{"name":"dev"}`, 201)
	if w.Header().Get("X-Boxpanel-Apply") != "dev-db-only" {
		t.Fatal("dev write not labeled")
	}
	f.call(t, "POST", "singbox/restart", "", 409)
	for _, body := range []string{`{"server":"127.0.0.1"}`, `{"server":"https://www.example.com"}`, `{"server":"www.example.com","port":0}`, `{"server":"www.example.com","port":null}`} {
		f.call(t, "POST", "reality/check", body, 400)
	}
}

func TestNonLinuxWithoutDevRejectsWrites(t *testing.T) {
	f := newResources(t, false)
	manager, err := singbox.NewManager(f.conn, singbox.ApplyOptions{OS: "darwin", Config: f.config})
	if err != nil {
		t.Fatal(err)
	}
	f.apply.manager = manager
	before := resourceSnapshot(t, f.conn)
	f.call(t, "POST", "users", `{"name":"denied"}`, 409)
	f.call(t, "POST", "inbounds", `{"protocol":"hysteria2"}`, 409)
	if resourceSnapshot(t, f.conn) != before || f.runner.checks != 0 {
		t.Fatal("unsupported platform changed state")
	}
}

func TestUserStatusAndPortHoppingConflicts(t *testing.T) {
	f := newResources(t, false)
	f.write(t, "POST", "users", `{"name":"alice","traffic_limit":10}`, 201)
	// Existing traffic samples are fixtures; T3.1 does not implement accounting.
	if _, err := f.conn.Exec("UPDATE users SET traffic_up=9223372036854775807,traffic_down=9223372036854775807 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if u := decodeResult[userView](t, f.call(t, "GET", "users/1", "", 200)); u.Status != "exhausted" {
		t.Fatal("overflow in status")
	}
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","options":{"port_hopping":{"enabled":true,"start":20000,"end":21000}}}`, 201)
	for _, body := range []string{
		`{"protocol":"hysteria2","port":20000}`,
		`{"protocol":"hysteria2","port":9001,"options":{"port_hopping":{"enabled":true,"start":20999,"end":22000}}}`,
		`{"protocol":"hysteria2","port":9001,"options":{"port_hopping":{"enabled":true,"start":8000,"end":8500}}}`,
	} {
		f.call(t, "POST", "inbounds", body, 409)
	}
	f.write(t, "POST", "inbounds/1/toggle", `{"enabled":false}`, 200)
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","port":20001}`, 201)
	f.call(t, "POST", "inbounds/1/toggle", `{"enabled":true}`, 409)
	if !reflect.DeepEqual(f.firewall.plan.Ports, []firewall.Port{{Port: 20001, Proto: "udp"}}) {
		t.Fatal(f.firewall.plan)
	}
}

func TestInboundPortChangesRetainUsableTagsAndOwnSockets(t *testing.T) {
	f := newResources(t, false)
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","port":20001,"options":{"port_hopping":{"enabled":true}}}`, 201)
	f.busy[sysinfo.Socket{Port: 20001, Proto: "udp"}] = true
	f.write(t, "PUT", "inbounds/1", `{"port":20002}`, 200)
	// Clash's TCP port is not a UDP conflict, even when it was selected by
	// Apply after this Hysteria2 inbound was created.
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","port":61000}`, 201)
}
