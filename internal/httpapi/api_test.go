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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/spa"
	"testing/fstest"
)

func testAPI(t *testing.T, dev bool, systems ...System) (http.Handler, *sql.DB, *bytes.Buffer) {
	t.Helper()
	conn, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO admins (username,password_hash,created_at) VALUES ('admin',?,0)", hash); err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	base := "panel"
	if dev {
		base = ""
	}
	var system System
	if len(systems) > 0 {
		system = systems[0]
	}
	api := New(service, Options{WebPath: base, Dev: dev, Logger: slog.New(slog.NewTextHandler(&logs, nil)), System: system})
	assets := fstest.MapFS{"index.html": {Data: []byte("<html><head><!--BOXPANEL_INJECT--></head></html>")}}
	handler, err := spa.New(assets, spa.Options{WebPath: base, API: api})
	if err != nil {
		t.Fatal(err)
	}
	return handler, conn, &logs
}

func request(t *testing.T, handler http.Handler, method, target, body string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:12345"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API response is cacheable")
	}
	if want >= 400 {
		var envelope struct {
			Error struct{ Code, Message, Hint string }
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error.Code == "" || envelope.Error.Message == "" || envelope.Error.Hint == "" {
			t.Fatalf("invalid error envelope: %s", w.Body.String())
		}
	}
}

