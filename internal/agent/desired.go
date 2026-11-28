package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
)

var (
	ErrInvalidKeys = errors.New("invalid inbound public material")
	ErrStaleKeys   = errors.New("stale inbound material revision")
)

const agentIdentity = "id=? AND is_local=0 AND status<>'removed' AND agent_public_key=?"

func (m *Master) refreshDesired(ctx context.Context, tx *sql.Tx, identity authenticated, now int64) (int64, error) {
	// Time alone can change eligibility. Advance a per-server watermark
	// before reading either the advertised version or its user snapshot.
	var generation int64
	err := tx.QueryRowContext(ctx, `UPDATE servers SET
		desired_generation=desired_generation+EXISTS(
			SELECT 1 FROM users WHERE enabled=1 AND expire_at>min(desired_expiry_at,?) AND expire_at<=max(desired_expiry_at,?)
			AND (traffic_limit=0 OR (traffic_up<traffic_limit AND traffic_down<traffic_limit-traffic_up))),
		desired_expiry_at=?
		WHERE `+agentIdentity+` RETURNING desired_generation`, now, now, now, identity.id, identity.publicKey).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("refresh agent desired generation: %w", err)
	}
	return generation, nil
}

func (m *Master) Desired(ctx context.Context, identity authenticated) (Desired, error) {
	out := Desired{Inbounds: []DesiredInbound{}, Users: []DesiredUser{}}
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin desired snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := requireNodeProtocol(ctx, tx, identity); err != nil {
		return out, err
	}
	now := m.now().Unix()
	out.Generation, err = m.refreshDesired(ctx, tx, identity, now)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN restart_status='pending' AND monitoring IS NOT NULL
		THEN restart_id ELSE '' END FROM servers WHERE id=?`, identity.id).Scan(&out.RestartID); err != nil {
		return out, fmt.Errorf("read desired restart command: %w", err)
	}
	out.Inbounds, err = desiredInbounds(ctx, tx, identity.id)
	if err != nil {
		return out, err
	}
	out.Users, err = desiredUsers(ctx, tx, identity.id, now)
	if err != nil {
		return out, err
	}
	out.Route.BlockBittorrent, err = db.GetSetting(ctx, tx, db.BlockBittorrent)
	if err != nil {
		return out, err
	}
	var protocol int
	if err := tx.QueryRowContext(ctx, "SELECT protocol FROM servers WHERE id=?", identity.id).Scan(&protocol); err != nil {
		return out, fmt.Errorf("read desired exit capability: %w", err)
	}
	if protocol < ExternalProxyProtocol {
		var external bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM exit_links WHERE entry_id=? AND external_proxy IS NOT NULL)", identity.id).Scan(&external); err != nil {
			return out, fmt.Errorf("read proxy capability requirement: %w", err)
		}
		if external {
			return out, ErrAgentUpgradeRequired
		}
	}
	if protocol >= ExitProtocol {
		exits, err := db.ServerExitConfig(ctx, tx, identity.id)
		if err != nil {
			return out, err
		}
		if !exits.Empty() {
			out.Exit = &exits
		}
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit desired snapshot: %w", err)
	}
	return out, nil
}

func desiredInbounds(ctx context.Context, tx *sql.Tx, serverID int64) ([]DesiredInbound, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,agent_key_id,tag,protocol,port,enabled,remark,options
		FROM inbounds WHERE server_id=? ORDER BY id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("read desired inbounds: %w", err)
	}
	defer rows.Close()
	inbounds := []DesiredInbound{}
	for rows.Next() {
		var in DesiredInbound
		var raw string
		if err := rows.Scan(&in.ID, &in.KeyID, &in.Tag, &in.Protocol, &in.Port, &in.Enabled, &in.Remark, &raw); err != nil {
			return nil, fmt.Errorf("scan desired inbound: %w", err)
		}
		switch in.Protocol {
		case "vless_reality":
			var options confgen.RealityOptions
			if json.Unmarshal([]byte(raw), &options) != nil {
				return nil, fmt.Errorf("decode desired Reality options")
			}
			options.PrivateKey, options.PublicKey, options.ShortIDs = "", "", nil
			in.Options, err = json.Marshal(options)
		case "hysteria2":
			var options confgen.Hysteria2Options
			if json.Unmarshal([]byte(raw), &options) != nil {
				return nil, fmt.Errorf("decode desired Hysteria2 options")
			}
			in.Options, err = json.Marshal(options)
		default:
			return nil, fmt.Errorf("read desired inbound: unsupported protocol")
		}
		if err != nil {
			return nil, fmt.Errorf("encode desired inbound options: %w", err)
		}
		inbounds = append(inbounds, in)
	}
	return inbounds, rows.Err()
}

// desiredUsers lists the eligible users that may use serverID.
func desiredUsers(ctx context.Context, tx *sql.Tx, serverID, now int64) ([]DesiredUser, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,uuid,password,enabled,traffic_limit,traffic_up,traffic_down,expire_at
		FROM users WHERE `+db.UserServerAccess+` ORDER BY id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("read desired users: %w", err)
	}
	defer rows.Close()
	users := []DesiredUser{}
	for rows.Next() {
		var user confgen.User
		if err := rows.Scan(&user.ID, &user.Name, &user.UUID, &user.Password, &user.Enabled, &user.TrafficLimit, &user.TrafficUp, &user.TrafficDown, &user.ExpireAt); err != nil {
			return nil, fmt.Errorf("scan desired user: %w", err)
		}
		if confgen.UserEligible(user, now) {
			users = append(users, DesiredUser{user.ID, user.Name, user.UUID, user.Password})
		}
	}
	return users, rows.Err()
}

func (m *Master) PublishKeys(ctx context.Context, identity authenticated, keys InboundKeys) error {
	tx, err := m.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin inbound key publication: %w", err)
	}
	defer tx.Rollback()
	if err := requireNodeProtocol(ctx, tx, identity); err != nil {
		return err
	}
	var keyID, protocol, raw string
	err = tx.QueryRowContext(ctx, `SELECT i.agent_key_id,i.protocol,i.options FROM inbounds i
		WHERE i.id=? AND i.server_id=(SELECT id FROM servers WHERE `+agentIdentity+`)`,
		keys.InboundID, identity.id, identity.publicKey).Scan(&keyID, &protocol, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("read owned inbound for key publication: %w", err)
	}
	if keyID == "" || keys.KeyID != keyID {
		return ErrStaleKeys
	}
	options, err := publishedOptions(protocol, raw, keys, m.now())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbounds SET options=?,agent_keys_ready=1
		WHERE id=? AND server_id=? AND (options<>? OR agent_keys_ready=0)`,
		string(options), keys.InboundID, identity.id, string(options)); err != nil {
		return fmt.Errorf("store inbound public material: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit inbound public material: %w", err)
	}
	return nil
}

