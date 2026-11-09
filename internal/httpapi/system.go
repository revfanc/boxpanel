package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type System interface {
	Check(context.Context) (sysinfo.Check, error)
	Status(context.Context) (sysinfo.Status, error)
	PublicIP(context.Context) (sysinfo.PublicIPs, error)
	EnableBBR(context.Context) (sysinfo.BBR, error)
}

func (a *api) systemCheck(w http.ResponseWriter, r *http.Request) {
	value, err := a.opts.System.Check(r.Context())
	if err != nil {
		a.systemError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *api) systemStatus(w http.ResponseWriter, r *http.Request) {
	value, err := a.opts.System.Status(r.Context())
	if err != nil {
		a.systemError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *api) systemPublicIP(w http.ResponseWriter, r *http.Request) {
	value, err := a.opts.System.PublicIP(r.Context())
	if err != nil {
		a.systemError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *api) systemBBR(w http.ResponseWriter, r *http.Request) {
	value, err := a.opts.System.EnableBBR(r.Context())
	if err != nil {
		a.systemError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *api) systemError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sysinfo.ErrUnsupported):
		writeError(w, 409, "system_unsupported", "当前环境不支持此操作", "请在 Linux VPS 上关闭 --dev 后重试；开发环境不会修改系统设置。")
	case errors.Is(err, sysinfo.ErrBBRUnavailable):
		writeError(w, 409, "bbr_unavailable", "当前内核未提供 BBR", "请升级支持 BBR 的内核；容器用户请联系宿主机管理员。")
	default:
		// Host command output is never sent to the client or request log.
		a.opts.Logger.Error("system operation failed; check host permissions, procfs and outbound connectivity")
		writeError(w, 500, "internal_error", "系统操作失败", "请检查 Linux 系统权限、/proc 挂载、出站网络及系统工具后重试；内核状态异常请运行 boxpanel singbox status；BBR 操作失败时请核对 sysctl 参数和 /etc/sysctl.d/99-boxpanel-bbr.conf。")
	}
}
