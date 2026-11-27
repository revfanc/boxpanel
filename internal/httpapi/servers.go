package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/traffic"
)

type serverRecord struct {
	ID                int64                `json:"id"`
	Name              string               `json:"name"`
	PublicHost        string               `json:"public_host"`
	IsLocal           bool                 `json:"is_local"`
	Status            string               `json:"status"`
	LastSeenAt        *int64               `json:"last_seen_at"`
	Version           string               `json:"version"`
	Protocol          int                  `json:"protocol"`
	UpgradeRequired   bool                 `json:"upgrade_required,omitempty"`
	CreatedAt         int64                `json:"created_at"`
	UpdatedAt         int64                `json:"updated_at"`
	DesiredGeneration int64                `json:"desired_generation,omitempty"`
	AppliedGeneration *int64               `json:"applied_generation,omitempty"`
	SyncStatus        string               `json:"sync_status,omitempty"`
	SyncError         string               `json:"sync_error,omitempty"`
	SingboxActive     *bool                `json:"singbox_active,omitempty"`
	TrafficUp         *int64               `json:"traffic_up,omitempty"`
	TrafficDown       *int64               `json:"traffic_down,omitempty"`
	Uptime            *int64               `json:"uptime,omitempty"`
	InboundCounts     *serverInboundCounts `json:"inbound_counts,omitempty"`
	AutoUpgrade       *bool                `json:"auto_upgrade,omitempty"`
	monitoring        sql.NullString
	restart           *agent.RestartState
}

type serverInboundCounts struct {
	Ready   int64 `json:"ready"`
	Pending int64 `json:"pending"`
}

const serverColumns = "id,name,public_host,is_local,status,last_seen_at,version,created_at,updated_at,desired_generation,applied_generation,sync_error,singbox_active,uptime,protocol,auto_upgrade,monitoring,restart_id,restart_status,restart_error"

const serverSelect = `SELECT ` + serverColumns + `,
	(SELECT count(*) FROM inbounds WHERE server_id=servers.id AND (agent_key_id='' OR agent_keys_ready=1)),
	(SELECT count(*) FROM inbounds WHERE server_id=servers.id AND agent_key_id<>'' AND agent_keys_ready=0)
	FROM servers WHERE status<>'removed'`

func scanServer(row rowScanner, counts *serverInboundCounts) (serverRecord, error) {
	var s serverRecord
	var desired, applied int64
	var syncError string
	var active *bool
	var uptime *int64
	var autoUpgrade bool
	var restart agent.RestartState
	dest := []any{&s.ID, &s.Name, &s.PublicHost, &s.IsLocal, &s.Status, &s.LastSeenAt,
		&s.Version, &s.CreatedAt, &s.UpdatedAt, &desired, &applied, &syncError, &active, &uptime, &s.Protocol, &autoUpgrade,
		&s.monitoring, &restart.ID, &restart.Status, &restart.Error}
	if counts != nil {
		dest = append(dest, &counts.Ready, &counts.Pending)
	}
	if err := row.Scan(dest...); err != nil {
		return s, fmt.Errorf("read server: %w", err)
	}
	s.InboundCounts = counts
	if restart.ID != "" {
		s.restart = &restart
	}
	if !s.IsLocal {
		s.UpgradeRequired = s.Protocol < agent.MinNodeProtocol
		s.AutoUpgrade = &autoUpgrade
		s.Uptime = uptime
		s.DesiredGeneration, s.AppliedGeneration, s.SingboxActive = desired, &applied, active
		s.SyncError = syncError
		s.SyncStatus = "syncing"
		if syncError != "" || s.UpgradeRequired {
			s.SyncStatus = "failed"
		} else if applied == desired && active != nil && *active {
			s.SyncStatus = "synced"
		}
	} else {
		s.Protocol = agent.ProtocolVersion
	}
	return s, nil
}

