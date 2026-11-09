package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
)

type PanelSettings struct {
	PublicHost      string `json:"public_host"`
	WebPort         int    `json:"web_port"`
	WebPath         string `json:"web_path"`
	SubPath         string `json:"sub_path"`
	TLSMode         string `json:"panel_tls_mode"`
	BlockBittorrent bool   `json:"block_bittorrent"`
	PanelName       string `json:"panel_name"`
}

func (s PanelSettings) URL() string {
	scheme := "https"
	if s.TLSMode == "http" {
		scheme = "http"
	}
	path := "/"
	if s.WebPath != "" {
		path += s.WebPath + "/"
	}
	return scheme + "://" + net.JoinHostPort(s.PublicHost, strconv.Itoa(s.WebPort)) + path
}

func readSettings(ctx context.Context, q db.SettingsDB) (PanelSettings, error) {
	var s PanelSettings
	var err error
	for _, f := range []struct {
		key db.SettingKey[string]
		dst *string
	}{{db.PublicHost, &s.PublicHost}, {db.WebPath, &s.WebPath}, {db.SubPath, &s.SubPath}, {db.PanelTLSMode, &s.TLSMode}, {db.PanelName, &s.PanelName}} {
		v, e := db.GetSetting(ctx, q, f.key)
		if e != nil {
			return s, e
		}
		*f.dst = v
	}
	if s.WebPort, err = db.GetSetting(ctx, q, db.WebPort); err != nil {
		return s, err
	}
	s.BlockBittorrent, err = db.GetSetting(ctx, q, db.BlockBittorrent)
	return s, err
}
func saveSettings(ctx context.Context, q db.SettingsDB, s PanelSettings) error {
	for _, f := range []struct {
		key   db.SettingKey[string]
		value string
	}{{db.PublicHost, s.PublicHost}, {db.WebPath, s.WebPath}, {db.SubPath, s.SubPath}, {db.PanelTLSMode, s.TLSMode}, {db.PanelName, s.PanelName}} {
		if err := db.SetSetting(ctx, q, f.key, f.value); err != nil {
			return err
		}
	}
	if err := db.SetSetting(ctx, q, db.WebPort, s.WebPort); err != nil {
		return err
	}
	return db.SetSetting(ctx, q, db.BlockBittorrent, s.BlockBittorrent)
}
func (a *api) settingsGet(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	tx, err := conn.BeginTx(r.Context(), nil)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	defer tx.Rollback()
	s, err := readSettings(r.Context(), tx)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, s)
}

type settingsInput struct {
	PublicHost      *string `json:"public_host"`
	WebPort         *int    `json:"web_port"`
	WebPath         *string `json:"web_path"`
	SubPath         *string `json:"sub_path"`
	TLSMode         *string `json:"panel_tls_mode"`
	BlockBittorrent *bool   `json:"block_bittorrent"`
	PanelName       *string `json:"panel_name"`
}

var settingPath = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

func patchSettings(s PanelSettings, in settingsInput, dev bool) (PanelSettings, error) {
	if in.PublicHost != nil {
		s.PublicHost = *in.PublicHost
	}
	if in.WebPort != nil {
		s.WebPort = *in.WebPort
	}
	if in.WebPath != nil {
		s.WebPath = *in.WebPath
	}
	if in.SubPath != nil {
		s.SubPath = *in.SubPath
	}
	if in.PanelName != nil {
		s.PanelName = *in.PanelName
	}
	if in.BlockBittorrent != nil {
		s.BlockBittorrent = *in.BlockBittorrent
	}
	if in.TLSMode != nil && *in.TLSMode != s.TLSMode {
		return s, invalid("panel_tls_mode", "当前仅支持初始化时的 TLS 模式；ACME 留待后续版本。")
	}
	if !rules.ValidHost(s.PublicHost) {
		return s, invalid("public_host", "请填写域名或裸 IPv4/IPv6 地址，不含协议、端口或路径。")
	}
	if s.WebPort < 1 || s.WebPort > 65535 {
		return s, invalid("web_port", "请选择 1–65535 的端口。")
	}
	if s.WebPath != "" && !settingPath.MatchString(s.WebPath) || !settingPath.MatchString(s.SubPath) || s.SubPath == s.WebPath || s.SubPath == "api" || s.SubPath == "assets" || s.SubPath == "index" {
		return s, invalid("web_path/sub_path", "路径只含 1–64 位小写字母或数字；两者不可相同，订阅路径不可为 api/assets/index；面板可挂在根目录。")
	}
	if strings.TrimSpace(s.PanelName) == "" || len(s.PanelName) > 128 || strings.ContainsAny(s.PanelName, "\r\n\x00") {
		return s, invalid("panel_name", "请填写 1–128 字节的面板名称，不含换行。")
	}
	if dev && (in.WebPort != nil && *in.WebPort != 2053 || in.WebPath != nil && *in.WebPath != "") {
		return s, invalid("web_port/web_path", "开发模式固定 127.0.0.1:2053 与根路径；请在生产环境修改监听地址。")
	}
	return s, nil
}
func (a *api) settingsPut(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	if !decodeResource(w, r, &in) {
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	tx, err := conn.BeginTx(r.Context(), nil)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	old, err := readSettings(r.Context(), tx)
	_ = tx.Rollback()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	next, err := patchSettings(old, in, a.opts.Dev)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	changeListener := next.WebPort != old.WebPort || next.WebPath != old.WebPath
	commit, cancel := func() {}, func() {}
	if changeListener {
		if a.opts.PreparePanel == nil {
			a.resourceError(w, conflict("listener_unavailable", "web_port/web_path", "监听切换尚未就绪，请重启面板后重试。"))
			return
		}
		commit, cancel, err = a.opts.PreparePanel(r.Context(), next)
		if err != nil {
			a.resourceError(w, conflict("port_in_use", "web_port/web_path", "新监听地址不可用或另一次切换尚未完成，请更换端口或稍后重试。"))
			return
		}
	}
	defer cancel()
	mutation := func(ctx context.Context, tx *sql.Tx) error {
		current, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		if current != old {
			return conflict("settings_changed", "settings", "设置已被其他请求修改，请刷新后重试。")
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM inbounds WHERE port=?", next.WebPort).Scan(&count); err != nil {
			return fmt.Errorf("check panel port: %w", err)
		}
		control, e := db.GetSetting(ctx, tx, db.ClashAPIPort)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if count > 0 || control == next.WebPort {
			return conflict("port_in_use", "web_port", "请避开已有入站和 Clash 控制端口。")
		}
		return saveSettings(ctx, tx, next)
	}
	if next.BlockBittorrent != old.BlockBittorrent {
		if !a.applyResource(w, r, mutation) {
			return
		}
	} else {
		tx, err := conn.BeginTx(r.Context(), nil)
		if err != nil {
			a.resourceError(w, err)
			return
		}
		defer tx.Rollback()
		if err = mutation(r.Context(), tx); err == nil {
			err = tx.Commit()
		}
		if err != nil {
			a.resourceError(w, err)
			return
		}
	}
	if changeListener {
		commit()
	}
	writeJSON(w, 200, struct {
		PanelSettings
		PanelURL     string `json:"panel_url"`
		RestartAfter int    `json:"restart_after"`
	}{next, next.URL(), func() int {
		if changeListener {
			return 2
		}
		return 0
	}()})
}
