package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/revfanc/boxpanel/internal/acme"
)

type Certificates interface {
	Status(context.Context) (acme.Status, error)
	Request(context.Context) error
	Changed()
}

func (a *api) certGet(w http.ResponseWriter, r *http.Request) {
	if a.opts.Certificates == nil {
		a.certUnavailable(w)
		return
	}
	status, err := a.opts.Certificates.Status(r.Context())
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, status)
}
func (a *api) certIssue(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	if a.opts.Certificates == nil || a.opts.Dev {
		a.certUnavailable(w)
		return
	}
	if err := a.opts.Certificates.Request(r.Context()); err != nil {
		if errors.Is(err, acme.ErrBusy) {
			writeError(w, 409, "acme_busy", "证书申请正在进行", "请等待当前申请完成，再到设置查看结果。")
			return
		}
		if errors.Is(err, acme.ErrDisabled) {
			a.certUnavailable(w)
			return
		}
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}
func (a *api) certUnavailable(w http.ResponseWriter) {
	writeError(w, 409, "acme_unavailable", "当前环境不能申请可信证书", "请在生产 HTTPS 面板中申请，并放行本机和云安全组 TCP 80。")
}
