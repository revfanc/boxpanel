package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		site := r.Header.Get("Sec-Fetch-Site")
		allowed := site == "" || site == "same-origin" || site == "none"
		if origins, present := r.Header["Origin"]; present {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			origin, err := url.Parse(r.Header.Get("Origin"))
			allowed = allowed && len(origins) == 1 && err == nil && origin.Scheme == scheme &&
				strings.EqualFold(origin.Host, r.Host) && origin.User == nil && origin.Path == "" &&
				origin.RawQuery == "" && !origin.ForceQuery && origin.Fragment == ""
		}
		if !allowed {
			writeError(w, 403, "csrf_rejected", "已拒绝跨站请求", "请从面板本身的地址打开页面，再执行此操作。")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Auth responses are small JSON documents. Buffer them so a panic cannot leak a
// partial success, cookie or sensitive panic value before the unified 500 reply.
type response struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *response) Header() http.Header { return w.header }
func (w *response) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *response) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

func middleware(next http.Handler, logger *slog.Logger, routes map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		buffer := &response{header: make(http.Header)}
		defer func() {
			if recover() != nil {
				buffer = &response{header: make(http.Header)}
				writeError(buffer, 500, "internal_error", "服务发生内部错误", "请重试；若仍失败，请检查服务日志并重启面板。")
				logger.Error("API panic recovered; retry or restart the panel")
			}
			buffer.WriteHeader(http.StatusOK)
			for key, values := range buffer.header {
				w.Header()[key] = values
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(buffer.status)
			if r.Method != "HEAD" {
				_, _ = w.Write(buffer.body.Bytes())
			}
			route := "unmatched"
			if _, ok := routes[r.Pattern]; ok {
				route = r.Pattern
			}
			// Raw URL/query/headers/body/panic values are intentionally omitted.
			logger.Info("api request", "route", route, "status", buffer.status, "duration", time.Since(start))
		}()
		if path.Clean(r.URL.Path) != r.URL.Path {
			notFound(buffer, r)
			return
		}
		next.ServeHTTP(buffer, r)
	})
}
