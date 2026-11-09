package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type inboundRecord struct {
	ID        int64           `json:"id"`
	Tag       string          `json:"tag"`
	Protocol  string          `json:"protocol"`
	Port      int             `json:"port"`
	Enabled   bool            `json:"enabled"`
	Remark    string          `json:"remark"`
	Options   json.RawMessage `json:"options"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

const inboundColumns = "id,tag,protocol,port,enabled,remark,options,created_at,updated_at"

func scanInbound(row rowScanner) (inboundRecord, error) {
	var in inboundRecord
	var raw string
	if err := row.Scan(&in.ID, &in.Tag, &in.Protocol, &in.Port, &in.Enabled, &in.Remark, &raw, &in.CreatedAt, &in.UpdatedAt); err != nil {
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
func (a *api) prepareInbound(ctx context.Context, protocol string, patch json.RawMessage) (json.RawMessage, error) {
	target := ""
	if protocol == "vless_reality" {
		fields := optionFields(patch)
		if raw, exists := fields["server_name"]; exists {
			if json.Unmarshal(raw, &target) != nil || !reality.ValidServer(target) {
				return nil, invalid("options.server_name", "请填写公网域名或 IP，不要包含 URL 路径或端口。")
			}
		} else {
			targets := reality.Recommend(ctx, a.opts.RealityProbe)
			if len(targets) == 0 {
				return nil, conflict("reality_target_unavailable", "options.server_name", "没有检测到合适的 Reality 目标，请检查服务器网络，或手动填写并检测 server_name 后重试。")
			}
			target = targets[0].Server
		}
	}
	defaults, err := defaultInboundOptions(protocol, target)
	if err != nil {
		return nil, err
	}
	return mergeInboundOptions(protocol, defaults, patch)
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
	options, err := a.prepareInbound(r.Context(), *input.Protocol, input.Options)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	sockets, err := a.opts.ListeningPorts(r.Context())
	if err != nil {
		a.portInspectionError(w)
		return
	}
	var result inboundRecord
	if !a.applyResource(w, r, func(ctx context.Context, tx *sql.Tx) error {
		reservations, err := a.inboundReservations(ctx, tx, sockets)
		if err != nil {
			return err
		}
		in := inboundRecord{Protocol: *input.Protocol, Enabled: true, Options: options, CreatedAt: time.Now().Unix()}
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
		// Options have already been prepared outside the transaction; patching
		// scalar fields here does not perform TLS probes or external I/O.
		patch := input
		patch.Options = nil
		if err := patch.patch(&in); err != nil {
			return err
		}
		if err := validateInbound(in); err != nil {
			return err
		}
		if err := reservations.check(in); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO inbounds (tag,protocol,port,enabled,remark,options,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)", in.Tag, in.Protocol, in.Port, in.Enabled, in.Remark, string(in.Options), in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return fmt.Errorf("create inbound: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("read created inbound ID: %w", err)
		}
		in, err = readInbound(ctx, tx, id)
		if err == nil {
			result, err = publicInbound(in)
		}
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
	var sockets map[sysinfo.Socket]bool
	if !deleted {
		sockets, err = a.opts.ListeningPorts(r.Context())
		if err != nil {
			a.portInspectionError(w)
			return
		}
	}
	var result inboundRecord
	if !a.applyResource(w, r, func(ctx context.Context, tx *sql.Tx) error {
		in, err := readInbound(ctx, tx, id)
		if err != nil {
			return err
		}
		if deleted {
			_, err := tx.ExecContext(ctx, "DELETE FROM inbounds WHERE id=?", id)
			if err != nil {
				return fmt.Errorf("delete inbound: %w", err)
			}
			return nil
		}
		if err := input.patch(&in); err != nil {
			return err
		}
		if err := validateInbound(in); err != nil {
			return err
		}
		reservations, err := a.inboundReservations(ctx, tx, sockets)
		if err != nil {
			return err
		}
		if err := reservations.check(in); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE inbounds SET tag=?,port=?,enabled=?,remark=?,options=?,updated_at=? WHERE id=?", in.Tag, in.Port, in.Enabled, in.Remark, string(in.Options), time.Now().Unix(), id)
		if err != nil {
			return fmt.Errorf("update inbound: %w", err)
		}
		in, err = readInbound(ctx, tx, id)
		if err == nil {
			result, err = publicInbound(in)
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
func (a *api) inboundUpdate(w http.ResponseWriter, r *http.Request) {
	var input inboundInput
	if !decodeResource(w, r, &input) {
		return
	}
	a.mutateInbound(w, r, false, input)
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
