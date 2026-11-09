package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
)

type userView struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	UUID         string `json:"uuid"` // masked; credentials and tokens never leave these APIs
	Enabled      bool   `json:"enabled"`
	TrafficLimit int64  `json:"traffic_limit"`
	TrafficUp    int64  `json:"traffic_up"`
	TrafficDown  int64  `json:"traffic_down"`
	ExpireAt     int64  `json:"expire_at"`
	Remark       string `json:"remark"`
	CreatedAt    int64  `json:"created_at"`
	Status       string `json:"status"`
}

type rowScanner interface{ Scan(...any) error }
type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const userColumns = "id,name,uuid,enabled,traffic_limit,traffic_up,traffic_down,expire_at,remark,created_at"

func scanUser(row rowScanner) (userView, error) {
	var u userView
	if err := row.Scan(&u.ID, &u.Name, &u.UUID, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &u.Remark, &u.CreatedAt); err != nil {
		return u, fmt.Errorf("read user: %w", err)
	}
	if len(u.UUID) > 8 {
		u.UUID = u.UUID[:4] + "****" + u.UUID[len(u.UUID)-4:]
	} else {
		u.UUID = "[REDACTED]"
	}
	u.Status = "active"
	switch {
	case !u.Enabled:
		u.Status = "disabled"
	case u.ExpireAt > 0 && u.ExpireAt <= time.Now().Unix():
		u.Status = "expired"
	case u.TrafficLimit > 0 && (u.TrafficUp >= u.TrafficLimit || u.TrafficDown >= u.TrafficLimit-u.TrafficUp):
		u.Status = "exhausted"
	}
	return u, nil
}
func readUser(ctx context.Context, q rowReader, id int64) (userView, error) {
	return scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id))
}

func (a *api) userList(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	rows, err := conn.QueryContext(r.Context(), "SELECT "+userColumns+" FROM users ORDER BY id")
	if err != nil {
		a.resourceError(w, fmt.Errorf("list users: %w", err))
		return
	}
	defer rows.Close()
	result := []userView{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			a.resourceError(w, err)
			return
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		a.resourceError(w, fmt.Errorf("list users: %w", err))
		return
	}
	writeJSON(w, 200, result)
}
func (a *api) userGet(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	u, err := readUser(r.Context(), conn, id)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, u)
}

type userInput struct {
	Name         *string `json:"name"`
	Remark       *string `json:"remark"`
	TrafficLimit *int64  `json:"traffic_limit"`
	ExpireAt     *int64  `json:"expire_at"`
}

func (in userInput) validate(create bool) error {
	if create && in.Name == nil || in.Name != nil && !auth.ValidUsername(*in.Name) {
		return invalid("name", "请使用 1–32 位字母、数字、下划线或连字符作为用户名称。")
	}
	if in.Remark != nil && len(*in.Remark) > 1024 {
		return invalid("remark", "请将备注缩短到 1024 字节以内。")
	}
	if in.TrafficLimit != nil && *in.TrafficLimit < 0 {
		return invalid("traffic_limit", "流量上限须为非负整数字节数，0 表示不限。")
	}
	if in.ExpireAt != nil && *in.ExpireAt < 0 {
		return invalid("expire_at", "到期时间须为非负 Unix 秒数，0 表示永不到期。")
	}
	return nil
}
func uniqueUserName(ctx context.Context, tx *sql.Tx, name string, id int64) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE name=? AND id<>?", name, id).Scan(&count); err != nil {
		return fmt.Errorf("check user name: %w", err)
	}
	if count > 0 {
		return conflict("name_exists", "name", "请换一个尚未使用的用户名称。")
	}
	return nil
}
func newUserCredentials() (string, string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("generate user UUID: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	password, err := auth.RandomToken(24)
	return uuid, password, err
}

func (a *api) userCreate(w http.ResponseWriter, r *http.Request) {
	var input userInput
	if !decodeResource(w, r, &input) {
		return
	}
	if err := input.validate(true); err != nil {
		a.resourceError(w, err)
		return
	}
	uuid, password, err := newUserCredentials()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	token, err := auth.RandomToken(24)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var result userView
	if !a.applyResource(w, r, func(ctx context.Context, tx *sql.Tx) error {
		if err := uniqueUserName(ctx, tx, *input.Name, 0); err != nil {
			return err
		}
		u := userView{Name: *input.Name, Enabled: true, CreatedAt: time.Now().Unix()}
		patchUser(&u, input)
		res, err := tx.ExecContext(ctx, "INSERT INTO users (name,uuid,password,sub_token,traffic_limit,expire_at,remark,created_at) VALUES (?,?,?,?,?,?,?,?)", u.Name, uuid, password, token, u.TrafficLimit, u.ExpireAt, u.Remark, u.CreatedAt)
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("read created user ID: %w", err)
		}
		result, err = readUser(ctx, tx, id)
		return err
	}) {
		return
	}
	writeJSON(w, 201, result)
}
func patchUser(u *userView, input userInput) {
	if input.Name != nil {
		u.Name = *input.Name
	}
	if input.Remark != nil {
		u.Remark = *input.Remark
	}
	if input.TrafficLimit != nil {
		u.TrafficLimit = *input.TrafficLimit
	}
	if input.ExpireAt != nil {
		u.ExpireAt = *input.ExpireAt
	}
}
func (a *api) mutateUser(w http.ResponseWriter, r *http.Request, deleted bool, change func(context.Context, *sql.Tx, userView) error) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var result userView
	if !a.applyResource(w, r, func(ctx context.Context, tx *sql.Tx) error {
		old, err := readUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := change(ctx, tx, old); err != nil {
			return fmt.Errorf("modify user: %w", err)
		}
		if !deleted {
			result, err = readUser(ctx, tx, id)
		}
		return err
	}) {
		return
	}
	if deleted {
		w.WriteHeader(204)
	} else {
		writeJSON(w, 200, result)
	}
}
func (a *api) userUpdate(w http.ResponseWriter, r *http.Request) {
	var input userInput
	if !decodeResource(w, r, &input) {
		return
	}
	if err := input.validate(false); err != nil {
		a.resourceError(w, err)
		return
	}
	a.mutateUser(w, r, false, func(ctx context.Context, tx *sql.Tx, u userView) error {
		patchUser(&u, input)
		if err := uniqueUserName(ctx, tx, u.Name, u.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET name=?,remark=?,traffic_limit=?,expire_at=? WHERE id=?", u.Name, u.Remark, u.TrafficLimit, u.ExpireAt, u.ID)
		return err
	})
}
func (a *api) userToggle(w http.ResponseWriter, r *http.Request) {
	enabled, ok := decodeToggle(w, r)
	if !ok {
		return
	}
	a.mutateUser(w, r, false, func(ctx context.Context, tx *sql.Tx, u userView) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET enabled=? WHERE id=?", *enabled, u.ID)
		return err
	})
}
func (a *api) userDelete(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	a.mutateUser(w, r, true, func(ctx context.Context, tx *sql.Tx, u userView) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=?", u.ID)
		return err
	})
}
func (a *api) userResetToken(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	token, err := auth.RandomToken(24)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	a.mutateUser(w, r, false, func(ctx context.Context, tx *sql.Tx, u userView) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET sub_token=? WHERE id=?", token, u.ID)
		return err
	})
}
func (a *api) userResetCredentials(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	uuid, password, err := newUserCredentials()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	a.mutateUser(w, r, false, func(ctx context.Context, tx *sql.Tx, u userView) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET uuid=?,password=? WHERE id=?", uuid, password, u.ID)
		return err
	})
}
