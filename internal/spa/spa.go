// Package spa serves an embedded frontend under a runtime-selected base path.
package spa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const injectionMarker = "<!--BOXPANEL_INJECT-->"

type Options struct {
	WebPath string
	Version string
	// API receives paths with the panel base removed, e.g. /api/v1/users.
	// It must return JSON for unmatched routes. If nil, all API requests get
	// the default JSON 404 without falling back to the frontend.
	API http.Handler
}

type handler struct {
	assets fs.FS
	base   string
	index  []byte
	api    http.Handler
}

// ValidateWebPath accepts one lowercase alphanumeric segment, or an empty
// string to mount at the root (development mode).
func ValidateWebPath(webPath string) error {
	if strings.Trim(webPath, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return fmt.Errorf("invalid web_path: use lowercase letters and digits without slashes, or an empty string")
	}
	return nil
}

// New prepares the injected index once. assets must be rooted at dist.
func New(assets fs.FS, opts Options) (http.Handler, error) {
	if err := ValidateWebPath(opts.WebPath); err != nil {
		return nil, fmt.Errorf("create SPA handler: %w", err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read frontend index (run make build, then restart the rebuilt binary): %w", err)
	}
	if bytes.Count(index, []byte(injectionMarker)) != 1 {
		return nil, fmt.Errorf("prepare frontend index: expected one %s marker; restore web/index.html and run make build", injectionMarker)
	}
	base := ""
	if opts.WebPath != "" {
		base = "/" + opts.WebPath
	}
	// encoding/json escapes HTML-sensitive characters, including a closing
	// script tag in a version supplied through build flags.
	runtime, err := json.Marshal(struct {
		Base    string `json:"base"`
		Version string `json:"version"`
	}{Base: base, Version: opts.Version})
	if err != nil {
		return nil, fmt.Errorf("encode frontend runtime: %w", err)
	}
	injection := `<base href="` + html.EscapeString(base+"/") + `">` + "\n" +
		`<script>window.__BOXPANEL__=` + string(runtime) + `</script>`
	api := opts.API
	if api == nil {
		api = http.HandlerFunc(apiNotFound)
	}
	if base != "" {
		api = http.StripPrefix(base, api)
	}
	return &handler{
		assets: assets,
		base:   base,
		index:  bytes.Replace(index, []byte(injectionMarker), []byte(injection), 1),
		api:    api,
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.base && !strings.HasPrefix(r.URL.Path, h.base+"/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, h.base+"/")
	if relative == "api" || strings.HasPrefix(relative, "api/") {
		h.api.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.base != "" && r.URL.Path == h.base {
		target := h.base + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	name := strings.TrimSuffix(relative, "/")
	if relative != "" && !fs.ValidPath(name) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Always inject the entry point, including explicit /index.html requests.
	if name == "" || relative == "index.html" {
		h.serveIndex(w, r)
		return
	}
	info, err := fs.Stat(h.assets, name)
	if err == nil && !info.IsDir() && !strings.HasSuffix(relative, "/") {
		content, err := fs.ReadFile(h.assets, name)
		if err != nil {
			http.Error(w, "无法读取页面资源，请重试或重新构建前端后重启。", http.StatusInternalServerError)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(content))
		return
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "无法读取页面资源，请重试或重新构建前端后重启。", http.StatusInternalServerError)
		return
	}
	if path.Ext(name) != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.serveIndex(w, r)
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func apiNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"接口不存在","hint":"请检查请求地址，确认使用当前版本支持的 API。"}}`))
	}
}
