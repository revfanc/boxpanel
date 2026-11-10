// Package httpapi exposes the panel's JSON API using net/http routing.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/subscription"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

const CookieName = "boxpanel_session"

type Options struct {
	Certificates   Certificates
	WebPath        string
	Dev            bool
	Logger         *slog.Logger
	System         System
	Singbox        Singbox
	DB             *sql.DB
	Applier        Applier
	RealityProbe   reality.Probe
	ListeningPorts func(context.Context) (map[sysinfo.Socket]bool, error)
	LookupIP       func(context.Context, string) ([]netip.Addr, error)
	DialContext    func(context.Context, string, string) (net.Conn, error)
	PanelPort      int // effective listener port, including CLI overrides
	Subscriptions  *subscription.Store
	// PreparePanel reserves the next listener before persistence. The returned
	// commit schedules activation after two seconds; cancel releases it on error.
	PreparePanel func(context.Context, PanelSettings) (commit func(), cancel func(), err error)
}

type api struct {
	auth       *auth.Service
	opts       Options
	cookiePath string
	settingsMu sync.Mutex
}

type adminKey struct{}

func New(service *auth.Service, opts Options) http.Handler {
	if opts.RealityProbe == nil {
		opts.RealityProbe = reality.NewProbe(reality.ProbeOptions{})
	}
	if opts.ListeningPorts == nil {
		opts.ListeningPorts = func(ctx context.Context) (map[sysinfo.Socket]bool, error) {
			if opts.Dev || runtime.GOOS != "linux" {
				return map[sysinfo.Socket]bool{}, nil
			}
			return sysinfo.ListeningPorts(ctx, host.Native{})
		}
	}
	if opts.System == nil {
		opts.System = sysinfo.New(sysinfo.Options{Dev: opts.Dev})
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.LookupIP == nil {
		opts.LookupIP = func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		}
	}
	if opts.DialContext == nil {
		opts.DialContext = (&net.Dialer{}).DialContext
	}
	a := &api{auth: service, opts: opts, cookiePath: "/"}
	if opts.WebPath != "" {
		a.cookiePath = "/" + opts.WebPath + "/"
	}
	mux := http.NewServeMux()
	routes := map[string]string{}
	handlers := map[string]map[string]http.Handler{}
	register := func(method, path string, handler http.HandlerFunc, protected bool) {
		if handlers[path] == nil {
			handlers[path] = map[string]http.Handler{}
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				if h := handlers[path][r.Method]; h != nil {
					h.ServeHTTP(w, r)
					return
				}
				w.Header().Set("Allow", routes[path])
				writeError(w, 405, "method_not_allowed", "请求方法不支持", "请使用接口文档指定的请求方法。")
			})
		} else {
			routes[path] += ", "
		}
		routes[path] += method
		var h http.Handler = handler
		if protected {
			h = a.authenticate(h)
		}
		handlers[path][method] = h
		if method == "GET" {
			routes[path] += ", HEAD"
			handlers[path]["HEAD"] = h
		}
	}
	register("POST", "/api/v1/auth/login", a.login, false)
	register("POST", "/api/v1/auth/logout", a.logout, true)
	register("GET", "/api/v1/auth/me", a.me, true)
	register("PUT", "/api/v1/auth/password", a.password, true)
	register("PUT", "/api/v1/auth/username", a.username, true)
	register("GET", "/api/v1/system/check", a.systemCheck, true)
	register("GET", "/api/v1/system/status", a.systemStatus, true)
	register("GET", "/api/v1/system/public-ip", a.systemPublicIP, true)
	register("POST", "/api/v1/system/bbr", a.systemBBR, true)
	register("POST", "/api/v1/singbox/restart", a.singboxRestart, true)
	register("GET", "/api/v1/singbox/logs", a.singboxLogs, true)
	register("GET", "/api/v1/singbox/config", a.singboxConfig, true)
	register("GET", "/api/v1/setup/status", a.setupGet, true)
	register("PUT", "/api/v1/setup/status", a.setupPut, true)
	register("POST", "/api/v1/system/port-probe", a.portProbe, true)
	register("GET", "/api/v1/cert", a.certGet, true)
	register("POST", "/api/v1/cert/issue", a.certIssue, true)
	a.registerResources(register)
	a.registerSubscriptions(register)
	mux.HandleFunc("/", notFound)
	return middleware(csrf(mux), opts.Logger, routes)
}

