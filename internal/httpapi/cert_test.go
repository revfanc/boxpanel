package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
)

type certificateStub struct {
	changes, requests int
	err               error
}

func (c *certificateStub) Status(context.Context) (acme.Status, error) {
	return acme.Status{Mode: "acme", Type: "self_signed", LastError: "acme_port_unavailable", Hint: "请释放 TCP 80 后重新申请。"}, nil
}
func (c *certificateStub) Request(context.Context) error { c.requests++; return c.err }
func (c *certificateStub) Changed()                      { c.changes++ }

func TestCertificateRoutesAndSettings(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	cert := &certificateStub{}
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h = New(service, Options{DB: f.conn, Applier: f.apply, Certificates: cert})
	for _, method := range []string{"GET", "HEAD"} {
		w := f.call(t, method, "cert", "", 200)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable certificate status")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	for _, path := range []string{"cert", "cert/issue"} {
		method := "GET"
		if path == "cert/issue" {
			method = "POST"
		}
		requireStatus(t, request(t, f.h, method, "https://example.test/api/v1/"+path, "", nil, nil), 401)
	}
	requireStatus(t, request(t, f.h, "POST", "https://example.test/api/v1/cert/issue", "", f.cookie, map[string]string{"Origin": "https://evil.test"}), 403)
	f.call(t, "POST", "cert/issue", `{"host":"attacker.test"}`, 400)
	f.call(t, "GET", "cert/issue", "", 405)
	f.call(t, "POST", "cert/issue", "", 202)
	cert.err = acme.ErrBusy
	f.call(t, "POST", "cert/issue", "", 409)
	if cert.requests != 2 {
		t.Fatal(cert.requests)
	}
	f.call(t, "PUT", "settings", `{"panel_tls_mode":"acme"}`, 200)
	mode, _ := db.GetSetting(context.Background(), f.conn, db.PanelTLSMode)
	if mode != "acme" || cert.changes != 1 {
		t.Fatal(mode, cert.changes)
	}
	f.call(t, "PUT", "settings", `{"public_host":"new.example.com"}`, 200)
	if cert.changes != 2 {
		t.Fatal("host change did not notify certificate manager")
	}
	f.runner.failCheck = true
	f.call(t, "PUT", "settings", `{"panel_tls_mode":"self_signed","block_bittorrent":false}`, 422)
	mode, _ = db.GetSetting(context.Background(), f.conn, db.PanelTLSMode)
	if mode != "acme" || cert.changes != 2 {
		t.Fatal("failed Apply changed TLS policy")
	}
	f.runner.failCheck = false
	f.call(t, "PUT", "settings", `{"panel_tls_mode":"self_signed"}`, 200)
	if cert.changes != 3 {
		t.Fatal(cert.changes)
	}
	f.call(t, "PUT", "settings", `{"panel_tls_mode":"http"}`, 400)
	f.h = New(service, Options{Dev: true, DB: f.conn, Certificates: cert})
	f.call(t, "POST", "cert/issue", "", 409)
	f.call(t, "PUT", "settings", `{"panel_tls_mode":"acme"}`, 400)
}

func TestSuggestFailureRetainsPortAndManualTarget(t *testing.T) {
	f := newResources(t, true)
	service, err := auth.New(f.conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h = New(service, Options{DB: f.conn, Dev: true, Applier: f.apply, PanelPort: 2053, RealityProbe: func(context.Context, string, int) reality.Target { return reality.Target{OK: false} }})
	w := f.call(t, http.MethodPost, "inbounds/suggest", `{"protocol":"vless_reality"}`, 200)
	var suggestion struct {
		Port    int
		Hint    string
		Options struct {
			ServerName    string `json:"server_name"`
			HandshakePort int    `json:"handshake_port"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &suggestion); err != nil {
		t.Fatal(err)
	}
	if suggestion.Port != 443 || suggestion.Hint == "" || suggestion.Options.ServerName != "" || suggestion.Options.HandshakePort != 443 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private_key") || strings.Contains(w.Body.String(), "public_key") {
		t.Fatal("suggest leaked keys")
	}
	f.call(t, "POST", "inbounds", `{"protocol":"vless_reality","port":443,"options":{"server_name":"example.com"}}`, 201)
}
