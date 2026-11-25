package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/db"
)

type userView struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	UUID            string `json:"uuid"` // masked; credentials and tokens never leave these APIs
	Enabled         bool   `json:"enabled"`
	TrafficResetDay int    `json:"traffic_reset_day"`
	TrafficResetAt  int64  `json:"traffic_reset_at"`
	TrafficLimit    int64  `json:"traffic_limit"`
	TrafficUp       int64  `json:"traffic_up"`
	TrafficDown     int64  `json:"traffic_down"`
	ExpireAt        int64  `json:"expire_at"`
	Remark          string `json:"remark"`
	CreatedAt       int64  `json:"created_at"`
	Status          string `json:"status"`
	// AllServers includes servers added later; otherwise ServerIDs lists the
	// non-removed servers whose config and subscription include the user.
	AllServers bool    `json:"all_servers"`
	ServerIDs  []int64 `json:"server_ids"`
}

type userServerTraffic struct {
	ServerID int64  `json:"server_id"`
	Name     string `json:"name"`
	IsLocal  bool   `json:"is_local"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

type rowScanner interface{ Scan(...any) error }
type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type userReader interface {
	rowReader
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const userColumns = "id,name,uuid,enabled,traffic_limit,traffic_up,traffic_down,expire_at,remark,created_at,traffic_reset_day,traffic_reset_at,all_servers"

func scanUser(row rowScanner) (userView, error) {
	u := userView{ServerIDs: []int64{}}
	if err := row.Scan(&u.ID, &u.Name, &u.UUID, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &u.Remark, &u.CreatedAt, &u.TrafficResetDay, &u.TrafficResetAt, &u.AllServers); err != nil {
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
func readUser(ctx context.Context, q userReader, id int64) (userView, error) {
	u, err := scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id))
	if err != nil {
		return u, err
	}
	access, err := readUserServers(ctx, q, id)
	if ids := access[id]; ids != nil {
		u.ServerIDs = ids
	}
	return u, err
}

// readUserServers maps user IDs to their selected non-removed servers; id=0
// reads every user. Rows of users with all_servers=1 are never stored.
func readUserServers(ctx context.Context, q userReader, id int64) (map[int64][]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT us.user_id,us.server_id FROM user_servers us
		JOIN servers s ON s.id=us.server_id WHERE s.status<>'removed' AND (?=0 OR us.user_id=?)
		ORDER BY us.user_id,us.server_id`, id, id)
	if err != nil {
		return nil, fmt.Errorf("read user servers: %w", err)
	}
	defer rows.Close()
	result := map[int64][]int64{}
	for rows.Next() {
		var user, server int64
		if err := rows.Scan(&user, &server); err != nil {
			return nil, fmt.Errorf("scan user server: %w", err)
		}
		result[user] = append(result[user], server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read user servers: %w", err)
	}
	return result, nil
}

// saveUserServers stores the access of a user after validating the selected
// servers. A nil ids keeps the current selection.
func saveUserServers(ctx context.Context, tx *sql.Tx, userID int64, all bool, ids *[]int64) error {
	if _, err := tx.ExecContext(ctx, "UPDATE users SET all_servers=? WHERE id=?", all, userID); err != nil {
		return fmt.Errorf("save user server scope: %w", err)
	}
	if all {
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_servers WHERE user_id=?", userID); err != nil {
			return fmt.Errorf("clear user servers: %w", err)
		}
		return nil
	}
	if ids != nil {
		wanted := map[int64]bool{}
		for _, id := range *ids {
			var ok bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM servers WHERE id=? AND status<>'removed')", id).Scan(&ok); err != nil {
				return fmt.Errorf("check user server: %w", err)
			}
			if !ok {
				return invalid("server_ids", "所选服务器不存在或已被移除；请刷新页面后重新选择。")
			}
			wanted[id] = true
		}
		current, err := readUserServers(ctx, tx, userID)
		if err != nil {
			return err
		}
		// Only touch rows that change, so untouched nodes keep their generation.
		for _, id := range current[userID] {
			if wanted[id] {
				delete(wanted, id)
			} else if _, err := tx.ExecContext(ctx, "DELETE FROM user_servers WHERE user_id=? AND server_id=?", userID, id); err != nil {
				return fmt.Errorf("remove user server: %w", err)
			}
		}
		for id := range wanted {
			if _, err := tx.ExecContext(ctx, "INSERT INTO user_servers (user_id,server_id) VALUES (?,?)", userID, id); err != nil {
				return fmt.Errorf("add user server: %w", err)
			}
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_servers us JOIN servers s ON s.id=us.server_id
		WHERE us.user_id=? AND s.status<>'removed'`, userID).Scan(&count); err != nil {
		return fmt.Errorf("count user servers: %w", err)
	}
	if count == 0 {
		return invalid("server_ids", "请至少选择一台服务器，或选择「全部服务器」。")
	}
	return nil
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
	rows.Close()
	access, err := readUserServers(r.Context(), conn, 0)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	for i := range result {
		if ids := access[result[i].ID]; ids != nil {
			result[i].ServerIDs = ids
		}
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
	ctx := r.Context()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.resourceError(w, fmt.Errorf("begin user detail: %w", err))
		return
	}
	defer tx.Rollback()
	u, err := readUser(ctx, tx, id)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	byServer, err := readUserServerTraffic(ctx, tx, u)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		userView
		TrafficByServer []userServerTraffic `json:"traffic_by_server,omitempty"`
	}{u, byServer})
}

func readUserServerTraffic(ctx context.Context, tx *sql.Tx, u userView) ([]userServerTraffic, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,s.is_local,coalesce(t.cycle_up,0),coalesce(t.cycle_down,0)
		FROM servers s CROSS JOIN users u
		LEFT JOIN server_user_traffic t ON t.server_id=s.id AND t.user_id=u.id AND t.user_epoch=u.traffic_epoch
		WHERE u.id=? AND s.status<>'removed' ORDER BY s.is_local DESC,s.id`, u.ID)
	if err != nil {
		return nil, fmt.Errorf("read user traffic by server: %w", err)
	}
	defer rows.Close()
	var result []userServerTraffic
	var remote cycleBytes
	for rows.Next() {
		var row userServerTraffic
		if err := rows.Scan(&row.ServerID, &row.Name, &row.IsLocal, &row.Up, &row.Down); err != nil {
			return nil, fmt.Errorf("scan user traffic by server: %w", err)
		}
		if !row.IsLocal {
			remote.add(cycleBytes{row.Up, row.Down})
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list user traffic by server: %w", err)
	}
	if len(result) < 2 {
		return nil, nil
	}
	if result[0].IsLocal {
		local := (cycleBytes{u.TrafficUp, u.TrafficDown}).remaining(remote)
		result[0].Up, result[0].Down = local.Up, local.Down
	}
	return result, nil
}