func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, err := a.auth.Authenticate(r.Context(), sessionToken(r))
		if err != nil {
			a.fail(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey{}, admin)))
	})
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	var input struct{ Username, Password string }
	if !decode(w, r, &input) {
		return
	}
	if !auth.ValidUsername(input.Username) || input.Password == "" || len(input.Password) > 1024 {
		writeError(w, 400, "invalid_argument", "账号或密码格式不正确", "用户名限 1–32 位字母、数字、下划线或连字符；密码不能为空且不超过 1024 字节。")
		return
	}
	session, err := a.auth.Login(r.Context(), peerIP(r.RemoteAddr), input.Username, input.Password, sessionToken(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	a.setCookie(w, session.Token, session.ExpiresAt, int(auth.SessionLifetime.Seconds()))
	writeJSON(w, 200, session.Admin)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		a.fail(w, err)
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, r.Context().Value(adminKey{}).(auth.Admin))
}

func (a *api) password(w http.ResponseWriter, r *http.Request) {
	var input struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !auth.ValidPassword(input.NewPassword) || input.OldPassword == "" || len(input.OldPassword) > 1024 {
		writeError(w, 400, "invalid_argument", "密码格式不正确", "请提供原密码，新密码至少 10 个字符；每个密码不得超过 1024 字节。")
		return
	}
	if err := a.auth.ChangeCredentials(r.Context(), sessionToken(r), "", input.OldPassword, input.NewPassword); err != nil {
		a.fail(w, err)
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) username(w http.ResponseWriter, r *http.Request) {
	var input struct{ Username string }
	if !decode(w, r, &input) {
		return
	}
	if !auth.ValidUsername(input.Username) {
		writeError(w, 400, "invalid_argument", "用户名格式不正确", "请使用 1–32 位字母、数字、下划线或连字符。")
		return
	}
	if err := a.auth.ChangeCredentials(r.Context(), sessionToken(r), input.Username, "", ""); err != nil {
		a.fail(w, err)
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) setCookie(w http.ResponseWriter, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: a.cookiePath,
		HttpOnly: true, Secure: !a.opts.Dev, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}

func (a *api) clearCookie(w http.ResponseWriter) {
	a.setCookie(w, "", time.Unix(1, 0), -1)
}

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func peerIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	// Never trust X-Forwarded-For / X-Real-IP supplied by clients.
	return host
}

func (a *api) fail(w http.ResponseWriter, err error) {
	var rate *auth.RateLimitError
	switch {
	case errors.As(err, &rate):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(rate.RetryAfter.Seconds()+0.999))))
		writeError(w, 429, "rate_limited", "登录失败次数过多", "请在 Retry-After 指示的秒数后重试，并确认账号密码。")
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "auth_busy", "认证服务正忙", "请稍等一秒后重试。")
	case errors.Is(err, auth.ErrCredentials):
		writeError(w, 401, "invalid_credentials", "用户名或密码不正确", "请检查输入；忘记密码时可在服务器执行 boxpanel reset-admin。")
	case errors.Is(err, auth.ErrSession):
		writeError(w, 401, "unauthorized", "尚未登录或会话已过期", "请重新登录面板。")
	case errors.Is(err, auth.ErrUsername):
		writeError(w, 409, "username_exists", "用户名已被使用", "请换一个用户名后重试。")
	default:
		// Do not expose SQL errors, arguments, hashes or credential values.
		a.opts.Logger.Error("authentication storage operation failed; check database availability and permissions")
		writeError(w, 500, "internal_error", "认证服务暂时不可用", "请检查数据目录权限及数据库状态，再重试。")
	}
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 400, "invalid_argument", "请求必须为 JSON", "请设置 Content-Type: application/json 并检查请求内容。")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, 400, "invalid_argument", "请求内容无效", "请检查字段和 JSON 格式；请求内容不能超过 8 KiB。")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_argument", "请求包含多余内容", "请只发送一个 JSON 对象。")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message, hint string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "hint": hint}})
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, 404, "not_found", "接口不存在", "请检查请求地址，确认使用当前版本支持的 API。")
}
