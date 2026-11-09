package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type fakeSystem struct {
	calls int
	err   error
}

func (s *fakeSystem) Check(context.Context) (sysinfo.Check, error) {
	s.calls++
	return sysinfo.Check{OS: "debian 12", Arch: "arm64", Items: []sysinfo.Item{{ID: "bbr", Level: "warn", Title: "未开启 BBR", Hint: "请开启 BBR。", Action: "enable_bbr"}}}, s.err
}
func (s *fakeSystem) Status(context.Context) (sysinfo.Status, error) {
	s.calls++
	return sysinfo.Status{Supported: true, CPU: 25, Mem: sysinfo.Usage{Used: 10, Total: 20}, Disk: sysinfo.Usage{Used: 30, Total: 40}, Uptime: 100, Net: sysinfo.Rates{RX: 1000, TX: 2000}}, s.err
}
func (s *fakeSystem) PublicIP(context.Context) (sysinfo.PublicIPs, error) {
	s.calls++
	return sysinfo.PublicIPs{IPv4: "203.0.113.1", IPv6: "2001:db8::1"}, s.err
}
func (s *fakeSystem) EnableBBR(context.Context) (sysinfo.BBR, error) {
	s.calls++
	return sysinfo.BBR{Enabled: true, Available: true}, s.err
}

func TestSystemAPIProtectionAndContract(t *testing.T) {
	system := &fakeSystem{}
	h, _, logs := testAPI(t, false, system)
	base := "https://example.test/panel/api/v1/"
	w := request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	for _, route := range []struct {
		method, path string
		keys         []string
	}{
		{"GET", "check", []string{"os", "arch", "virt", "ipv4", "ipv6", "bbr", "firewall", "singbox", "ports", "items"}},
		{"GET", "status", []string{"supported", "cpu", "mem", "disk", "uptime", "net", "singbox"}},
		{"GET", "public-ip", []string{"ipv4", "ipv6"}},
		{"POST", "bbr", []string{"enabled", "available"}},
	} {
		calls := system.calls
		requireStatus(t, request(t, h, route.method, base+"system/"+route.path, "", nil, nil), 401)
		if system.calls != calls {
			t.Fatal("unauthenticated request reached system")
		}
		w = request(t, h, route.method, base+"system/"+route.path+"?token=secret-query", "", cookie, nil)
		requireStatus(t, w, 200)
		var data map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		for _, key := range route.keys {
			if _, ok := data[key]; !ok {
				t.Errorf("%s missing %s", route.path, key)
			}
		}
		if route.method == "GET" {
			w = request(t, h, "HEAD", base+"system/"+route.path, "", cookie, nil)
			requireStatus(t, w, 200)
			if w.Body.Len() != 0 {
				t.Fatal("HEAD returned body")
			}
		}
		w = request(t, h, "DELETE", base+"system/"+route.path, "", cookie, nil)
		requireStatus(t, w, 405)
		if !strings.Contains(w.Header().Get("Allow"), route.method) {
			t.Fatal("missing allowed method")
		}
	}
	calls := system.calls
	requireStatus(t, request(t, h, "POST", base+"system/bbr", "", cookie, map[string]string{"Origin": "https://evil.test"}), 403)
	if system.calls != calls {
		t.Fatal("CSRF reached system mutation")
	}
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{{sysinfo.ErrUnsupported, 409, "system_unsupported"}, {sysinfo.ErrBBRUnavailable, 409, "bbr_unavailable"}, {errors.New("secret-command-output"), 500, "internal_error"}} {
		system.err = tt.err
		w = request(t, h, "POST", base+"system/bbr", "", cookie, nil)
		requireStatus(t, w, tt.status)
		if !strings.Contains(w.Body.String(), tt.code) || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("unexpected response %s", w.Body.String())
		}
	}
	for _, path := range []string{"check", "status", "public-ip"} {
		requireStatus(t, request(t, h, "GET", base+"system/"+path, "", cookie, nil), 500)
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("logs leaked command output or query")
	}

}

func TestDevSystemAPI(t *testing.T) {
	h, _, _ := testAPI(t, true)
	base := "http://localhost/api/v1/"
	w := request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	w = request(t, h, "GET", base+"system/status", "", cookie, nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"supported":false`) {
		t.Fatal("dev reports real telemetry")
	}
	w = request(t, h, "GET", base+"system/check", "", cookie, nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"id":"platform"`) {
		t.Fatal("missing dev explanation")
	}
	for _, route := range []struct{ method, path string }{{http.MethodPost, "bbr"}, {http.MethodGet, "public-ip"}} {
		requireStatus(t, request(t, h, route.method, base+"system/"+route.path, "", cookie, nil), 409)
	}
}
