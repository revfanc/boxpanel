package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type exitServer struct {
	inboundServer
	name, address, ports string
	protocol             int
	portSampleAt         int64
	syncPending          bool
	usage                exitlink.Totals
}

const exitServersSelect = `SELECT id,is_local,status,name,coalesce(nullif(exit_address,''),public_host),protocol,
	coalesce(exit_ports,''),exit_ports_at,desired_generation<>applied_generation,exit_traffic_up,exit_traffic_down
	FROM servers WHERE status<>'removed'`

func scanExitServer(row rowScanner) (exitServer, error) {
	var s exitServer
	err := row.Scan(&s.id, &s.local, &s.status, &s.name, &s.address, &s.protocol,
		&s.ports, &s.portSampleAt, &s.syncPending, &s.usage.Up, &s.usage.Down)
	ip := exitIP(s.address)
	s.address = ""
	if ip.IsValid() {
		s.address = ip.String()
	}
	return s, err
}

func exitIP(value string) netip.Addr {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !agent.ValidPublicIP(ip) {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func (s exitServer) supported() bool { return s.local || s.protocol >= agent.ExitProtocol }

func (s exitServer) candidateReason(entry exitServer, alreadyLinked bool, now int64) string {
	switch {
	case !s.supported():
		return "upgrade_required"
	case checkInboundServer(s.inboundServer) != nil:
		return "pending"
	case s.address == "":
		return "address_required"
	case entry.address != "" && exitIP(entry.address).Is4() != exitIP(s.address).Is4():
		return "address_family"
	case !alreadyLinked && !s.local && (s.ports == "" || s.portSampleAt < now-int64(exitlink.ProbeExpires/time.Second)):
		return "ports_pending"
	default:
		return ""
	}
}

type exitLinkView struct {
	ExitServerID int64 `json:"exit_server_id"`
	Port         int   `json:"port"`
	exitlink.Policy
	Status    string `json:"status"`
	CheckedAt *int64 `json:"checked_at"`
}

type exitServerChoice struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Reason  string `json:"unavailable_reason,omitempty"`
}

type exitServerView struct {
	ServerID   int64              `json:"server_id"`
	Address    string             `json:"address"`
	Supported  bool               `json:"supported"`
	Link       *exitLinkView      `json:"link"`
	Candidates []exitServerChoice `json:"candidates"`
	Forwarded  exitlink.Totals    `json:"forwarded"`
}

func (a *api) serverExit(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	out, err := a.readExitServer(r.Context(), id)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (a *api) readExitServer(ctx context.Context, id int64) (exitServerView, error) {
	out := exitServerView{ServerID: id, Candidates: []exitServerChoice{}}
	conn, err := a.readDB()
	if err != nil {
		return out, err
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("begin read of exit settings: %w", err)
	}
	defer tx.Rollback()
	entry, err := scanExitServer(tx.QueryRowContext(ctx, exitServersSelect+" AND id=?", id))
	if err != nil {
		return out, fmt.Errorf("read entry server: %w", err)
	}
	out.Address, out.Supported, out.Forwarded = entry.address, entry.supported(), entry.usage
	var link exitLinkView
	var rules string
	var checked int64
	var available *bool
	var policyDown bool
	err = tx.QueryRowContext(ctx, "SELECT exit_id,port,mode,failure,rules,checked_at,available,policy_down FROM exit_links WHERE entry_id=?", id).
		Scan(&link.ExitServerID, &link.Port, &link.Mode, &link.Failure, &rules, &checked, &available, &policyDown)
	if err == nil {
		if err := json.Unmarshal([]byte(rules), &link.Rules); err != nil {
			return out, fmt.Errorf("read saved exit rules; restore or reenter the rules")
		}
		out.Link = &link
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("read entry's exit settings: %w", err)
	}
	rows, err := tx.QueryContext(ctx, exitServersSelect+" AND id<>? ORDER BY is_local DESC,id", id)
	if err != nil {
		return out, fmt.Errorf("list exit candidates: %w", err)
	}
	defer rows.Close()
	now := time.Now().Unix()
	for rows.Next() {
		candidate, err := scanExitServer(rows)
		if err != nil {
			return out, fmt.Errorf("read exit candidate: %w", err)
		}
		selected := candidate.id == link.ExitServerID
		reason := candidate.candidateReason(entry, selected, now)
		out.Candidates = append(out.Candidates, exitServerChoice{candidate.id, candidate.name, candidate.address, reason})
		if selected {
			link.Status = exitStatus(entry, candidate, available, policyDown, checked, now, link.Failure)
			if checked > 0 {
				link.CheckedAt = &checked
			}
		}
	}
	return out, rows.Err()
}

func exitStatus(entry, target exitServer, available *bool, policyDown bool, checked, now int64, failure string) string {
	switch {
	case !entry.supported() || !target.supported():
		return "upgrade_required"
	case !entry.local && entry.syncPending:
		// A saved policy can differ from the entry's running policy until
		// the node acknowledges this generation, even if the probe is fresh.
		return "syncing"
	case available == nil:
		if !target.local && target.syncPending {
			return "syncing"
		}
		return "checking"
	case checked < now-int64(exitlink.ProbeExpires/time.Second):
		return "stale"
	case policyDown == *available:
		return "policy_pending"
	case *available:
		return "available"
	case failure == exitlink.Fallback:
		return "fallback"
	default:
		return "unavailable"
	}
}

type exitInput struct {
	ExitServerID int64 `json:"exit_server_id"`
	exitlink.Policy
}

func (a *api) saveServerExit(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var input exitInput
	if !decodeResource(w, r, &input) {
		return
	}
	if input.ExitServerID <= 0 || input.ExitServerID == id {
		a.resourceError(w, invalid("exit_server_id", "请选择与入口不同的已管理服务器。"))
		return
	}
	if err := input.Normalize(); err != nil {
		a.resourceError(w, invalid("rules", "请检查分流方式、故障策略和预设。每类自定义规则最多 128 条，请输入不含通配符、端口或网址的域名。"))
		return
	}
	rules, err := json.Marshal(input.Rules)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	reader, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	local, err := db.LocalServerID(r.Context(), reader)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var sockets map[sysinfo.Socket]bool
	var socketsErr error
	if input.ExitServerID == local {
		sockets, socketsErr = a.opts.ListeningPorts(r.Context())
	}
	if !a.writeExitChange(w, r, id, input.ExitServerID, false, func(ctx context.Context, tx *sql.Tx) error {
		entry, err := scanExitServer(tx.QueryRowContext(ctx, exitServersSelect+" AND id=?", id))
		if err != nil {
			return fmt.Errorf("read entry server for exit settings: %w", err)
		}
		target, err := scanExitServer(tx.QueryRowContext(ctx, exitServersSelect+" AND id=?", input.ExitServerID))
		if errors.Is(err, sql.ErrNoRows) {
			return unavailableServer()
		}
		if err != nil {
			return fmt.Errorf("read selected exit server: %w", err)
		}
		var previous int64
		err = tx.QueryRowContext(ctx, "SELECT exit_id FROM exit_links WHERE entry_id=?", id).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read previous exit server: %w", err)
		}
		same := previous == target.id
		if !entry.supported() || !target.supported() {
			return exitUnavailable("exit_upgrade_required", "请先升级入口或出口节点的 boxpanel，并等节点重新上报心跳。")
		}
		if err := checkInboundServer(entry.inboundServer); err != nil {
			return err
		}
		if entry.address == "" {
			return exitUnavailable("exit_address_required", "请先在入口服务器的「出口」标签填写链路公网 IP。")
		}
		switch target.candidateReason(entry, same, time.Now().Unix()) {
		case "pending":
			return unavailableServer()
		case "address_required":
			return exitUnavailable("exit_address_required", "请先在所选出口服务器的「出口」标签填写链路公网 IP。")
		case "address_family":
			return exitUnavailable("exit_address_family", "请让入口和出口的链路公网 IP 同为 IPv4 或同为 IPv6。")
		case "ports_pending":
			return exitUnavailable("exit_ports_pending", "请让出口节点上线并等待下一次端口上报，再保存出口设置。")
		}
		if same {
			if _, err := tx.ExecContext(ctx, "UPDATE exit_links SET mode=?,failure=?,rules=? WHERE entry_id=?", input.Mode, input.Failure, string(rules), id); err != nil {
				return fmt.Errorf("update exit policy: %w", err)
			}
			return nil
		}
		localSockets := sockets
		if target.local {
			if socketsErr != nil {
				return exitUnavailable("exit_ports_pending", "未能检查主控端口，请检查 ss 命令权限后重试。")
			}
		} else {
			ports, err := exitlink.DecodePorts(target.ports)
			if err != nil {
				return fmt.Errorf("decode exit server port inventory: %w", err)
			}
			localSockets = make(map[sysinfo.Socket]bool, len(ports))
			for _, port := range ports {
				localSockets[sysinfo.Socket{Port: port, Proto: "tcp"}] = true
			}
		}
		reservations, err := a.inboundReservations(ctx, tx, localSockets, target.inboundServer)
		if err != nil {
			return err
		}
		port, err := reservations.suggestExit()
		if err != nil {
			return err
		}
		key, err := exitlink.NewPassword()
		if err != nil {
			return err
		}
		if err := db.SetExitAddress(ctx, tx, entry.id, entry.address); err != nil {
			return err
		}
		if err := db.SetExitAddress(ctx, tx, target.id, target.address); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM exit_links WHERE entry_id=?", id); err != nil {
			return fmt.Errorf("replace former exit link: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exit_links (entry_id,exit_id,port,password,mode,failure,rules)
			VALUES (?,?,?,?,?,?,?)`, id, target.id, port, key, input.Mode, input.Failure, string(rules)); err != nil {
			return fmt.Errorf("save private exit link: %w", err)
		}
		return nil
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func exitUnavailable(code, hint string) error {
	return &resourceError{http.StatusUnprocessableEntity, code, "exit_server_id", hint}
}

func (a *api) deleteServerExit(w http.ResponseWriter, r *http.Request) {
	if !noBody(w, r) {
		return
	}
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if !a.writeExitChange(w, r, id, 0, false, func(ctx context.Context, tx *sql.Tx) error {
		var exists int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM servers WHERE id=? AND status<>'removed'", id).Scan(&exists); err != nil {
			return fmt.Errorf("read entry server for exit removal: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM exit_links WHERE entry_id=?", id); err != nil {
			return fmt.Errorf("disable exit link: %w", err)
		}
		return nil
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) saveExitAddress(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var input struct {
		Address string `json:"address"`
	}
	if !decodeResource(w, r, &input) {
		return
	}
	ip := exitIP(input.Address)
	if !ip.IsValid() {
		a.resourceError(w, invalid("address", "请填写入口和出口之间实际可用的公网 IPv4 或 IPv6 地址。"))
		return
	}
	if !a.writeExitChange(w, r, id, 0, true, func(ctx context.Context, tx *sql.Tx) error {
		server, err := scanExitServer(tx.QueryRowContext(ctx, exitServersSelect+" AND id=?", id))
		if err != nil {
			return fmt.Errorf("read server for exit address change: %w", err)
		}
		if !server.supported() {
			return exitUnavailable("exit_upgrade_required", "请先升级节点并等待心跳，再修改链路公网 IP。")
		}
		return db.SetExitAddress(ctx, tx, id, ip.String())
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errApplyLocalExit = errors.New("exit change needs local apply")

// Decide inside the writer transaction. A concurrent move involving the local
// server must not commit a DB-only change that leaves the local core stale.
func (a *api) writeExitChange(w http.ResponseWriter, r *http.Request, id, target int64, includeIncoming bool, mutate singbox.Mutation) bool {
	conn, err := a.writeDB()
	if err == nil {
		err = transactInbound(r.Context(), conn, func(ctx context.Context, tx *sql.Tx) error {
			var local bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers s WHERE s.is_local=1 AND (
				s.id IN (?,?) OR EXISTS(SELECT 1 FROM exit_links l WHERE
				(l.entry_id=? AND l.exit_id=s.id) OR (? AND l.exit_id=? AND l.entry_id=s.id))))`,
				id, target, id, includeIncoming, id).Scan(&local)
			if err != nil {
				return fmt.Errorf("determine local impact of exit change: %w", err)
			}
			if local {
				return errApplyLocalExit
			}
			return mutate(ctx, tx)
		})
	}
	if errors.Is(err, errApplyLocalExit) {
		return a.applyResource(w, r, mutate)
	}
	if err != nil {
		a.resourceError(w, err)
		return false
	}
	w.Header().Set("X-Boxpanel-Apply", "queued")
	return true
}
