package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/subscription"
)

func (a *api) registerSubscriptions(register func(string, string, http.HandlerFunc, bool)) {
	register("GET", "/api/v1/users/{id}/links", a.userLinks, true)
	register("GET", "/api/v1/rules", a.rulesGet, true)
	register("PUT", "/api/v1/rules", a.rulesPut, true)
	register("GET", "/api/v1/rules/preview", a.rulesPreview, true)
	register("GET", "/api/v1/settings", a.settingsGet, true)
	register("PUT", "/api/v1/settings", a.settingsPut, true)
}
func (a *api) subscriptionData(ctx context.Context, id int64) (subscription.Data, error) {
	if a.opts.Subscriptions == nil {
		return subscription.Data{}, fmt.Errorf("subscription store unavailable")
	}
	d, err := a.opts.Subscriptions.Load(ctx, id, nil)
	if a.opts.PanelPort > 0 {
		d.WebPort = a.opts.PanelPort
	}
	return d, err
}
func (a *api) subscriptionError(w http.ResponseWriter, err error) {
	if errors.Is(err, subscription.ErrUnavailable) {
		writeError(w, 409, "subscription_unavailable", "节点证书尚不可用", "请在 Linux 服务器上成功生效节点配置后重试；开发模式不创建证书。")
		return
	}
	a.resourceError(w, err)
}
func (a *api) userLinks(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	d, err := a.subscriptionData(r.Context(), id)
	if err != nil {
		a.subscriptionError(w, err)
		return
	}
	l, err := subscription.MakeLinks(d)
	if err != nil {
		a.subscriptionError(w, err)
		return
	}
	writeJSON(w, 200, l)
}
func (a *api) rulesGet(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	s, err := db.GetSetting(r.Context(), conn, db.Rules)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, s)
}
func (a *api) rulesPut(w http.ResponseWriter, r *http.Request) {
	var input db.RulesSettings
	if !decodeResource(w, r, &input) {
		return
	}
	if err := rules.Validate(input); err != nil {
		a.resourceError(w, invalid("rules", "请选择有效预设与规则源；DNS 使用 HTTPS，镜像以 / 结尾，更新间隔为 1h–30d，域名列表不含通配符或 URL。"))
		return
	}
	if input.CustomDirect == nil {
		input.CustomDirect = []string{}
	}
	if input.CustomProxy == nil {
		input.CustomProxy = []string{}
	}
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if err = db.SetSetting(r.Context(), conn, db.Rules, input); err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, input)
}
func (a *api) rulesPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := subscription.Detect(q, "sing-box", "")
	if err != nil || f != subscription.Singbox && f != subscription.Clash {
		a.resourceError(w, invalid("format", "预览格式请选择 singbox 或 clash。"))
		return
	}
	d, err := a.subscriptionData(r.Context(), 0)
	if err != nil {
		a.subscriptionError(w, err)
		return
	}
	// Redact structured inputs, before serializing either JSON or YAML. This
	// also handles credentials appearing in names/hosts through replacement.
	secrets := []string{d.User.UUID, d.User.Password, d.User.Token}
	for _, n := range d.Nodes {
		if n.Hysteria2 != nil {
			secrets = append(secrets, n.Hysteria2.ObfsPassword)
		}
	}
	b, err := subscription.Render(d.Snapshot, f)
	if err != nil {
		a.subscriptionError(w, err)
		return
	}
	// Decode and walk serialized output so escaped YAML/JSON secrets cannot
	// evade redaction. Preview is a JSON envelope, including for YAML.
	redacted, err := subscription.Redact(b, f, secrets)
	if err != nil {
		a.subscriptionError(w, err)
		return
	}
	if f == subscription.Singbox {
		writeJSON(w, 200, map[string]any{"format": f, "config": json.RawMessage(redacted)})
	} else {
		writeJSON(w, 200, map[string]any{"format": f, "config": string(redacted)})
	}
}
