package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/singbox"
)

type Applier interface {
	Apply(context.Context, singbox.Mutation) error
}

type resourceError struct {
	status            int
	code, field, hint string
}

func (e *resourceError) Error() string        { return "resource operation: " + e.code + " (" + e.field + ")" }
func invalid(field, hint string) error        { return &resourceError{400, "invalid_argument", field, hint} }
func conflict(code, field, hint string) error { return &resourceError{409, code, field, hint} }

func (a *api) resourceError(w http.ResponseWriter, err error) {
	var e *resourceError
	switch {
	case errors.As(err, &e):
		message := "参数校验失败"
		if e.status == 409 {
			message = "端口或名称已被占用"
		}
		if e.code == "reality_target_unavailable" {
			message = "没有检测到合适的 Reality 目标"
		}
		writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": message, "hint": e.hint, "detail": e.field}})
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, 404, "not_found", "记录不存在", "请刷新列表并选择仍然存在的记录。")
	default:
		a.singboxError(w, err)
	}
}

func (a *api) applyResource(w http.ResponseWriter, r *http.Request, mutate singbox.Mutation) bool {
	if a.opts.Applier == nil {
		a.resourceError(w, singbox.ErrUnsupported)
		return false
	}
	if err := a.opts.Applier.Apply(r.Context(), mutate); err != nil {
		a.resourceError(w, err)
		return false
	}
	if a.opts.Dev {
		w.Header().Set("X-Boxpanel-Apply", "dev-db-only")
	} else {
		w.Header().Set("X-Boxpanel-Apply", "applied")
	}
	return true
}

func (a *api) registerResources(register func(string, string, http.HandlerFunc, bool)) {
	register("GET", "/api/v1/reality/recommend", a.realityRecommend, true)
	register("POST", "/api/v1/reality/check", a.realityCheck, true)
	register("POST", "/api/v1/inbounds/suggest", a.inboundSuggest, true)
	register("GET", "/api/v1/inbounds", a.inboundList, true)
	register("POST", "/api/v1/inbounds", a.inboundCreate, true)
	register("GET", "/api/v1/inbounds/{id}", a.inboundGet, true)
	register("PUT", "/api/v1/inbounds/{id}", a.inboundUpdate, true)
	register("DELETE", "/api/v1/inbounds/{id}", a.inboundDelete, true)
	register("POST", "/api/v1/inbounds/{id}/toggle", a.inboundToggle, true)
	register("GET", "/api/v1/users", a.userList, true)
	register("POST", "/api/v1/users", a.userCreate, true)
	register("GET", "/api/v1/users/{id}", a.userGet, true)
	register("PUT", "/api/v1/users/{id}", a.userUpdate, true)
	register("DELETE", "/api/v1/users/{id}", a.userDelete, true)
	register("POST", "/api/v1/users/{id}/toggle", a.userToggle, true)
	register("POST", "/api/v1/users/{id}/reset-token", a.userResetToken, true)
	register("POST", "/api/v1/users/{id}/reset-credentials", a.userResetCredentials, true)
}

func resourceID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalid("id", "请使用列表返回的正整数 ID。")
	}
	return id, nil
}

// Strict resource input rejects null at every level, including nullable Go
// pointer fields used to distinguish omitted fields from false/0/empty values.
func decodeResource(w http.ResponseWriter, r *http.Request, dst any) bool {
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return false
	}
	if err := strictObject(raw, dst); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "invalid_argument", "message": "请求字段无效", "hint": "请按接口文档发送 JSON 对象，不要使用 null、重复字段、未知字段或错误类型。", "detail": "body"}})
		return false
	}
	return true
}

func strictObject(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("expected object")
	}
	check := json.NewDecoder(bytes.NewReader(raw))
	if err := validateJSONValue(check); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("extra JSON")
	}
	return nil
}
func validateJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null field")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("duplicate key")
			}
			seen[strings.ToLower(name)] = true
		}
		if err := validateJSONValue(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func decodeToggle(w http.ResponseWriter, r *http.Request) (*bool, bool) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeResource(w, r, &input) {
		return nil, false
	}
	if input.Enabled == nil {
		writeError(w, 400, "invalid_argument", "缺少启停状态", "请提供 enabled: true 或 false。")
		return nil, false
	}
	return input.Enabled, true
}

func noBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || len(bytes.TrimSpace(body)) > 0 {
		writeError(w, 400, "invalid_argument", "此操作不接受请求内容", "请使用空请求体重试。")
		return false
	}
	return true
}

func (a *api) readDB() (*sql.DB, error) {
	if a.opts.DB == nil {
		return nil, fmt.Errorf("resource database is unavailable; configure the database connection")
	}
	return a.opts.DB, nil
}