func TestAuthAPI(t *testing.T) {
	h, _, logs := testAPI(t, false)
	base := "https://example.test/panel/api/v1/auth/"
	headers := map[string]string{"Origin": "https://example.test", "Sec-Fetch-Site": "same-origin"}
	login := func(username, password string) *http.Cookie {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		w := request(t, h, "POST", base+"login", string(body), nil, headers)
		requireStatus(t, w, 200)
		var value map[string]any
		if json.Unmarshal(w.Body.Bytes(), &value) != nil || len(value) != 1 || value["username"] != username {
			t.Fatal("login must expose only username")
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing session cookie")
		}
		cookie := cookies[0]
		if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/panel/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 604800 || time.Until(cookie.Expires) < auth.SessionLifetime-time.Minute {
			t.Fatal("invalid production cookie attributes")
		}
		return cookie
	}
	requireStatus(t, request(t, h, "GET", base+"me", "", nil, nil), 401)
	cookie := login("admin", "test-password")
	w := request(t, h, "GET", base+"me?token=secret-query", "", cookie, nil)
	requireStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"username":"admin"}` {
		t.Fatal("incorrect me response")
	}
	w = request(t, h, "POST", base+"logout", "", cookie, headers)
	requireStatus(t, w, 204)
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge != -1 || c[0].Path != "/panel/" || !c[0].Secure || c[0].Value != "" {
		t.Fatal("logout must expire the same cookie")
	}
	requireStatus(t, request(t, h, "GET", base+"me", "", cookie, nil), 401)
	cookie = login("admin", "test-password")
	requireStatus(t, request(t, h, "PUT", base+"password", `{"old_password":"wrong","new_password":"new-password"}`, cookie, headers), 401)
	requireStatus(t, request(t, h, "PUT", base+"password", `{"old_password":"test-password","new_password":"short"}`, cookie, headers), 400)
	requireStatus(t, request(t, h, "PUT", base+"password", `{"old_password":"test-password","new_password":"new-password"}`, cookie, headers), 204)
	requireStatus(t, request(t, h, "GET", base+"me", "", cookie, nil), 401)
	cookie = login("admin", "new-password")
	requireStatus(t, request(t, h, "PUT", base+"username", `{"username":"renamed"}`, cookie, headers), 204)
	requireStatus(t, request(t, h, "GET", base+"me", "", cookie, nil), 401)
	cookie = login("renamed", "new-password")
	requireStatus(t, request(t, h, "GET", base+"me", "", cookie, nil), 200)
	for _, secret := range []string{"test-password", "new-password", cookie.Value, "secret-query", "Cookie", "password_hash"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("logs contain credential/request data")
		}
	}
}

func TestCSRF(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers map[string]string
		allowed bool
	}{
		{"CLI", nil, true},
		{"matching origin", map[string]string{"Origin": "https://example.test"}, true},
		{"same origin metadata", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"direct navigation", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"cross origin", map[string]string{"Origin": "https://evil.test"}, false},
		{"wrong scheme", map[string]string{"Origin": "http://example.test"}, false},
		{"wrong port", map[string]string{"Origin": "https://example.test:1234"}, false},
		{"null origin", map[string]string{"Origin": "null"}, false},
		{"empty origin", map[string]string{"Origin": ""}, false},
		{"origin path", map[string]string{"Origin": "https://example.test/path"}, false},
		{"origin userinfo", map[string]string{"Origin": "https://user@example.test"}, false},
		{"cross site", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same site sibling", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"conflicting headers", map[string]string{"Origin": "https://example.test", "Sec-Fetch-Site": "cross-site"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := csrf(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			w := request(t, h, "POST", "https://example.test/api/v1/auth/login", "", nil, tt.headers)
			want := 403
			if tt.allowed {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("status=%d want=%d", w.Code, want)
			}
		})
	}
}

func TestAPIValidationRateLimitAndErrors(t *testing.T) {
	h, conn, logs := testAPI(t, false)
	base := "https://example.test/panel/api/v1/"
	for _, body := range []string{"{", `null`, `[]`, `{"username":"admin","password":"test-password","unknown":true}`, `{"username":"admin","password":"test-password"} {}`, `{"username":"bad/name","password":"test-password"}`, strings.Repeat("x", 9000)} {
		requireStatus(t, request(t, h, "POST", base+"auth/login", body, nil, nil), 400)
	}
	requireStatus(t, request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, map[string]string{"Content-Type": "text/plain"}), 400)
	requireStatus(t, request(t, h, "GET", base+"missing-sensitive-token", "", nil, nil), 404)
	requireStatus(t, request(t, h, "GET", base+"auth//login", "", nil, nil), 404)
	requireStatus(t, request(t, h, "DELETE", base+"auth/login", "", nil, nil), 405)
	requireStatus(t, request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, map[string]string{"Origin": "https://evil.test"}), 403)
	for i := 0; i < 5; i++ {
		w := request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"wrong"}`, nil, map[string]string{"X-Forwarded-For": "203.0.113." + string(rune('1'+i)), "X-Real-IP": "198.51.100.1"})
		want := 401
		if i == 4 {
			want = 429
			if w.Header().Get("Retry-After") != "900" {
				t.Fatal("missing lock duration")
			}
		}
		requireStatus(t, w, want)
	}
	requireStatus(t, request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil), 429)
	if strings.Contains(logs.String(), "missing-sensitive-token") {
		t.Fatal("unknown raw path leaked into logs")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.RandomToken(32)
	requireStatus(t, request(t, h, "GET", base+"auth/me", "", &http.Cookie{Name: CookieName, Value: token}, nil), 500)
}

func TestDevCookieAndViteOrigin(t *testing.T) {
	h, _, _ := testAPI(t, true)
	w := request(t, h, "POST", "http://localhost:5173/api/v1/auth/login", `{"username":"admin","password":"test-password"}`, nil, map[string]string{"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-origin"})
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("invalid development cookie")
	}
}

func TestPanicRecoveryDiscardsPartialResponse(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "secret-cookie")
		_, _ = io.WriteString(w, "secret-partial-body")
		panic("secret-panic-value")
	}), logger, nil)
	w := request(t, h, "GET", "https://example.test/secret-url?token=secret-query", "", nil, nil)
	requireStatus(t, w, 500)
	if w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "secret") || strings.Contains(logs.String(), "secret") {
		t.Fatal("panic leaked sensitive data")
	}
	if !strings.Contains(logs.String(), "status=500") {
		t.Fatal("panic not logged as failure")
	}
}
