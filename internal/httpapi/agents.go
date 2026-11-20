package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

func (a *api) agentEndpoints() http.Handler {
	routes := map[string]string{
		agent.Prefix + "/join": http.MethodPost, agent.Prefix + "/heartbeat": http.MethodPost,
		agent.Prefix + "/desired": http.MethodGet, agent.Prefix + "/inbound-keys": http.MethodPost,
		agent.TrafficPath: http.MethodPost,
	}
	return middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ok := routes[r.URL.Path]
		if !ok {
			notFound(w, r)
			return
		}
		r.Pattern = r.URL.Path
		if r.URL.Path != agent.Prefix+"/join" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, 405, "method_not_allowed", "请求方法不支持", "请使用接口文档指定的请求方法。")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.TLS == nil {
			a.rejectAgent(w, r.URL.Path == agent.Prefix+"/join")
			return
		}
		if r.URL.Path == agent.Prefix+"/join" {
			a.agentJoin(w, r)
		} else {
			a.agentAuthenticated(w, r)
		}
	}), a.opts.Logger, routes)
}

func (a *api) rejectAgent(w http.ResponseWriter, join bool) {
	if join {
		writeError(w, 401, "invalid_join_token", "加入令牌无效或已过期，请在主控重新生成", "复制新的加入命令并在节点上执行。")
		return
	}
	writeError(w, 401, "agent_unauthorized", "节点认证失败", "请检查节点时钟和主控中的服务器记录，再重试。")
}

func (a *api) agentJoin(w http.ResponseWriter, r *http.Request) {
	peer := peerIP(r.RemoteAddr)
	if allowed, retry := a.opts.Agents.AllowJoin(peer); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeError(w, 429, "rate_limited", "加入请求过多", "请等待 Retry-After 指定的秒数后重试。")
		return
	}
	var in agent.Registration
	if !decode(w, r, &in) {
		return
	}
	id, err := a.opts.Agents.Register(r.Context(), in, peer)
	switch err {
	case nil:
		writeJSON(w, 201, id)
	case agent.ErrUnauthorized:
		a.rejectAgent(w, true)
	case agent.ErrRegistration:
		writeError(w, 400, "invalid_argument", "节点信息无效", "请检查主机名、IP 地址和 boxpanel 版本后重试。")
	case agent.ErrPublicIP:
		writeError(w, 422, "agent_public_ip_required", agent.ErrPublicIP.Error(), "请在加入命令末尾加上 --public-ip <公网 IP> 后重试。")
	default:
		a.resourceError(w, err)
	}
}

func (a *api) agentAuthenticated(w http.ResponseWriter, r *http.Request) {
	limit := int64(8 * 1024)
	if r.URL.Path == agent.TrafficPath {
		limit = agent.MaxTrafficBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, tooLarge := err.(*http.MaxBytesError); r.URL.Path == agent.TrafficPath && tooLarge {
			writeError(w, 413, "invalid_argument", "节点流量上报超过大小限制", "请将每批流量上报拆分至 1 MiB 以内后重试。")
			return
		}
		a.rejectAgent(w, false)
		return
	}
	path := strings.TrimSuffix(a.cookiePath, "/") + r.URL.EscapedPath()
	identity, err := a.opts.Agents.Authenticate(r.Context(), r.Method, path, r.Header, body)
	if err == agent.ErrUnauthorized {
		a.rejectAgent(w, false)
		return
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	switch r.URL.Path {
	case agent.TrafficPath:
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var report agent.TrafficReport
		if err != nil || media != "application/json" || strictObject(body, &report) != nil {
			a.agentOperationError(w, agent.ErrInvalidTraffic)
			return
		}
		if err := a.opts.Agents.RecordTraffic(r.Context(), identity, report); err != nil {
			a.agentOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case agent.Prefix + "/desired":
		if len(body) != 0 {
			a.rejectAgent(w, false)
			return
		}
		out, err := a.opts.Agents.Desired(r.Context(), identity)
		if err == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		a.agentOperationError(w, err)
	case agent.Prefix + "/inbound-keys":
		var in agent.InboundKeys
		if !decodeResource(w, r, &in) {
			return
		}
		if err := a.opts.Agents.PublishKeys(r.Context(), identity, in); err != nil {
			a.agentOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case agent.Prefix + "/heartbeat":
		var in agent.Heartbeat
		if !decodeResource(w, r, &in) {
			return
		}
		if !agent.ValidMetadata(in.Version, 128) || in.Uptime < 0 || !agent.ValidSyncError(in.SyncError) ||
			in.AppliedGeneration != nil && *in.AppliedGeneration < 0 || in.AppliedGeneration == nil && in.SyncError != "" {
			writeError(w, 400, "invalid_argument", "节点心跳内容无效", "请检查节点 boxpanel 版本后重试。")
			return
		}
		reply, err := a.opts.Agents.RecordHeartbeat(r.Context(), identity, in)
		if err != nil {
			a.agentOperationError(w, err)
			return
		}
		if in.AppliedGeneration == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, reply)
	}
}

func (a *api) agentOperationError(w http.ResponseWriter, err error) {
	switch err {
	case agent.ErrUnauthorized:
		a.rejectAgent(w, false)
	case agent.ErrStaleKeys:
		writeError(w, 409, "stale_inbound_keys", "入站密钥版本已变更", "请等待节点重新拉取配置后自动重试。")
	case agent.ErrInvalidKeys:
		writeError(w, 400, "invalid_inbound_keys", "入站公开材料无效", "请检查节点时间和证书，或更新节点版本后重试。")
	case agent.ErrInvalidTraffic:
		writeError(w, 400, "invalid_argument", "节点流量上报内容无效", "请检查节点版本；每批最多上报 10000 个用户，每个用户只能出现一次。")
	default:
		a.resourceError(w, err)
	}
}

func (a *api) joinTokenList(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.opts.Agents.Tokens(r.Context())
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, tokens)
}

func (a *api) joinTokenCreate(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || a.opts.AgentPin == nil {
		writeError(w, 409, "agent_requires_https", "加入节点需要 HTTPS", "请在启用 HTTPS 的主控面板生成加入命令。")
		return
	}
	name, ok := a.readServerName(w, r)
	if !ok {
		return
	}
	reader, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	ctx := r.Context()
	host, err := db.GetSetting(ctx, reader, db.PublicHost)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	port := a.opts.PanelPort
	if port == 0 {
		port, err = db.GetSetting(ctx, reader, db.WebPort)
		if err != nil {
			a.resourceError(w, err)
			return
		}
	}
	address := (app.Config{PublicHost: host, WebPort: port, WebPath: a.opts.WebPath, TLSMode: "acme"}).URL()
	pin, err := a.opts.AgentPin()
	if err != nil || pin == "" {
		if err == nil {
			err = fmt.Errorf("empty panel fallback pin")
		}
		a.resourceError(w, fmt.Errorf("read join certificate pin: %w", err))
		return
	}
	token, plaintext, err := a.opts.Agents.IssueToken(ctx, name)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 201, struct {
		agent.JoinToken
		Token     string `json:"token"`
		MasterURL string `json:"master_url"`
		MasterPin string `json:"master_pin"`
	}{token, plaintext, address, pin})
}

func (a *api) joinTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err == nil {
		err = a.opts.Agents.Revoke(r.Context(), id)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) serverRemove(w http.ResponseWriter, r *http.Request) {
	id, err := resourceID(r)
	if err == nil {
		err = a.opts.Agents.Remove(r.Context(), id)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
