package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/subscription"
)

func newSubscriptionAPI(t *testing.T) (*resourceFixture, *subscription.Store, *int, *int) {
	t.Helper()
	f := newResources(t, false)
	ctx := context.Background()
	for _, x := range []struct {
		k db.SettingKey[string]
		v string
	}{{db.PublicHost, "2001:db8::8"}, {db.PanelName, "面板"}, {db.SubPath, "subscribe"}, {db.WebPath, "panel"}, {db.PanelTLSMode, "self_signed"}} {
		if err := db.SetSetting(ctx, f.conn, x.k, x.v); err != nil {
			t.Fatal(err)
		}
	}
	r := db.RulesSettings{Preset: "bypass_cn", Source: "metacubex", UpdateInterval: "1d", DNS: db.DNSSettings{Remote: "https://1.1.1.1/dns-query", Local: "https://223.5.5.5/dns-query"}, CustomDirect: []string{}, CustomProxy: []string{}}
	if err := db.SetSetting(ctx, f.conn, db.Rules, r); err != nil {
		t.Fatal(err)
	}
	store := &subscription.Store{DB: f.conn, Certificate: func(id int64, sni string) ([]byte, error) {
		hash := sha256.Sum256([]byte(sni))
		return os.ReadFile(filepath.Join(filepath.Dir(f.config), "certs", fmt.Sprintf("hy2-%d-%x.crt", id, hash[:8])))
	}}
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	commits, cancels := new(int), new(int)
	f.h = New(service, Options{DB: f.conn, Applier: f.apply, Subscriptions: store, Logger: slog.New(slog.NewTextHandler(f.logs, nil)), PanelPort: 2053, PreparePanel: func(context.Context, PanelSettings) (func(), func(), error) {
		done := false
		return func() { done = true; *commits++ }, func() {
			if !done {
				*cancels++
			}
		}, nil
	}})
	return f, store, commits, cancels
}
func TestSubscriptionManagement(t *testing.T) {
	f, store, _, _ := newSubscriptionAPI(t)
	f.call(t, "GET", "rules/preview", "", 404)
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality","options":{"server_name":"www.example.com"}}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2","options":{"obfs_password":"secret-obfs-quote\""}}`, 201)
	w := f.call(t, "GET", "users/1/links", "", 200)
	var links subscription.Links
	if err := json.Unmarshal(w.Body.Bytes(), &links); err != nil {
		t.Fatal(err)
	}
	if len(links.Nodes) != 2 || !strings.HasPrefix(links.Subscription["auto"], "https://[2001:db8::8]:2053/subscribe/") {
		t.Fatal(links)
	}
	d, err := store.Load(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"singbox", "clash"} {
		w = f.call(t, "GET", "rules/preview?format="+format, "", 200)
		for _, secret := range []string{d.User.UUID, d.User.Password, d.User.Token, "secret-obfs"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("preview leaked credentials")
			}
		}
		var preview struct {
			Format string
			Config json.RawMessage
		}
		if json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Format != format {
			t.Fatal(w.Body.String())
		}
	}
	for _, path := range []string{"users/1/links", "rules", "rules/preview?format=clash", "settings"} {
		w = f.call(t, "HEAD", path, "", 200)
		if w.Body.Len() != 0 {
			t.Fatal("HEAD emitted body")
		}
	}
	old := d.User.Token
	f.write(t, "POST", "users/1/reset-token", "", 200)
	if _, err := store.Load(context.Background(), 0, &old); err == nil {
		t.Fatal("old token survived reset")
	}
	for _, path := range []string{"users/1/links", "rules", "rules/preview", "settings"} {
		requireStatus(t, request(t, f.h, "GET", "https://example.test/api/v1/"+path, "", nil, nil), 401)
	}
	for _, path := range []string{"rules", "settings"} {
		requireStatus(t, request(t, f.h, "PUT", "https://example.test/api/v1/"+path, `{}`, f.cookie, map[string]string{"Origin": "https://evil.test"}), 403)
		f.call(t, "POST", path, `{}`, 405)
	}
	f.call(t, "GET", "rules/preview?format=base64", "", 400)
	f.call(t, "GET", "rules/preview?format=clash&format=singbox", "", 400)
	f.call(t, "GET", "users/bad/links", "", 400)
	f.call(t, "GET", "users/99/links", "", 404)
	for _, secret := range []string{d.User.UUID, d.User.Password, d.User.Token, "secret-obfs"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("logs leaked credentials")
		}
	}
}
func TestRulesUpdatesOnlyAffectSubscriptions(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	before := f.apply.calls
	s := decodeResult[db.RulesSettings](t, f.call(t, "GET", "rules", "", 200))
	s.Preset = "global"
	s.DNS.FakeIP = true
	s.Source = "sagernet"
	s.BlockAds = true
	b, _ := json.Marshal(s)
	f.call(t, "PUT", "rules", string(b), 200)
	if f.apply.calls != before {
		t.Fatal("rules restarted server")
	}
	got := decodeResult[db.RulesSettings](t, f.call(t, "GET", "rules", "", 200))
	if got.Preset != "global" || !got.DNS.FakeIP {
		t.Fatal(got)
	}
	for _, body := range []string{`{}`, `null`, `{"preset":"global","preset":"bypass_cn"}`, `{"unknown":true}`, strings.Replace(string(b), "https://1.1.1.1/dns-query", "http://1.1.1.1", 1), strings.Replace(string(b), `"custom_proxy":[]`, `"custom_proxy":["example.com,DIRECT"]`, 1)} {
		f.call(t, "PUT", "rules", body, 400)
	}
	got = decodeResult[db.RulesSettings](t, f.call(t, "GET", "rules", "", 200))
	if got.Preset != "global" {
		t.Fatal("invalid rules persisted")
	}
}
func TestSettingsApplyRollbackAndListener(t *testing.T) {
	f, _, commits, cancels := newSubscriptionAPI(t)
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality","options":{"server_name":"www.example.com"}}`, 201)
	before := f.apply.calls
	f.call(t, "PUT", "settings", `{"panel_name":"new","public_host":"node.example","sub_path":"newsub"}`, 200)
	if f.apply.calls != before {
		t.Fatal("client metadata restarted core")
	}
	f.write(t, "PUT", "settings", `{"block_bittorrent":false}`, 200)
	old := decodeResult[PanelSettings](t, f.call(t, "GET", "settings", "", 200))
	f.runner.failCheck = true
	f.call(t, "PUT", "settings", `{"block_bittorrent":true,"web_port":22053,"web_path":"newpanel"}`, 422)
	if got := decodeResult[PanelSettings](t, f.call(t, "GET", "settings", "", 200)); got != old {
		t.Fatal("settings not rolled back")
	}
	if *commits != 0 || *cancels != 1 {
		t.Fatal("failed apply activated listener")
	}
	f.runner.failCheck = false
	f.runner.failRestart = true
	f.call(t, "PUT", "settings", `{"block_bittorrent":true}`, 500)
	if got := decodeResult[PanelSettings](t, f.call(t, "GET", "settings", "", 200)); got != old {
		t.Fatal("restart failed without DB rollback")
	}
	w := f.call(t, "PUT", "settings", `{"web_port":22053,"web_path":"newpanel"}`, 200)
	if *commits != 1 || !strings.Contains(w.Body.String(), "https://node.example:22053/newpanel/") || !strings.Contains(w.Body.String(), `"restart_after":2`) {
		t.Fatal(w.Body.String())
	}
	for _, body := range []string{`{"web_port":443}`, `{"web_port":61000}`} {
		f.call(t, "PUT", "settings", body, 409)
	}
	for _, body := range []string{`null`, `{"web_port":0}`, `{"web_path":"../x"}`, `{"sub_path":"api"}`, `{"sub_path":"newpanel"}`, `{"panel_name":"a\r\nb"}`, `{"panel_tls_mode":"http"}`, `{"public_host":"user@host"}`, `{"block_bittorrent":null}`, `{"web_port":2,"web_port":3}`} {
		f.call(t, "PUT", "settings", body, 400)
	}
}
func TestMissingCertificateFailsClosed(t *testing.T) {
	f, store, _, _ := newSubscriptionAPI(t)
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"hysteria2"}`, 201)
	store.Certificate = nil
	f.call(t, "GET", "users/1/links", "", 409)
	f.call(t, "GET", "rules/preview", "", 409)
}