func (a *api) serverList(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	ctx := r.Context()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.resourceError(w, fmt.Errorf("begin server list: %w", err))
		return
	}
	defer tx.Rollback()
	servers, err := readServers(ctx, tx)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	remote := make(map[int64]int)
	for i := range servers {
		if !servers[i].IsLocal {
			remote[servers[i].ID] = i
			servers[i].TrafficUp, servers[i].TrafficDown = new(int64), new(int64)
		}
	}
	if len(remote) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT t.server_id,t.total_up,t.total_down FROM server_user_traffic t
			JOIN servers s ON s.id=t.server_id WHERE s.is_local=0 AND s.status<>'removed'`)
		if err != nil {
			a.resourceError(w, fmt.Errorf("read server traffic totals: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, up, down int64
			if err := rows.Scan(&id, &up, &down); err != nil {
				a.resourceError(w, fmt.Errorf("scan server traffic totals: %w", err))
				return
			}
			s := &servers[remote[id]]
			*s.TrafficUp = traffic.AddBytes(*s.TrafficUp, up)
			*s.TrafficDown = traffic.AddBytes(*s.TrafficDown, down)
		}
		if err := rows.Err(); err != nil {
			a.resourceError(w, fmt.Errorf("list server traffic totals: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, servers)
}

func readServers(ctx context.Context, tx *sql.Tx) ([]serverRecord, error) {
	rows, err := tx.QueryContext(ctx, serverSelect+" ORDER BY is_local DESC,id")
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	servers := []serverRecord{}
	for rows.Next() {
		s, err := scanServer(rows, &serverInboundCounts{})
		if err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return servers, nil
}

func (a *api) loadServer(r *http.Request) (serverRecord, error) {
	id, err := resourceID(r)
	if err != nil {
		return serverRecord{}, err
	}
	conn, err := a.readDB()
	if err != nil {
		return serverRecord{}, err
	}
	return scanServer(conn.QueryRowContext(r.Context(), serverSelect+" AND id=?", id), &serverInboundCounts{})
}

func (a *api) serverGet(w http.ResponseWriter, r *http.Request) {
	server, err := a.loadServer(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, server)
}

func (a *api) serverRename(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var input struct {
		Name        *string `json:"name"`
		PublicHost  *string `json:"public_host"`
		AutoUpgrade *bool   `json:"auto_upgrade"`
	}
	if !decodeResource(w, r, &input) {
		return
	}
	if input.Name == nil && input.PublicHost == nil && input.AutoUpgrade == nil {
		a.resourceError(w, invalid("body", "请提供服务器名称、公网地址或自动升级设置后重试。"))
		return
	}
	if input.Name != nil {
		if err := validateServerName(*input.Name); err != nil {
			a.resourceError(w, err)
			return
		}
		*input.Name = strings.TrimSpace(*input.Name)
	}
	ctx := r.Context()
	conn, err := a.writeDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if input.PublicHost != nil {
		*input.PublicHost = strings.TrimSpace(*input.PublicHost)
		if !rules.ValidHost(*input.PublicHost) {
			a.resourceError(w, invalid("public_host", "请输入公网域名或裸 IPv4/IPv6 地址。"))
			return
		}
	}
	if input.PublicHost != nil || input.AutoUpgrade != nil {
		var local bool
		if err := conn.QueryRowContext(ctx, "SELECT is_local FROM servers WHERE id=? AND status<>'removed'", id).Scan(&local); err != nil {
			a.resourceError(w, err)
			return
		}
		if local && input.PublicHost != nil {
			a.resourceError(w, invalid("public_host", "请在设置页修改本机公网地址。"))
			return
		}
		if local && input.AutoUpgrade != nil {
			a.resourceError(w, invalid("auto_upgrade", "请在主控的「状态」页点击「升级主控」。自动跟随升级仅用于远端服务器。"))
			return
		}
	}
	// These are subscription metadata or node preferences delivered with the
	// next heartbeat; neither affects a running kernel.
	s, err := scanServer(conn.QueryRowContext(ctx, `UPDATE servers SET name=coalesce(?,name),
		public_host=coalesce(?,public_host),auto_upgrade=coalesce(?,auto_upgrade),updated_at=unixepoch()
		WHERE id=? AND status<>'removed' RETURNING `+
		serverColumns, input.Name, input.PublicHost, input.AutoUpgrade, id), nil)
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
	if err := validateServerName(input.Name); err != nil {
		a.resourceError(w, err)
		return "", false
	}
	return strings.TrimSpace(input.Name), true
}

func validateServerName(input string) error {
	name := strings.TrimSpace(input)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(input, unicode.IsControl) {
		return invalid("name", "请输入 1–64 个字符的服务器名称，且不要包含控制字符。")
	}
	return nil
}
