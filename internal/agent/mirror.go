package agent

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
)

type mirrorInbound struct {
	DesiredInbound
	certificateID string
}

func mirrorInbounds(ctx context.Context, tx *sql.Tx) ([]mirrorInbound, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,agent_key_id,agent_cert_id,protocol,options
		FROM inbounds WHERE server_id=(SELECT id FROM servers WHERE is_local=1) ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read mirrored inbounds: %w", err)
	}
	defer rows.Close()
	inbounds := []mirrorInbound{}
	for rows.Next() {
		var in mirrorInbound
		var raw string
		if err := rows.Scan(&in.ID, &in.KeyID, &in.certificateID, &in.Protocol, &raw); err != nil {
			return nil, fmt.Errorf("scan mirrored inbound: %w", err)
		}
		in.Options = json.RawMessage(raw)
		inbounds = append(inbounds, in)
	}
	return inbounds, rows.Err()
}

func (n *Node) publicKeys(ctx context.Context) ([]InboundKeys, error) {
	tx, err := n.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin reading node public material: %w", err)
	}
	defer tx.Rollback()
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return nil, err
	}
	inbounds, err := mirrorInbounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	keys := make([]InboundKeys, 0, len(inbounds))
	for _, in := range inbounds {
		if in.KeyID == "" {
			return nil, fmt.Errorf("missing mirrored inbound identity")
		}
		k := InboundKeys{InboundID: in.ID, KeyID: in.KeyID}
		switch in.Protocol {
		case "vless_reality":
			var options confgen.RealityOptions
			if json.Unmarshal(in.Options, &options) != nil {
				return nil, fmt.Errorf("invalid mirrored Reality options")
			}
			public, short := localRealityMaterial(options)
			if public == "" || public != options.PublicKey || short == "" {
				return nil, fmt.Errorf("missing node Reality material")
			}
			k.PublicKey, k.ShortID = public, short
		case "hysteria2":
			var options confgen.Hysteria2Options
			if json.Unmarshal(in.Options, &options) != nil {
				return nil, fmt.Errorf("invalid mirrored Hysteria2 options")
			}
			pem, fingerprint, err := n.Manager.AgentCertificate(in.ID, options.SNI, in.certificateID)
			if err != nil {
				return nil, err
			}
			k.CertPEM, k.CertSHA256 = string(pem), fingerprint
		default:
			return nil, fmt.Errorf("unsupported mirrored protocol")
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func localRealityMaterial(options confgen.RealityOptions) (string, string) {
	public, _ := reality.PublicKey(options.PrivateKey)
	var short string
	if len(options.ShortIDs) == 1 {
		if raw, err := hex.DecodeString(options.ShortIDs[0]); err == nil && len(raw) == 8 {
			short = options.ShortIDs[0]
		}
	}
	return public, short
}

func (n *Node) replaceMirror(ctx context.Context, tx *sql.Tx, desired Desired) error {
	if err := n.verifyIdentity(ctx, tx); err != nil {
		return err
	}
	local, err := db.LocalServerID(ctx, tx)
	if err != nil {
		return err
	}
	current, err := mirrorInbounds(ctx, tx)
	if err != nil {
		return err
	}
	old := make(map[int64]mirrorInbound, len(current))
	for _, in := range current {
		old[in.ID] = in
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM inbounds WHERE server_id=?", local); err != nil {
		return fmt.Errorf("replace mirrored inbounds: %w", err)
	}
	for _, in := range desired.Inbounds {
		previous := old[in.ID]
		if in.ID <= 0 || in.KeyID == "" {
			return fmt.Errorf("invalid desired inbound identity")
		}
		if previous.KeyID != in.KeyID || previous.Protocol != in.Protocol {
			previous = mirrorInbound{}
		}
		options, err := nodeOptions(in, previous)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbounds
			(id,server_id,tag,protocol,port,enabled,remark,options,created_at,updated_at,agent_key_id,agent_cert_id)
			VALUES (?,?,?,?,?,?,?,?,unixepoch(),unixepoch(),?,?)`, in.ID, local, in.Tag, in.Protocol, in.Port,
			in.Enabled, in.Remark, string(options), in.KeyID, previous.certificateID); err != nil {
			return fmt.Errorf("save mirrored inbound: %w", err)
		}
	}
	if err := mirrorUsers(ctx, tx, desired.Users); err != nil {
		return err
	}
	if err := db.SetSetting(ctx, tx, db.BlockBittorrent, desired.Route.BlockBittorrent); err != nil {
		return err
	}
	if err := replaceExitMirror(ctx, tx, desired.Exit); err != nil {
		return err
	}
	return db.SetSetting(ctx, tx, db.AgentAppliedGeneration, desired.Generation)
}

func nodeOptions(in DesiredInbound, previous mirrorInbound) ([]byte, error) {
	switch in.Protocol {
	case "vless_reality":
		var options, old confgen.RealityOptions
		if json.Unmarshal(in.Options, &options) != nil {
			return nil, fmt.Errorf("invalid desired Reality options")
		}
		_ = json.Unmarshal(previous.Options, &old)
		public, short := localRealityMaterial(old)
		options.PrivateKey, options.PublicKey = old.PrivateKey, public
		if public == "" {
			pair, err := reality.GenerateKeyPair()
			if err != nil {
				return nil, err
			}
			options.PrivateKey, options.PublicKey = pair.PrivateKey, pair.PublicKey
		}
		if short == "" {
			var err error
			short, err = reality.GenerateShortID()
			if err != nil {
				return nil, err
			}
		}
		options.ShortIDs = []string{short}
		return json.Marshal(options)
	case "hysteria2":
		var options confgen.Hysteria2Options
		if json.Unmarshal(in.Options, &options) != nil {
			return nil, fmt.Errorf("invalid desired Hysteria2 options")
		}
		return json.Marshal(options)
	default:
		return nil, fmt.Errorf("unsupported desired inbound protocol")
	}
}

func mirrorUsers(ctx context.Context, tx *sql.Tx, users []DesiredUser) error {
	type counters struct {
		uuid, token     string
		up, down, epoch int64
	}
	previousEpoch, err := db.GetSetting(ctx, tx, db.AgentTrafficEpoch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	epoch := previousEpoch
	old := make(map[int64]counters)
	rows, err := tx.QueryContext(ctx, "SELECT id,uuid,sub_token,traffic_up,traffic_down,traffic_epoch FROM users")
	if err != nil {
		return fmt.Errorf("read mirrored users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c counters
		if err := rows.Scan(&id, &c.uuid, &c.token, &c.up, &c.down, &c.epoch); err != nil {
			return fmt.Errorf("scan mirrored user: %w", err)
		}
		old[id] = c
		epoch = max(epoch, c.epoch)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read mirrored users: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM users"); err != nil {
		return fmt.Errorf("replace mirrored users: %w", err)
	}
	for _, user := range users {
		c := old[user.ID]
		if c.token == "" || c.uuid != user.UUID {
			token, err := auth.RandomToken(32)
			if err != nil {
				return err
			}
			if epoch == math.MaxInt64 {
				return fmt.Errorf("node traffic epoch exhausted")
			}
			epoch++
			c = counters{token: token, epoch: epoch}
		} else {
			delete(old, user.ID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users
			(id,name,uuid,password,sub_token,traffic_up,traffic_down,traffic_epoch,created_at)
			VALUES (?,?,?,?,?,?,?,?,unixepoch())`, user.ID, user.Name, user.UUID, user.Password, c.token, c.up, c.down, c.epoch); err != nil {
			return fmt.Errorf("save mirrored user: %w", err)
		}
	}
	for id, c := range old {
		if c.up == 0 && c.down == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_retired_traffic (user_id,epoch,up,down) VALUES (?,?,?,?)`,
			id, c.epoch, c.up, c.down); err != nil {
			return fmt.Errorf("retain retired node counters: %w", err)
		}
	}
	if epoch != previousEpoch {
		return db.SetSetting(ctx, tx, db.AgentTrafficEpoch, epoch)
	}
	return nil
}
