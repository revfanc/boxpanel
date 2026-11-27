package httpapi

import (
	"context"
	"net/http"
)

type UpgradeStatus struct {
	State   string `json:"state"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target,omitempty"`
}

type MasterUpgrade interface {
	Status(context.Context) (UpgradeStatus, error)
	Start(context.Context) (UpgradeStatus, error)
}

func (a *api) masterUpgrade(w http.ResponseWriter, r *http.Request) {
	start := r.Method == http.MethodPost
	if start && !noBody(w, r) {
		return
	}
	if a.opts.MasterUpgrade == nil {
		if start {
			writeError(w, 422, "upgrade_unsupported", "此安装方式不支持面板升级", "请使用官方安装脚本部署的主控发布版。")
		} else {
			writeJSON(w, 200, UpgradeStatus{State: "unsupported"})
		}
		return
	}
	action := a.opts.MasterUpgrade.Status
	if start {
		action = a.opts.MasterUpgrade.Start
	}
	status, err := action(r.Context())
	if err != nil {
		a.opts.Logger.Warn("master upgrade request failed", "reason", err)
		writeError(w, 503, "upgrade_unavailable", "未能检查或启动主控升级", "请检查主控到 GitHub 的连接和 systemd 服务状态，然后在此重试。")
		return
	}
	code := http.StatusOK
	if start && status.State == "running" {
		code = http.StatusAccepted
	}
	writeJSON(w, code, status)
}
