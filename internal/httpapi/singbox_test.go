package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type fakeSingbox struct {
	calls, lines int
	err          error
}

func (s *fakeSingbox) Restart(context.Context) error { s.calls++; return s.err }
func (s *fakeSingbox) Config(context.Context) (json.RawMessage, error) {
	s.calls++
	return json.RawMessage(`{"secret":"[REDACTED]"}`), s.err
}
func (s *fakeSingbox) Logs(_ context.Context, lines int) ([]string, error) {
	s.calls++
	s.lines = lines
	return []string{"bind failed"}, s.err
}

func TestSingboxRoutesProtectionContractAndErrors(t *testing.T) {
	_, conn, logs := testAPI(t, false)
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeSingbox{}
	h := New(service, Options{Singbox: core, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	base := "https://example.test/api/v1/"
	w := request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	for _, route := range []struct {
		method, path string
		status       int
	}{{"POST", "restart", 204}, {"GET", "logs", 200}, {"GET", "config", 200}} {
		before := core.calls
		requireStatus(t, request(t, h, route.method, base+"singbox/"+route.path, "", nil, nil), 401)
		if before != core.calls {
			t.Fatal("unauthenticated core call")
		}
		w = request(t, h, route.method, base+"singbox/"+route.path, "", cookie, nil)
		requireStatus(t, w, route.status)
		requireStatus(t, request(t, h, "DELETE", base+"singbox/"+route.path, "", cookie, nil), 405)
		if route.method == "GET" {
			w = request(t, h, "HEAD", base+"singbox/"+route.path, "", cookie, nil)
			requireStatus(t, w, 200)
			if w.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		}
	}
	before := core.calls
	requireStatus(t, request(t, h, "POST", base+"singbox/restart", "", cookie, map[string]string{"Origin": "https://evil.test"}), 403)
	if before != core.calls {
		t.Fatal("CSRF reached restart")
	}
	for _, query := range []string{"lines=0", "lines=1001", "lines=-1", "lines=no", "lines=", "lines=1&lines=2"} {
		requireStatus(t, request(t, h, "GET", base+"singbox/logs?"+query, "", cookie, nil), 400)
	}
	for _, query := range []string{"", "?lines=30", "?lines=1000"} {
		requireStatus(t, request(t, h, "GET", base+"singbox/logs"+query, "", cookie, nil), 200)
	}
	if core.lines != 1000 {
		t.Fatal("lines not propagated")
	}
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{singbox.ErrUnsupported, 409, "system_unsupported"}, {singbox.ErrNoConfig, 404, "singbox_config_missing"},
		{&singbox.ApplyError{Code: "singbox_check_failed", Detail: "redacted check failure"}, 422, "singbox_check_failed"},
		{&singbox.ApplyError{Code: "singbox_restart_failed", Detail: "redacted journal"}, 500, "singbox_restart_failed"},
		{&singbox.ApplyError{Code: "singbox_restart_failed", RollbackFailed: true}, 500, "singbox_rollback_failed"},
		{errors.New("private-command-output"), 500, "internal_error"},
	} {
		core.err = tt.err
		w = request(t, h, "POST", base+"singbox/restart", "", cookie, nil)
		requireStatus(t, w, tt.status)
		if !strings.Contains(w.Body.String(), tt.code) || strings.Contains(w.Body.String(), "private-command-output") {
			t.Fatal(w.Body.String())
		}
	}
	if strings.Contains(logs.String(), "private-command-output") {
		t.Fatal("raw error logged")
	}
	for _, path := range []string{"singbox/upgrade", "singbox/versions", "inbounds", "users"} {
		requireStatus(t, request(t, h, "POST", base+path, "", cookie, nil), 404)
	}
}
