package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type inboundRecord struct {
	ID        int64           `json:"id"`
	ServerID  int64           `json:"server_id"`
	Tag       string          `json:"tag"`
	Protocol  string          `json:"protocol"`
	Port      int             `json:"port"`
	Enabled   bool            `json:"enabled"`
	Remark    string          `json:"remark"`
	Options   json.RawMessage `json:"options"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
	KeyStatus string          `json:"key_status,omitempty"`
	keyID     string
	keysReady bool
}

const inboundColumns = "id,tag,protocol,port,enabled,remark,options,created_at,updated_at,server_id,agent_key_id,agent_keys_ready"

func scanInbound(row rowScanner) (inboundRecord, error) {
	var in inboundRecord
	var raw string
	if err := row.Scan(&in.ID, &in.Tag, &in.Protocol, &in.Port, &in.Enabled, &in.Remark, &raw, &in.CreatedAt, &in.UpdatedAt, &in.ServerID, &in.keyID, &in.keysReady); err != nil {
		return in, fmt.Errorf("read inbound: %w", err)
	}
	in.Options = json.RawMessage(raw)
	return in, nil
}
func readInbound(ctx context.Context, q rowReader, id int64) (inboundRecord, error) {
	return scanInbound(q.QueryRowContext(ctx, "SELECT "+inboundColumns+" FROM inbounds WHERE id=?", id))
}
func (a *api) inboundList(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	rows, err := conn.QueryContext(r.Context(), "SELECT "+inboundColumns+" FROM inbounds ORDER BY id")
	if err != nil {
		a.resourceError(w, fmt.Errorf("list inbounds: %w", err))
		return
	}
	defer rows.Close()
	result := []inboundRecord{}
	for rows.Next() {
		in, err := scanInbound(rows)
		if err == nil {
			in, err = publicInbound(in)
		}
		if err != nil {
			a.resourceError(w, err)
			return
		}
		result = append(result, in)
	}
	if err := rows.Err(); err != nil {
		a.resourceError(w, fmt.Errorf("list inbounds: %w", err))
		return
	}
	writeJSON(w, 200, result)
}
func (a *api) inboundGet(w http.ResponseWriter, r *http.Request) {
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
	in, err := readInbound(r.Context(), conn, id)
	if err == nil {
		in, err = publicInbound(in)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, in)
}

type inboundInput struct {
	ServerID *int64          `json:"server_id"`
	Protocol *string         `json:"protocol"`
	Port     *int            `json:"port"`
	Tag      *string         `json:"tag"`
	Remark   *string         `json:"remark"`
	Enabled  *bool           `json:"enabled"`
	Options  json.RawMessage `json:"options"`
}

func (input inboundInput) patch(in *inboundRecord) error {
	if input.Protocol != nil && *input.Protocol != in.Protocol {
		return invalid("protocol", "修改时不能切换协议，请新建另一种协议的入站。")
	}
	if input.Port != nil {
		in.Port = *input.Port
	}
	if input.Tag != nil {
		in.Tag = *input.Tag
	}
	if input.Remark != nil {
		in.Remark = *input.Remark
	}
	if input.Enabled != nil {
		in.Enabled = *input.Enabled
	}
	var err error
	in.Options, err = mergeInboundOptions(in.Protocol, in.Options, input.Options)
	return err
}
func (a *api) prepareInbound(ctx context.Context, protocol string, patch json.RawMessage, local bool) (json.RawMessage, error) {
	if !local {
		if err := rejectRemoteKeyOptions(patch); err != nil {
			return nil, err
		}
	}
	target := ""
	if protocol == "vless_reality" {
		fields := optionFields(patch)
		if raw, exists := fields["server_name"]; exists {
			if json.Unmarshal(raw, &target) != nil || !reality.ValidServer(target) {
				return nil, invalid("options.server_name", "请填写公网域名或 IP，不要包含 URL 路径或端口。")
			}
		} else if local {
			targets := reality.Recommend(ctx, a.opts.RealityProbe)
			if len(targets) == 0 {
				return nil, conflict("reality_target_unavailable", "options.server_name", "没有检测到合适的 Reality 目标，请检查服务器网络，或手动填写并检测 server_name 后重试。")
			}
			target = targets[0].Server
		}
	}
	defaults, err := defaultInboundOptions(protocol, target, local)
	if err != nil {
		return nil, err
	}
	return mergeInboundOptions(protocol, defaults, patch)
}

type inboundServer struct {
	id     int64
	local  bool
	status string
}

func lookupInboundServer(ctx context.Context, q rowReader, id *int64) (inboundServer, error) {
	var s inboundServer
	var err error
	const query = "SELECT id,is_local,status FROM servers WHERE "
	if id == nil {
		err = q.QueryRowContext(ctx, query+"is_local=1").Scan(&s.id, &s.local, &s.status)
	} else {
		err = q.QueryRowContext(ctx, query+"id=?", *id).Scan(&s.id, &s.local, &s.status)
	}
	if id != nil && errors.Is(err, sql.ErrNoRows) {
		return s, unavailableServer()
	}
	if err != nil {
		return s, fmt.Errorf("read inbound server: %w", err)
	}
	return s, nil
}

func unavailableServer() error {
	return &resourceError{422, "server_unavailable", "server_id", "请选择本机，或已完成首次心跳且未被移除的服务器。"}
}

func checkInboundServer(s inboundServer) error {
	if !s.local && s.status != "online" && s.status != "offline" {
		return unavailableServer()
	}
	return nil
}

func (a *api) writeInbound(w http.ResponseWriter, r *http.Request, local bool, mutate singbox.Mutation) bool {
	if local {
		return a.applyResource(w, r, mutate)
	}
	conn, err := a.writeDB()
	if err == nil {
		err = transactInbound(r.Context(), conn, mutate)
	}
	if err != nil {
		a.resourceError(w, err)
		return false
	}
	w.Header().Set("X-Boxpanel-Apply", "queued")
	return true
}

func transactInbound(ctx context.Context, conn *sql.DB, mutate singbox.Mutation) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remote inbound change: %w", err)
	}
	defer tx.Rollback()
	if err := mutate(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remote inbound change: %w", err)
	}
	return nil
}

func bumpInboundGeneration(ctx context.Context, tx *sql.Tx, first, second int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers SET desired_generation=desired_generation+1
		WHERE id IN (?,?) AND is_local=0 AND status<>'removed'`, first, second)
	if err != nil {
		return fmt.Errorf("advance inbound desired generation: %w", err)
	}
	return nil
}

