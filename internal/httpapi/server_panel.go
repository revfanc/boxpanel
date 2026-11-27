package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func (a *api) registerServerPanel(register func(string, string, http.HandlerFunc, bool)) {
	register("GET", "/api/v1/overview", a.panelOverview, true)
	register("GET", "/api/v1/servers/{id}", a.serverGet, true)
	register("GET", "/api/v1/servers/{id}/exit", a.serverExit, true)
	register("PUT", "/api/v1/servers/{id}/exit", a.saveServerExit, true)
	register("DELETE", "/api/v1/servers/{id}/exit", a.deleteServerExit, true)
	register("PUT", "/api/v1/servers/{id}/exit-address", a.saveExitAddress, true)
	register("GET", "/api/v1/servers/{id}/status", a.serverStatus, true)
	register("GET", "/api/v1/servers/{id}/traffic", a.serverTraffic, true)
	register("GET", "/api/v1/servers/{id}/alerts", a.serverAlertSettings, true)
	register("PUT", "/api/v1/servers/{id}/alerts", a.alertSaveSettings, true)
	register("POST", "/api/v1/servers/{id}/restart", a.serverRestart, true)
	register("GET", "/api/v1/servers/{id}/logs", a.serverLogs, true)
	register("PUT", "/api/v1/servers/{id}/logs/{viewer}", a.serverWatchLogs, true)
	register("DELETE", "/api/v1/servers/{id}/logs/{viewer}", a.serverWatchLogs, true)
}

type serverStatusView struct {
	ServerID      int64               `json:"server_id"`
	Metrics       *sysinfo.Metrics    `json:"metrics"`
	MetricsAt     *int64              `json:"metrics_at"`
	Core          *sysinfo.Singbox    `json:"singbox"`
	SingboxActive *bool               `json:"singbox_active"`
	Restart       *agent.RestartState `json:"restart"`
}

func (a *api) serverStatus(w http.ResponseWriter, r *http.Request) {
	server, err := a.loadServer(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	result, err := a.readServerStatus(r.Context(), server)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) readServerStatus(ctx context.Context, server serverRecord) (serverStatusView, error) {
	out := serverStatusView{ServerID: server.ID, SingboxActive: server.SingboxActive, Restart: server.restart}
	var value sysinfo.Monitoring
	if server.IsLocal {
		if system, ok := a.opts.System.(interface {
			Monitor(context.Context) sysinfo.Monitoring
		}); ok {
			value = system.Monitor(ctx)
		} else if result, err := a.opts.System.Status(ctx); err == nil {
			value.Metrics = result.Metrics()
			if result.Supported {
				value.Core = &result.Singbox
			}
		}
		if value.Core != nil {
			out.SingboxActive = &value.Core.Running
		}
	} else if server.monitoring.Valid {
		if err := json.Unmarshal([]byte(server.monitoring.String), &value); err != nil {
			return out, fmt.Errorf("decode last node metrics: %w", err)
		}
		out.MetricsAt = server.LastSeenAt
	}
	out.Metrics, out.Core = value.Metrics, value.Core
	if out.Core != nil && out.SingboxActive != nil {
		out.Core.Running = *out.SingboxActive
	}
	return out, nil
}

func (a *api) serverRestart(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	server, err := a.loadServer(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if server.IsLocal {
		a.singboxRestart(w, r)
		return
	}
	if a.opts.Agents == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	result, err := a.opts.Agents.QueueRestart(r.Context(), server.ID)
	if errors.Is(err, agent.ErrMonitoringRequired) {
		a.nodeMonitoringRequired(w)
		return
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (a *api) nodeMonitoringRequired(w http.ResponseWriter) {
	writeError(w, 409, "node_monitoring_required", "节点尚未上报监控状态", "请确认节点已连接并升级至支持监控的版本，再尝试此操作。")
}

func (a *api) serverLogs(w http.ResponseWriter, r *http.Request) {
	server, err := a.loadServer(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if !server.IsLocal {
		if !server.monitoring.Valid || server.UpgradeRequired {
			a.nodeMonitoringRequired(w)
			return
		}
		if a.opts.Agents == nil {
			a.singboxError(w, singbox.ErrUnsupported)
			return
		}
		writeJSON(w, 200, a.opts.Agents.Logs(server.ID))
		return
	}
	if a.opts.Singbox == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	lines, err := a.opts.Singbox.Logs(r.Context(), agent.LogLines)
	if err != nil {
		a.singboxError(w, err)
		return
	}
	writeJSON(w, 200, agent.LogView{Ready: true, Lines: append([]string{}, lines...)})
}

func (a *api) serverWatchLogs(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	viewer := r.PathValue("viewer")
	if !agent.ValidLogViewer(viewer) {
		a.resourceError(w, invalid("viewer", "请重新打开服务器日志标签获取有效的查看标识。"))
		return
	}
	if a.opts.Agents == nil {
		a.singboxError(w, singbox.ErrUnsupported)
		return
	}
	if r.Method == http.MethodPut {
		switch err := a.opts.Agents.WatchLogs(r.Context(), id, viewer); {
		case errors.Is(err, agent.ErrMonitoringRequired):
			a.nodeMonitoringRequired(w)
			return
		case errors.Is(err, agent.ErrLogViewers):
			writeError(w, 429, "too_many_log_viewers", "查看该服务器日志的页面过多", "请关掉其他日志标签页，稍后重试。")
			return
		case err != nil:
			a.resourceError(w, err)
			return
		}
	} else {
		a.opts.Agents.UnwatchLogs(id, viewer)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) serverAlertSettings(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if !a.alertsAvailable(w) {
		return
	}
	result, err := a.opts.Alerts.ReadServerPreferences(r.Context(), id)
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *api) serverTraffic(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if !a.alertsAvailable(w) {
		return
	}
	ctx := r.Context()
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.resourceError(w, fmt.Errorf("begin server traffic read: %w", err))
		return
	}
	defer tx.Rollback()
	var local, usedAsExit bool
	var forwarded exitlink.Totals
	if err := tx.QueryRowContext(ctx, `SELECT is_local,exit_served,exit_traffic_up,exit_traffic_down
		FROM servers WHERE id=? AND status<>'removed'`, id).Scan(&local, &usedAsExit, &forwarded.Up, &forwarded.Down); err != nil {
		a.resourceError(w, fmt.Errorf("read server traffic owner: %w", err))
		return
	}
	result := serverTrafficView{ServerID: id, Users: []trafficUser{}}
	if usedAsExit {
		result.ExitTraffic = &forwarded
	}
	result.Billing, err = a.opts.Alerts.ReadBilling(ctx, tx, id)
	if err != nil {
		alertError(w, err)
		return
	}
	err = walkUserCycles(ctx, tx, func(user trafficUser, remotes map[int64]cycleBytes, remoteTotal cycleBytes) {
		usage := remotes[id]
		if local {
			usage = user.cycleBytes.remaining(remoteTotal)
		}
		if usage != (cycleBytes{}) {
			result.UserTraffic.add(usage)
			user.cycleBytes = usage
			result.Users = append(result.Users, user)
		}
	})
	if err != nil {
		a.resourceError(w, err)
		return
	}
	sortByTraffic(result.Users)
	writeJSON(w, 200, result)
}

type serverTrafficView struct {
	ServerID    int64            `json:"server_id"`
	Billing     alerts.Billing   `json:"billing"`
	UserTraffic cycleBytes       `json:"user_traffic"`
	Users       []trafficUser    `json:"users"`
	ExitTraffic *exitlink.Totals `json:"exit_traffic,omitempty"`
}
