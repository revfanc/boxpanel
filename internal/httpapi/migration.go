package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/migration"
)

func (a *api) migrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrMigrationReadOnly):
		writeError(w, 409, "migration_read_only", "当前主控处于迁移只读状态", "迁移完成后请在新主控操作；若迁移失败，请等待旧主控解冻后重试。")
	case errors.Is(err, migration.ErrToken):
		writeError(w, 401, "invalid_migration_token", migration.ErrToken.Error(), "请回到设置页生成并复制新的迁移命令。")
	case errors.Is(err, migration.ErrCommitted):
		writeError(w, 409, "migration_committed", migration.ErrCommitted.Error(), "请使用新主控面板检查节点切换进度。")
	case errors.Is(err, migration.ErrBusy), errors.Is(err, context.DeadlineExceeded):
		writeError(w, 409, "migration_busy", migration.ErrBusy.Error(), "请等待其他写入操作结束，再执行迁移命令。")
	case errors.Is(err, migration.ErrAddress):
		writeError(w, 422, "migration_address_required", migration.ErrAddress.Error(), "请在迁移命令末尾添加 --public-ip <新公网 IP>。")
	case errors.Is(err, migration.ErrVersion):
		writeError(w, 426, "migration_version_mismatch", migration.ErrVersion.Error(), "请在新机安装与旧主控相同的版本。")
	case errors.Is(err, migration.ErrReady):
		writeError(w, 422, "migration_target_unready", migration.ErrReady.Error(), "请放行新机面板 TCP 端口，并确保旧主控可访问新机。")
	default:
		a.opts.Logger.Warn("主控迁移操作失败，请检查数据库、证书和备份目录权限。")
		writeError(w, 500, "migration_failed", "主控迁移操作失败", "请检查两台服务器的网络、证书、磁盘空间与服务日志后重试。")
	}
}

func (a *api) migrationWrite(next http.Handler) http.Handler {
	if a.opts.Migration == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, err := a.opts.Migration.BeginWrite(r.Context())
		if err != nil {
			a.migrationError(w, err)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

func (a *api) getMigration(w http.ResponseWriter, r *http.Request) {
	if a.opts.Migration == nil {
		writeJSON(w, 200, migration.Status{Phase: "unavailable"})
		return
	}
	status, err := a.opts.Migration.PanelStatus(r.Context())
	if err != nil {
		a.migrationError(w, err)
		return
	}
	writeJSON(w, 200, status)
}

func (a *api) issueMigration(w http.ResponseWriter, r *http.Request) {
	if a.opts.Migration == nil || r.TLS == nil {
		writeError(w, 409, "migration_unavailable", "当前环境无法发起主控迁移", "请在使用标准数据目录的 Linux 生产面板上使用 HTTPS 发起迁移。")
		return
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Confirm {
		writeError(w, 400, "migration_confirmation_required", "请确认主控迁移", "阅读冻结和订阅地址变更说明后重试。")
		return
	}
	cfg, err := app.LoadConfig(r.Context(), a.opts.Migration.ReadDB)
	if err != nil {
		a.migrationError(w, err)
		return
	}
	if a.opts.PanelPort > 0 {
		cfg.WebPort = a.opts.PanelPort
	}
	cfg.WebPath = a.opts.WebPath
	grant, err := a.opts.Migration.Issue(r.Context(), cfg.URL())
	if err != nil {
		a.migrationError(w, err)
		return
	}
	writeJSON(w, 201, grant)
}

// Peer calls live outside the buffered management middleware. The private
// archive has a known length and is streamed directly over HTTPS.
func (a *api) migrationPeer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.TLS == nil || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		a.migrationError(w, migration.ErrToken)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "method_not_allowed", "请求方法不支持", "请使用接口文档指定的请求方法。")
		return
	}
	switch r.URL.Path {
	case migration.Prefix + "/claim":
		a.migrationClaim(w, r)
	case migration.Prefix + "/ready":
		var in migration.Ready
		if decode(w, r, &in) {
			if err := a.opts.Migration.Ready(r.Context(), in); err != nil {
				a.migrationError(w, err)
				return
			}
			w.WriteHeader(204)
		}
	case migration.Prefix + "/abort":
		var in struct {
			Secret string `json:"secret"`
		}
		if decode(w, r, &in) {
			if err := a.opts.Migration.Abort(r.Context(), in.Secret); err != nil {
				a.migrationError(w, err)
				return
			}
			w.WriteHeader(204)
		}
	case migration.Prefix + "/status":
		var in struct {
			ID string `json:"id"`
		}
		if decode(w, r, &in) {
			status, err := a.opts.Migration.PeerStatus(r.Context(), in.ID)
			if err != nil {
				a.migrationError(w, err)
				return
			}
			writeJSON(w, 200, status)
		}
	default:
		notFound(w, r)
	}
}

func (a *api) migrationClaim(w http.ResponseWriter, r *http.Request) {
	var in migration.Claim
	if !decode(w, r, &in) {
		return
	}
	s := a.opts.Migration
	state, err := s.Claim(r.Context(), in, peerIP(r.RemoteAddr))
	if err != nil {
		a.migrationError(w, err)
		return
	}
	sent := false
	defer func() {
		if !sent {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Abort(ctx, in.Secret)
		}
	}()
	// Keep the normal panel write timeout for all other endpoints.
	ctx, cancel := context.WithTimeout(r.Context(), migration.Lifetime)
	defer cancel()
	remaining := time.Unix(state.ExpiresAt, 0)
	_ = http.NewResponseController(w).SetWriteDeadline(remaining)
	file, err := s.Snapshot(ctx, state)
	if err != nil {
		a.migrationError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		a.migrationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set(migration.HeaderTarget, state.TargetURL)
	w.Header().Set(migration.HeaderExpires, strconv.FormatInt(state.ExpiresAt, 10))
	n, err := io.Copy(w, file)
	if err == nil && n == info.Size() && r.Context().Err() == nil {
		err = http.NewResponseController(w).Flush()
		sent = err == nil || errors.Is(err, http.ErrNotSupported)
	}
}
