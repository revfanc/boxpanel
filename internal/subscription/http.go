package subscription

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/revfanc/boxpanel/internal/db"
)

type bucket struct {
	start time.Time
	count int
}
type Limiter struct {
	mu      sync.Mutex
	entries map[string]bucket
	Now     func() time.Time
}

func (l *Limiter) Allow(ip string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	if l.entries == nil {
		l.entries = map[string]bucket{}
	}
	for key, b := range l.entries {
		if !now.Before(b.start.Add(time.Minute)) {
			delete(l.entries, key)
		}
	}
	b, exists := l.entries[ip]
	if !exists {
		if len(l.entries) >= 10000 {
			return false, 60
		}
		b.start = now
	}
	if b.count >= 30 {
		return false, max(1, int(math.Ceil(b.start.Add(time.Minute).Sub(now).Seconds())))
	}
	b.count++
	l.entries[ip] = b
	return true, 0
}

// Handler sits outside the SPA and management middleware. Invalid tokens,
// inactive users, malformed paths and unknown paths have the same empty 404.
type Handler struct {
	Store     *Store
	Next      http.Handler
	Limiter   *Limiter
	PanelPort int // actual listener, including initial CLI overrides
	Logger    *slog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, err := db.GetSetting(r.Context(), h.Store.DB, db.SubPath)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) == 0 || parts[0] != path {
		h.Next.ServeHTTP(w, r)
		return
	}
	// Count all requests in the subscription namespace, including bad tokens.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if addr, e := netip.ParseAddr(ip); e == nil {
		ip = addr.Unmap().String()
	}
	if ok, retry := h.Limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		w.WriteHeader(429)
		return
	}
	if len(parts) != 2 || parts[1] == "" || len(parts[1]) > 128 || r.URL.RawPath != "" || r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(404)
		return
	}
	d, err := h.Store.Load(r.Context(), 0, &parts[1])
	if err == nil && d.SubPath != path {
		w.WriteHeader(404)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(404)
		return
	}
	if err != nil {
		publicError(w, r, 503, "订阅暂时不可用，请联系管理员检查节点与证书后重新生效。")
		return
	}
	if h.PanelPort > 0 {
		d.WebPort = h.PanelPort
	}
	f, err := Detect(r.URL.Query(), r.UserAgent(), r.Header.Get("Accept"))
	if err != nil {
		publicError(w, r, 400, "订阅格式无效，请使用 singbox、clash 或 base64。")
		return
	}
	var body []byte
	var nonce string
	if f == HTML {
		var random [32]byte
		if _, err = rand.Read(random[:]); err == nil {
			nonce = base64.RawURLEncoding.EncodeToString(random[:])
			body, err = Page(d, nonce, h.Store.now())
		}
	} else {
		body, err = Render(d.Snapshot, f)
	}
	if err != nil {
		publicError(w, r, 503, "订阅生成失败，请联系管理员检查规则和节点配置。")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", d.User.TrafficUp, d.User.TrafficDown, d.User.TrafficLimit, d.User.ExpireAt))
	w.Header().Set("Profile-Update-Interval", "12")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+strings.ReplaceAll(url.QueryEscape(d.PanelName), "+", "%20"))
	contentType := "text/plain; charset=utf-8"
	if f == Singbox {
		contentType = "application/json; charset=utf-8"
	}
	if f == Clash {
		contentType = "application/yaml; charset=utf-8"
	}
	if f == HTML {
		contentType = "text/html; charset=utf-8"
		w.Header().Del("Content-Disposition")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(200)
	if r.Method != "HEAD" {
		if _, err := w.Write(body); err != nil {
			return
		}
	}
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	format := string(f)
	if f == Base64 {
		format = "uri"
	}
	secrets := []string{r.URL.Path, r.URL.EscapedPath(), d.User.Token, d.User.UUID, d.User.Password, r.RemoteAddr, ip}
	for _, n := range d.Nodes {
		if n.Hysteria2 != nil {
			secrets = append(secrets, n.Hysteria2.ObfsPassword)
		}
	}
	logger.InfoContext(r.Context(), "subscription served", "format", format, "user_agent", logUserAgent(r.UserAgent(), secrets), "user_id", d.User.ID)
}

// User-Agent is untrusted. Remove control/formatting characters and known
// credentials before truncating by Unicode character, never in the middle of
// a UTF-8 sequence or before redaction has seen a complete secret.
func logUserAgent(ua string, secrets []string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if !unicode.IsPrint(r) {
				return -1
			}
			return r
		}, s)
	}
	ua = clean(ua)
	for _, secret := range secrets {
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if form = clean(form); form != "" {
				ua = strings.ReplaceAll(ua, form, "[REDACTED]")
			}
		}
	}
	runes := []rune(ua)
	return string(runes[:min(len(runes), 128)])
}

func publicError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write([]byte(message + "\n"))
	}
}
