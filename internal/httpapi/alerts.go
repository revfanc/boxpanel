package httpapi

import (
	"errors"
	"net/http"

	"github.com/revfanc/boxpanel/internal/alerts"
)

func (a *api) registerAlerts(register func(string, string, http.HandlerFunc, bool)) {
	register("GET", "/api/v1/alerts", a.alertOverview, true)
	register("GET", "/api/v1/alerts/settings", a.alertSettings, true)
	register("PUT", "/api/v1/alerts/settings", a.alertSaveSettings, true)
	register("PUT", "/api/v1/alerts/channels/{channel}", a.alertSaveChannel, true)
	register("POST", "/api/v1/alerts/channels/{channel}/test", a.alertTest, true)
	register("POST", "/api/v1/alerts/events/{key}/read", a.alertRead, true)
}

func (a *api) alertsAvailable(w http.ResponseWriter) bool {
	if a.opts.Alerts == nil {
		writeError(w, 503, "alerts_unavailable", "告警服务暂不可用", "请稍后刷新，并检查面板服务状态。")
		return false
	}
	return true
}

func alertError(w http.ResponseWriter, err error) {
	var validation alerts.ValidationError
	if errors.As(err, &validation) {
		hint := "请按设置页或接口文档填写后重试。"
		switch validation {
		case "credentials_required", "credential", "chat_id":
			hint = "请从对应通道复制有效凭证。Telegram 还需要数字聊天 ID 或 @频道名。"
		case "url", "bark_destination":
			hint = "请填写可从服务器解析为公网 IP 的 HTTPS Bark 地址，或使用默认的 https://api.day.app。"
		case "interface":
			hint = "请选择自动检测，或填入服务器上有效的非回环网卡名称。"
		case "quota_gb":
			hint = "请输入 0 至 9223372036 之间的整数 GB。"
		case "billing_day":
			hint = "请输入 1 至 28 之间的 UTC 计费日。"
		}
		writeJSON(w, 400, map[string]any{"error": map[string]string{
			"code": "invalid_argument", "message": "告警设置无效", "hint": hint, "detail": string(validation),
		}})
		return
	}
	writeError(w, 500, "alerts_failed", "告警操作失败", "请稍后重试，如仍失败，请检查数据库权限与磁盘空间。")
}

func (a *api) alertOverview(w http.ResponseWriter, r *http.Request) {
	if !a.alertsAvailable(w) {
		return
	}
	result, err := a.opts.Alerts.Overview(r.Context())
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) alertSettings(w http.ResponseWriter, r *http.Request) {
	if !a.alertsAvailable(w) {
		return
	}
	result, err := a.opts.Alerts.Settings(r.Context())
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) alertSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in alerts.ConfigUpdate
	if !a.alertsAvailable(w) || !decodeResource(w, r, &in) {
		return
	}
	result, err := a.opts.Alerts.SaveConfig(r.Context(), in)
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) alertSaveChannel(w http.ResponseWriter, r *http.Request) {
	var in alerts.ChannelUpdate
	if !a.alertsAvailable(w) || !decodeResource(w, r, &in) {
		return
	}
	result, err := a.opts.Alerts.SaveChannel(r.Context(), r.PathValue("channel"), in)
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) alertTest(w http.ResponseWriter, r *http.Request) {
	if !a.alertsAvailable(w) || !noBody(w, r) {
		return
	}
	id, err := a.opts.Alerts.QueueTest(r.Context(), r.PathValue("channel"))
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 202, map[string]int64{"id": id})
}

func (a *api) alertRead(w http.ResponseWriter, r *http.Request) {
	if !a.alertsAvailable(w) || !noBody(w, r) {
		return
	}
	found, err := a.opts.Alerts.ReadEvent(r.Context(), r.PathValue("key"))
	if err != nil {
		alertError(w, err)
		return
	}
	if !found {
		writeError(w, 404, "not_found", "告警不存在", "请刷新概览并选择仍然存在的告警。")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
