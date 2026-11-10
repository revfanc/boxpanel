package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestSubscriptionSuccessLog(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		for _, tt := range []struct{ query, ua, accept, format string }{
			{"", "sing-box/1.14", "", "singbox"},
			{"", "Clash/1.0", "", "clash"},
			{"", "Stash/3.6", "", "clash"},
			{"", "Shadowrocket", "", "uri"},
			{"?format=base64", "mihomo/1.0", "", "uri"},
			{"?format=singbox", "Clash", "", "singbox"},
			{"", "Mozilla/5.0", "text/html", "html"},
		} {
			t.Run(method+"/"+tt.ua+tt.query, func(t *testing.T) {
				h, _ := publicFixture(t)
				var logs bytes.Buffer
				h.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
				w := publicRequest(h, method, "/subscribe123/token-that-must-not-leak"+tt.query, map[string]string{"User-Agent": tt.ua, "Accept": tt.accept, "Authorization": "Bearer auth-secret", "Cookie": "secret-cookie", "X-Forwarded-For": "198.51.100.99"})
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				var record map[string]any
				if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
					t.Fatal("expected exactly one JSON log", err)
				}
				if record["level"] != "INFO" || record["msg"] != "subscription served" || record["format"] != tt.format || record["user_agent"] != tt.ua || record["user_id"] != float64(1) || len(record) != 6 {
					t.Fatalf("unexpected success log fields: %v", record)
				}
				for _, secret := range []string{"token-that-must-not-leak", "subscribe123", "192.0.2.1", "198.51.100.99", "evil-host", "auth-secret", "secret-cookie", "aaaaaaaa-", "test-password", "NEVER-IN-CLIENT"} {
					if strings.Contains(logs.String(), secret) {
						t.Fatalf("log leaked %s", secret)
					}
				}
			})
		}
	}
}

func TestSubscriptionLogSanitizesUserAgent(t *testing.T) {
	h, _ := publicFixture(t)
	var logs bytes.Buffer
	h.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ua := "Clash\r\n\x00\t\x7f\u0085\u2028\u202e" + strings.Repeat("界", 150)
	w := publicRequest(h, "GET", "/subscribe123/token-that-must-not-leak", map[string]string{"User-Agent": ua})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var record struct {
		UserAgent string `json:"user_agent"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(record.UserAgent) || utf8.RuneCountInString(record.UserAgent) != 128 || record.UserAgent != "Clash"+strings.Repeat("界", 123) {
		t.Fatalf("UA not cleaned/truncated by character: %q", record.UserAgent)
	}
	logs.Reset()
	ua = "Clash /subscribe123/token-that-must-not-leak token-that-must-not-leak test-password aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa 192.0.2.1"
	w = publicRequest(h, "GET", "/subscribe123/token-that-must-not-leak", map[string]string{"User-Agent": ua})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, secret := range []string{"subscribe123", "token-that-must-not-leak", "test-password", "aaaaaaaa-", "192.0.2.1"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("reflected UA leaked %s", secret)
		}
	}
	secret := "sensitive:/?#value"
	for _, form := range []string{secret, url.PathEscape(secret), url.QueryEscape(secret)} {
		got := logUserAgent(strings.Repeat("界", 120)+form, []string{secret})
		if got != strings.Repeat("界", 120)+"[REDACTE" {
			t.Fatal("must redact before truncating")
		}
	}
	for _, ua := range []string{"", "abc\xffdef", "\r\n\t", "Clash/版本"} {
		got := logUserAgent(ua, nil)
		if !utf8.ValidString(got) || strings.ContainsFunc(got, func(r rune) bool { return !unicode.IsPrint(r) }) {
			t.Fatal("unsafe log UA")
		}
	}
}

func TestSubscriptionFailuresDoNotLogSuccess(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		status     int
		prepare    func(*Handler, *Store)
	}{
		{"bad_token", "/subscribe123/wrong", 404, nil},
		{"other_path", "/missing", 404, nil},
		{"bad_format", "/subscribe123/token-that-must-not-leak?format=bad", 400, nil},
		{"inactive", "/subscribe123/token-that-must-not-leak", 404, func(_ *Handler, s *Store) {
			if _, err := s.DB.Exec("UPDATE users SET enabled=0"); err != nil {
				t.Fatal(err)
			}
		}},
		{"rate_limit", "/subscribe123/token-that-must-not-leak", 429, func(h *Handler, _ *Store) {
			for range 30 {
				h.Limiter.Allow("192.0.2.1")
			}
		}},
		{"render_error", "/subscribe123/token-that-must-not-leak?format=clash", 503, func(_ *Handler, s *Store) {
			r := fixture(t).Rules
			r.Preset = "invalid"
			if err := db.SetSetting(context.Background(), s.DB, db.Rules, r); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s := publicFixture(t)
			var logs bytes.Buffer
			h.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if tt.prepare != nil {
				tt.prepare(h, s)
			}
			w := publicRequest(h, "GET", tt.path, map[string]string{"User-Agent": "secret-UA"})
			if w.Code != tt.status || logs.Len() != 0 {
				t.Fatalf("status=%d logs=%s", w.Code, logs.String())
			}
		})
	}
	h, _ := publicFixture(t)
	var logs bytes.Buffer
	h.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	h.ServeHTTP(failedSubscriptionWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "/subscribe123/token-that-must-not-leak", nil))
	if logs.Len() != 0 {
		t.Fatal("failed write logged as success")
	}
}

type failedSubscriptionWriter struct{ http.ResponseWriter }

func (failedSubscriptionWriter) Write([]byte) (int, error) { return 0, errors.New("disconnected") }
