package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/revfanc/boxpanel/internal/backup"
)

// Backups never returns credentials in JSON. Export is an authenticated binary
// attachment; Restore validates first and hands off to a separate service.
type Backups interface {
	Create(context.Context, string) ([]byte, error)
	Status() (backup.Status, error)
	Restore(context.Context, []byte, string) error
}

func (a *api) backupPost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Passphrase string `json:"passphrase"`
	}
	if !decodeResource(w, r, &input) {
		return
	}
	if !backup.ValidPassphrase(input.Passphrase) {
		writeError(w, 400, "invalid_argument", "备份口令格式不正确", "请输入至少 12 字符且不超过 1024 字节的口令，并单独妥善保管。")
		return
	}
	if a.opts.Backups == nil {
		a.backupUnavailable(w)
		return
	}
	data, err := a.opts.Backups.Create(r.Context(), input.Passphrase)
	input.Passphrase = ""
	if err != nil {
		a.backupError(w, err)
		return
	}
	defer clear(data)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="boxpanel-backup-`+time.Now().UTC().Format("20060102T150405Z")+`.tar.gz.enc"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
func (a *api) backupStatus(w http.ResponseWriter, r *http.Request) {
	if a.opts.Backups == nil {
		a.backupUnavailable(w)
		return
	}
	status, err := a.opts.Backups.Status()
	if err != nil {
		a.backupError(w, err)
		return
	}
	writeJSON(w, 200, status)
}
func (a *api) restorePost(w http.ResponseWriter, r *http.Request) {
	if a.opts.Backups == nil || a.opts.Dev {
		a.backupUnavailable(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaxSize+(16<<10))
	// Stream multipart to memory with explicit limits, never ParseMultipartForm's
	// implicit temporary files containing a passphrase on disk.
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		writeError(w, 400, "invalid_argument", "恢复需要上传备份文件", "请选择加密备份并填写口令后重试。")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		a.backupError(w, backup.ErrInvalid)
		return
	}
	var data []byte
	var pass string
	var confirmed bool
	seen := map[string]bool{}
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			a.backupError(w, backup.ErrInvalid)
			return
		}
		name := part.FormName()
		if seen[name] || name != "file" && name != "passphrase" && name != "confirm" {
			a.backupError(w, backup.ErrInvalid)
			return
		}
		seen[name] = true
		limit := int64(1025)
		if name == "file" {
			limit = backup.MaxSize + 1
		}
		b, e := io.ReadAll(io.LimitReader(part, limit))
		part.Close()
		if e != nil || int64(len(b)) >= limit {
			a.backupError(w, backup.ErrInvalid)
			return
		}
		switch name {
		case "file":
			data = b
		case "passphrase":
			pass = string(b)
			clear(b)
		case "confirm":
			confirmed = string(b) == "true"
		}
	}
	defer clear(data)
	if !confirmed || !backup.ValidPassphrase(pass) || !backup.IsEncrypted(data) {
		a.backupError(w, backup.ErrInvalid)
		return
	}
	err = a.opts.Backups.Restore(r.Context(), data, pass)
	pass = ""
	if err != nil {
		a.backupError(w, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}
func (a *api) backupUnavailable(w http.ResponseWriter) {
	writeError(w, 409, "backup_unavailable", "当前环境无法执行此备份恢复操作", "恢复请在 Linux/systemd 的生产实例执行 boxpanel restore；自定义路径请先迁移到标准数据目录布局。")
}
func (a *api) backupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrNewer):
		writeError(w, 400, "backup_schema_newer", "备份来自更新的数据库版本", "请先升级 boxpanel，再恢复此备份。")
	case errors.Is(err, backup.ErrInvalid):
		writeError(w, 400, "backup_invalid", "备份无效或口令错误", "请核对至少 12 字符的口令，重新选择完整且未损坏的加密备份，并确认覆盖操作。")
	default:
		writeError(w, 503, "backup_failed", "备份恢复暂时未能完成", "请检查是否已有恢复任务、磁盘空间、数据目录权限及 systemd-run 状态；查看服务日志后重试。")
	}
}