func (a *api) inboundCreate(w http.ResponseWriter, r *http.Request) {
	var input inboundInput
	if !decodeResource(w, r, &input) {
		return
	}
	if input.Protocol == nil {
		a.resourceError(w, invalid("protocol", "请选择 vless_reality 或 hysteria2。"))
		return
	}
	ctx := r.Context()
	reader, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	server, err := lookupInboundServer(ctx, reader, input.ServerID)
	if err == nil {
		err = checkInboundServer(server)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	options, err := a.prepareInbound(ctx, *input.Protocol, input.Options, server.local)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var sockets map[sysinfo.Socket]bool
	if server.local {
		sockets, err = a.opts.ListeningPorts(ctx)
		if err != nil {
			a.portInspectionError(w)
			return
		}
	}
	var result inboundRecord
	if !a.writeInbound(w, r, server.local, func(ctx context.Context, tx *sql.Tx) error {
		server, err := lookupInboundServer(ctx, tx, &server.id)
		if err != nil {
			return err
		}
		if err := checkInboundServer(server); err != nil {
			return err
		}
		reservations, err := a.inboundReservations(ctx, tx, sockets, server)
		if err != nil {
			return err
		}
		in := inboundRecord{ServerID: server.id, Protocol: *input.Protocol, Enabled: true, Options: options, CreatedAt: time.Now().Unix()}
		in.UpdatedAt = in.CreatedAt
		if input.Port != nil {
			in.Port = *input.Port
		} else {
			in.Port, err = reservations.suggest(in.Protocol)
			if err != nil {
				return err
			}
		}
		in.Tag = strings.ReplaceAll(in.Protocol, "_", "-") + fmt.Sprintf("-%d", in.Port)
		// Options have already been prepared outside the transaction.
		patch := input
		patch.Options = nil
		if err := patch.patch(&in); err != nil {
			return err
		}
		if err := validateInbound(in, server.local); err != nil {
			return err
		}
		if err := reservations.check(in); err != nil {
			return err
		}
		if !server.local {
			in.keyID, err = auth.RandomToken(16)
			if err != nil {
				return err
			}
		}
		in, err = scanInbound(tx.QueryRowContext(ctx, `INSERT INTO inbounds
			(server_id,tag,protocol,port,enabled,remark,options,created_at,updated_at,agent_key_id)
			VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING `+inboundColumns, in.ServerID, in.Tag, in.Protocol, in.Port,
			in.Enabled, in.Remark, string(in.Options), in.CreatedAt, in.UpdatedAt, in.keyID))
		if err != nil {
			return fmt.Errorf("create inbound: %w", err)
		}
		if !server.local {
			if err := bumpInboundGeneration(ctx, tx, server.id, server.id); err != nil {
				return err
			}
		}
		result, err = publicInbound(in)
		return err
	}) {
		return
	}
	writeJSON(w, 201, result)
}

func (a *api) portInspectionError(w http.ResponseWriter) {
	writeError(w, 500, "internal_error", "无法检查端口占用", "请安装 iproute2 并确认可以运行 ss，再重试。")
}

func (a *api) mutateInbound(w http.ResponseWriter, r *http.Request, deleted bool, input inboundInput) {
	id, err := resourceID(r)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	ctx := r.Context()
	reader, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	record, err := readInbound(ctx, reader, id)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	source, err := lookupInboundServer(ctx, reader, &record.ServerID)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	targetID := &source.id
	if input.ServerID != nil {
		targetID = input.ServerID
	}
	target, err := lookupInboundServer(ctx, reader, targetID)
	if err == nil && !deleted {
		err = checkInboundServer(target)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var sockets map[sysinfo.Socket]bool
	if !deleted && target.local {
		sockets, err = a.opts.ListeningPorts(ctx)
		if err != nil {
			a.portInspectionError(w)
			return
		}
	}
	var result inboundRecord
	if !a.writeInbound(w, r, source.local || target.local, func(ctx context.Context, tx *sql.Tx) error {
		in, err := readInbound(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.ServerID != source.id {
			return conflict("inbound_server_changed", "server_id", "入站所属服务器已变更，请刷新后重试。")
		}
		if deleted {
			if _, err := tx.ExecContext(ctx, "DELETE FROM inbounds WHERE id=?", id); err != nil {
				return fmt.Errorf("delete inbound: %w", err)
			}
			if !source.local {
				return bumpInboundGeneration(ctx, tx, source.id, source.id)
			}
			return nil
		}
		target, err := lookupInboundServer(ctx, tx, targetID)
		if err != nil {
			return err
		}
		if err := checkInboundServer(target); err != nil {
			return err
		}
		previous := in
		if err := patchInbound(&in, input, target); err != nil {
			return err
		}
		if err := validateInbound(in, target.local); err != nil {
			return err
		}
		reservations, err := a.inboundReservations(ctx, tx, sockets, target)
		if err != nil {
			return err
		}
		if err := reservations.check(in); err != nil {
			return err
		}
		if (!source.local || !target.local) && (in.ServerID != previous.ServerID || in.Tag != previous.Tag ||
			in.Port != previous.Port || in.Enabled != previous.Enabled || in.Remark != previous.Remark ||
			in.keyID != previous.keyID || !bytes.Equal(in.Options, previous.Options)) {
			if err := bumpInboundGeneration(ctx, tx, source.id, target.id); err != nil {
				return err
			}
		}
		in, err = scanInbound(tx.QueryRowContext(ctx, `UPDATE inbounds SET server_id=?,tag=?,port=?,enabled=?,
			remark=?,options=?,updated_at=?,agent_key_id=?,agent_keys_ready=? WHERE id=? RETURNING `+inboundColumns,
			in.ServerID, in.Tag, in.Port, in.Enabled, in.Remark, string(in.Options), time.Now().Unix(), in.keyID, in.keysReady, id))
		if err != nil {
			return fmt.Errorf("update inbound: %w", err)
		}
		result, err = publicInbound(in)
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

func patchInbound(in *inboundRecord, input inboundInput, target inboundServer) error {
	oldOptions := in.Options
	moved := target.id != in.ServerID
	in.ServerID = target.id
	if !target.local {
		if err := rejectRemoteKeyOptions(input.Options); err != nil {
			return err
		}
	}
	if moved && in.Protocol == "vless_reality" {
		var options confgen.RealityOptions
		if json.Unmarshal(in.Options, &options) != nil {
			return fmt.Errorf("decode transferred Reality options")
		}
		options.PrivateKey, options.PublicKey, options.ShortIDs = "", "", nil
		if target.local {
			if err := generateLocalReality(&options); err != nil {
				return err
			}
		}
		var err error
		in.Options, err = json.Marshal(options)
		if err != nil {
			return fmt.Errorf("encode transferred Reality options: %w", err)
		}
	}
	if err := input.patch(in); err != nil {
		return err
	}
	rotate := moved
	if in.Protocol == "hysteria2" && (moved || !target.local && len(input.Options) != 0) {
		old, current := optionFields(oldOptions), optionFields(in.Options)
		rotate = rotate || !bytes.Equal(old["sni"], current["sni"])
		if rotate {
			delete(current, "fingerprint")
			delete(current, "certificate")
		} else if in.keysReady {
			current["fingerprint"], current["certificate"] = old["fingerprint"], old["certificate"]
		}
		var err error
		in.Options, err = json.Marshal(current)
		if err != nil {
			return fmt.Errorf("encode transferred Hysteria2 options: %w", err)
		}
	}
	if rotate {
		in.keyID, in.keysReady = "", false
		if !target.local {
			id, err := auth.RandomToken(16)
			if err != nil {
				return err
			}
			in.keyID = id
		}
	}
	return nil
}

func (a *api) inboundUpdate(w http.ResponseWriter, r *http.Request) {
	var input inboundInput
	if decodeResource(w, r, &input) {
		a.mutateInbound(w, r, false, input)
	}
}
func (a *api) inboundToggle(w http.ResponseWriter, r *http.Request) {
	enabled, ok := decodeToggle(w, r)
	if ok {
		a.mutateInbound(w, r, false, inboundInput{Enabled: enabled})
	}
}
func (a *api) inboundDelete(w http.ResponseWriter, r *http.Request) {
	if noBody(w, r) {
		a.mutateInbound(w, r, true, inboundInput{})
	}
}
