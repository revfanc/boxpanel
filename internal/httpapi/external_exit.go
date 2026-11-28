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
	"github.com/revfanc/boxpanel/internal/exitlink"
)

type proxyInput struct {
	exitlink.Proxy
	UseSavedPassword bool `json:"use_saved_password,omitempty"`
}

type proxyView struct {
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
}

type proxyReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveExternal(ctx context.Context, q proxyReader, id int64, input proxyInput) (exitlink.Proxy, bool, error) {
	var local bool
	var protocol int
	var saved sql.NullString
	err := q.QueryRowContext(ctx, `SELECT s.is_local,s.protocol,l.external_proxy FROM servers s LEFT JOIN exit_links l ON l.entry_id=s.id WHERE s.id=? AND s.status<>'removed'`, id).Scan(&local, &protocol, &saved)
	if err != nil {
		return exitlink.Proxy{}, false, fmt.Errorf("read external exit entry: %w", err)
	}
	if !local && protocol < agent.ExternalProxyProtocol {
		return exitlink.Proxy{}, false, exitUnavailable("exit_upgrade_required", "请升级入口节点后等待下一次心跳，再使用外部代理。")
	}
	p := input.Proxy
	p.Host = strings.ToLower(strings.TrimSpace(p.Host))
	if input.UseSavedPassword {
		var old exitlink.Proxy
		if !saved.Valid || json.Unmarshal([]byte(saved.String), &old) != nil || old.Password == "" || p.Password != "" ||
			old.Host != p.Host || old.Port != p.Port || old.Type != p.Type || old.Username != p.Username {
			return p, local, invalid("password", "代理地址、协议或账号已变更，请重新输入密码。")
		}
		p.Password = old.Password
	}
	if err := p.Validate(); err != nil {
		return p, local, invalid("external_proxy", "请检查代理协议、主机和端口。需要认证时请同时填写用户名和密码。")
	}
	return p, local, nil
}

func saveExternalExit(ctx context.Context, tx *sql.Tx, id int64, input proxyInput, policy exitlink.Policy, rules string) error {
	p, _, err := resolveExternal(ctx, tx, id, input)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode external exit: %w", err)
	}
	// Removing the managed link also queues cleanup on its former exit server.
	if _, err := tx.ExecContext(ctx, "DELETE FROM exit_links WHERE entry_id=? AND exit_id IS NOT NULL", id); err != nil {
		return fmt.Errorf("detach previous exit: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO exit_links(entry_id,external_proxy,port,password,mode,failure,rules) VALUES(?,?,?,'',?,?,?)
		ON CONFLICT(entry_id) DO UPDATE SET external_proxy=excluded.external_proxy,port=excluded.port,mode=excluded.mode,failure=excluded.failure,rules=excluded.rules,
		revision=revision+(external_proxy<>excluded.external_proxy),checked_at=CASE WHEN external_proxy=excluded.external_proxy THEN checked_at ELSE 0 END,
		available=CASE WHEN external_proxy=excluded.external_proxy THEN available END,
		policy_down=CASE WHEN external_proxy=excluded.external_proxy THEN policy_down ELSE 0 END,
		incident_at=CASE WHEN external_proxy=excluded.external_proxy THEN incident_at ELSE 0 END`, id, string(encoded), p.Port, policy.Mode, policy.Failure, rules)
	if err != nil {
		return fmt.Errorf("save external exit: %w", err)
	}
	return nil
}

func (a *api) testExternalExit(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var input proxyInput
	if !decodeResource(w, r, &input) {
		return
	}
	q, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	p, local, err := resolveExternal(r.Context(), q, id, input)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var ip netip.Addr
	if local {
		ip, err = agent.TestProxy(r.Context(), p)
	} else if a.opts.Agents == nil {
		err = agent.ErrProxyTest
	} else {
		// Allow one normal 30s heartbeat plus the bounded HTTPS probe and reply.
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
		ip, err = a.opts.Agents.TestNodeProxy(ctx, id, p)
	}
	if errors.Is(err, agent.ErrProxyBusy) {
		writeError(w, 409, "proxy_test_busy", "此服务器正在测试代理", "请等待本次测试结束后重试。")
		return
	}
	if err != nil {
		writeError(w, 422, "proxy_test_failed", "未能通过代理取得公网 IP", "请检查入口是否在线、代理协议、认证、IP 白名单以及入口到代理的网络连接。")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"ip": ip.String()})
}