func publishedOptions(protocol, raw string, keys InboundKeys, now time.Time) ([]byte, error) {
	switch protocol {
	case "vless_reality":
		short, err := hex.DecodeString(keys.ShortID)
		if decodeBytes(keys.PublicKey, 32) == nil || err != nil || len(short) != 8 || keys.CertPEM != "" || keys.CertSHA256 != "" {
			return nil, ErrInvalidKeys
		}
		var options confgen.RealityOptions
		if json.Unmarshal([]byte(raw), &options) != nil {
			return nil, fmt.Errorf("decode stored Reality options")
		}
		options.PrivateKey, options.PublicKey, options.ShortIDs = "", keys.PublicKey, []string{hex.EncodeToString(short)}
		return json.Marshal(options)
	case "hysteria2":
		var options struct {
			confgen.Hysteria2Options
			Fingerprint string `json:"fingerprint"`
			Certificate string `json:"certificate"`
		}
		if json.Unmarshal([]byte(raw), &options) != nil {
			return nil, fmt.Errorf("decode stored Hysteria2 options")
		}
		block, rest := pem.Decode([]byte(keys.CertPEM))
		if keys.PublicKey != "" || keys.ShortID != "" || block == nil || block.Type != "CERTIFICATE" ||
			len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return nil, ErrInvalidKeys
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		hash := sha256.Sum256(block.Bytes)
		if err != nil || cert.VerifyHostname(options.SNI) != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) ||
			!bytes.Equal(cert.RawIssuer, cert.RawSubject) || cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil ||
			hex.EncodeToString(hash[:]) != keys.CertSHA256 {
			return nil, ErrInvalidKeys
		}
		options.Fingerprint, options.Certificate = keys.CertSHA256, string(pem.EncodeToMemory(block))
		return json.Marshal(options)
	default:
		return nil, ErrInvalidKeys
	}
}
