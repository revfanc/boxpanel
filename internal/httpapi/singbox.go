package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/revfanc/boxpanel/internal/singbox"
)

type Singbox interface {
	Restart(context.Context) error
	Logs(context.Context, int) ([]string, error)
	Config(context.Context) (json.RawMessage, error)
}

func (a *api) singboxRestart(w http.ResponseWriter, r *http.Request) {
	if a.opts.Singbox == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	if err := a.opts.Singbox.Restart(r.Context()); err != nil {
		a.singboxError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *api) singboxLogs(w http.ResponseWriter, r *http.Request) {
	lines := 200
	values := r.URL.Query()["lines"]
	var err error
	if len(values) > 0 {
		lines, err = strconv.Atoi(values[0])
	}
	if err != nil || len(values) > 1 || lines < 1 || lines > 1000 {
		writeError(w, 400, "invalid_argument", "日志行数无效", "请使用 1–1000 之间的整数 lines；省略时返回最近 200 行。")
		return
	}
	if a.opts.Singbox == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	result, err := a.opts.Singbox.Logs(r.Context(), lines)
	if err != nil {
		a.singboxError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"lines": result})
}
func (a *api) singboxConfig(w http.ResponseWriter, r *http.Request) {
	if a.opts.Singbox == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	result, err := a.opts.Singbox.Config(r.Context())
	if err != nil {
		a.singboxError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *api) singboxError(w http.ResponseWriter, err error) {
	var apply *singbox.ApplyError
	switch {
	case errors.Is(err, singbox.ErrUnsupported):
		writeError(w, 409, "system_unsupported", "当前环境不支持 sing-box 生效操作", "请在 Linux VPS 上关闭 --dev 后重试；本机可用 make tools 校验配置。")
	case errors.Is(err, singbox.ErrNoConfig):
		writeError(w, 404, "singbox_config_missing", "尚无生效配置", "请先安装内核，再运行 boxpanel apply。")
	case errors.As(err, &apply):
		status := 500
		message := "配置生效失败，已恢复旧配置，数据库修改未提交"
		hint := "请查看脱敏诊断，检查服务、端口和防火墙权限后重试 boxpanel apply。"
		code := apply.Code
		if code == "singbox_check_failed" {
			status = 422
			message = "配置生成或校验未通过，数据库修改未提交"
			hint = "请检查入站、用户及证书配置，修正后重新生效。"
		}
		if apply.RollbackFailed {
			status = 500
			code = "singbox_rollback_failed"
			message = "配置生效失败，自动恢复也未完成，数据库修改未提交"
			hint = "请检查 sing-box.json.bak、systemd 和防火墙状态，修复后运行 boxpanel apply；恢复完成前不要继续修改节点。"
		}
		if apply.DatabaseUncertain {
			message = "数据库提交或恢复状态无法确认"
			hint = "请保留 sing-box.json.bak 和 .pending，修复数据库访问后运行 boxpanel apply，待自动核对恢复完成再继续操作。"
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "hint": hint, "detail": apply.Detail}})
	default:
		a.opts.Logger.Error("sing-box operation failed; inspect managed files, database and service permissions")
		writeError(w, 500, "internal_error", "sing-box 操作失败", "请检查数据目录、内核安装和 systemd 权限，再运行 boxpanel apply 重试。")
	}
}