type userInput struct {
	TrafficResetDay *int     `json:"traffic_reset_day"`
	Name            *string  `json:"name"`
	Remark          *string  `json:"remark"`
	TrafficLimit    *int64   `json:"traffic_limit"`
	ExpireAt        *int64   `json:"expire_at"`
	AllServers      *bool    `json:"all_servers"`
	ServerIDs       *[]int64 `json:"server_ids"`
}

func (in userInput) validate(create bool) error {
	if in.ServerIDs != nil {
		if len(*in.ServerIDs) > 1000 {
			return invalid("server_ids", "所选服务器过多；请改为选择「全部服务器」。")
		}
		for _, id := range *in.ServerIDs {
			if id <= 0 {
				return invalid("server_ids", "服务器编号无效；请刷新页面后重新选择。")
			}
		}
	}
	if create && in.Name == nil || in.Name != nil && !auth.ValidUsername(*in.Name) {
		return invalid("name", "请使用 1–32 位字母、数字、下划线或连字符作为用户名称。")
	}
	if in.Remark != nil && len(*in.Remark) > 1024 {
		return invalid("remark", "请将备注缩短到 1024 字节以内。")
	}
	if in.TrafficResetDay != nil && (*in.TrafficResetDay < 0 || *in.TrafficResetDay > 28) {
		return invalid("traffic_reset_day", "每月重置日须为 0–28 的整数，0 表示不自动重置；请修改后重试。")
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
	var epochBytes [8]byte
	if _, err := rand.Read(epochBytes[:]); err != nil {
		a.resourceError(w, fmt.Errorf("generate traffic epoch: %w", err))
		return
	}
	// New users get a random epoch, preventing reused SQLite IDs from receiving
	// pending increments belonging to a deleted user. Leave ample reset headroom.
	epoch := int64(binary.BigEndian.Uint64(epochBytes[:])&((1<<62)-1)) + 1
	var result userView
	if !a.applyResource(w, r, func(ctx context.Context, tx *sql.Tx) error {
		if err := uniqueUserName(ctx, tx, *input.Name, 0); err != nil {
			return err
		}
		u := userView{Name: *input.Name, Enabled: true, CreatedAt: time.Now().Unix()}
		patchUser(&u, input)
		lastID, err := db.GetSetting(ctx, tx, db.UserIDHighWatermark)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO users
			(id,name,uuid,password,sub_token,traffic_limit,expire_at,remark,created_at,traffic_reset_day,traffic_epoch,alert_identity)
			VALUES (max(?,coalesce((SELECT max(id) FROM users),0))+1,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
			lastID, u.Name, uuid, password, token, u.TrafficLimit, u.ExpireAt, u.Remark, u.CreatedAt, u.TrafficResetDay, epoch, epoch).Scan(&id); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if err := db.SetSetting(ctx, tx, db.UserIDHighWatermark, id); err != nil {
			return err
		}
		if err := applyUserServers(ctx, tx, id, input); err != nil {
			return err
		}
		result, err = readUser(ctx, tx, id)
		return err
	}) {
		return
	}
	writeJSON(w, 201, result)
}
func patchUser(u *userView, input userInput) {
	if input.TrafficResetDay != nil {
		u.TrafficResetDay = *input.TrafficResetDay
	}
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
		if _, err := tx.ExecContext(ctx, "UPDATE users SET name=?,remark=?,traffic_limit=?,expire_at=?,traffic_reset_day=? WHERE id=?", u.Name, u.Remark, u.TrafficLimit, u.ExpireAt, u.TrafficResetDay, u.ID); err != nil {
			return err
		}
		return applyUserServers(ctx, tx, u.ID, input)
	})
}

// applyUserServers saves server access when the input mentions it. Giving
// only server_ids selects exactly those servers.
func applyUserServers(ctx context.Context, tx *sql.Tx, id int64, input userInput) error {
	if input.AllServers == nil && input.ServerIDs == nil {
		return nil
	}
	all := false
	if input.AllServers != nil {
		all = *input.AllServers
	}
	return saveUserServers(ctx, tx, id, all, input.ServerIDs)
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

func (a *api) userResetTraffic(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	a.mutateUser(w, r, false, func(ctx context.Context, tx *sql.Tx, u userView) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET traffic_up=0,traffic_down=0,traffic_reset_at=?,traffic_epoch=traffic_epoch+1 WHERE id=?", time.Now().Unix(), u.ID)
		return err
	})
}
