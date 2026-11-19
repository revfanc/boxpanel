package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type serverRecord struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PublicHost string `json:"public_host"`
	IsLocal    bool   `json:"is_local"`
	Status     string `json:"status"`
	LastSeenAt *int64 `json:"last_seen_at"`
	Version    string `json:"version"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

const serverColumns = "id,name,public_host,is_local,status,last_seen_at,version,created_at,updated_at"

func scanServer(row rowScanner) (serverRecord, error) {
	var s serverRecord
	if err := row.Scan(&s.ID, &s.Name, &s.PublicHost, &s.IsLocal, &s.Status, &s.LastSeenAt, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return s, fmt.Errorf("read server: %w", err)
	}
	return s, nil
}

func (a *api) serverList(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	rows, err := conn.QueryContext(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE status<>'removed' ORDER BY is_local DESC, id")
	if err != nil {
		a.resourceError(w, fmt.Errorf("list servers: %w", err))
		return
	}
	defer rows.Close()
	servers := []serverRecord{}
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			a.resourceError(w, err)
			return
		}
		servers = append(servers, s)
	}
	if err := rows.Err(); err != nil {
		a.resourceError(w, fmt.Errorf("list servers: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

func (a *api) serverRename(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	name, ok := a.readServerName(w, r)
	if !ok {
		return
	}
	conn, err := a.writeDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	// Renaming changes subscription metadata. It does not require kernel Apply.
	s, err := scanServer(conn.QueryRowContext(r.Context(), "UPDATE servers SET name=?, updated_at=unixepoch() WHERE id=? AND status<>'removed' RETURNING "+serverColumns, name, id))
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *api) readServerName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Name string `json:"name"`
	}
	if !decodeResource(w, r, &input) {
		return "", false
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(input.Name, unicode.IsControl) {
		a.resourceError(w, invalid("name", "请输入 1–64 个字符的服务器名称，且不要包含控制字符。"))
		return "", false
	}
	return name, true
}
