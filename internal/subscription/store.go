package subscription

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/share"
	"github.com/revfanc/boxpanel/web"
)

var ErrUnavailable = errors.New("subscription certificate unavailable; apply the inbound first")

type Store struct {
	DB          *sql.DB
	Certificate func(id int64, sni string) ([]byte, error)
	Now         func() time.Time
}
type User struct {
	confgen.User
	Token string
}
type Data struct {
	Snapshot
	User                                             User
	PublicHost, PanelName, SubPath, WebPath, TLSMode string
	WebPort                                          int
	MigrationURL                                     string
	ReceivedMigration                                bool
	nodeGroups                                       []nodeGroup
}

type nodeGroup struct {
	name  string
	first int // index of the first node in this server's group
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// EqualToken hashes both strings to a fixed size before the constant-time
// comparison. Callers scan every stored token, with no token-index lookup or
// early return that would replace this check with a variable-time SQL compare.
func EqualToken(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func Active(u confgen.User, now time.Time) bool {
	return u.Enabled && u.TrafficLimit >= 0 && u.TrafficUp >= 0 && u.TrafficDown >= 0 && u.ExpireAt >= 0 &&
		(u.ExpireAt == 0 || u.ExpireAt > now.Unix()) && (u.TrafficLimit == 0 || u.TrafficUp < u.TrafficLimit && u.TrafficDown < u.TrafficLimit-u.TrafficUp)
}

// Load obtains user, rules, settings and inbounds from one transaction. id=0
// selects the first user for previews; token != nil authenticates a public link.
func (s *Store) Load(ctx context.Context, id int64, token *string) (Data, error) {
	var d Data
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, fmt.Errorf("read subscription snapshot: %w", err)
	}
	defer tx.Rollback()
	if token != nil {
		out, err := db.LoadMasterMigration(ctx, tx, db.MigrationOutgoing)
		if err != nil {
			return d, err
		}
		if out.Phase == db.MigrationRetired {
			d.MigrationURL = out.TargetURL
		} else {
			in, err := db.LoadMasterMigration(ctx, tx, db.MigrationReceived)
			if err != nil {
				return d, err
			}
			d.ReceivedMigration = in.Phase == db.MigrationActive
		}
		rows, err := tx.QueryContext(ctx, "SELECT id,sub_token FROM users ORDER BY id")
		if err != nil {
			return d, fmt.Errorf("read subscription tokens: %w", err)
		}
		id = 0
		for rows.Next() {
			var candidate int64
			var stored string
			if err := rows.Scan(&candidate, &stored); err != nil {
				rows.Close()
				return d, fmt.Errorf("read subscription token: %w", err)
			}
			if EqualToken(*token, stored) {
				id = candidate
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return d, fmt.Errorf("read subscription tokens: %w", err)
		}
		if id == 0 {
			return d, sql.ErrNoRows
		}
	}
	query := "SELECT id,name,uuid,password,enabled,traffic_limit,traffic_up,traffic_down,expire_at,sub_token FROM users WHERE id=?"
	args := []any{id}
	if id == 0 {
		query = "SELECT id,name,uuid,password,enabled,traffic_limit,traffic_up,traffic_down,expire_at,sub_token FROM users ORDER BY id LIMIT 1"
		args = nil
	}
	u := &d.User
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&u.ID, &u.Name, &u.UUID, &u.Password, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &u.Token); err != nil {
		return d, fmt.Errorf("read subscription user: %w", err)
	}
	if token != nil && !Active(u.User, s.now()) {
		return d, sql.ErrNoRows
	}
	for _, field := range []struct {
		key db.SettingKey[string]
		dst *string
	}{{db.PublicHost, &d.PublicHost}, {db.PanelName, &d.PanelName}, {db.SubPath, &d.SubPath}, {db.WebPath, &d.WebPath}, {db.PanelTLSMode, &d.TLSMode}} {
		v, e := db.GetSetting(ctx, tx, field.key)
		if e != nil {
			return d, e
		}
		*field.dst = v
	}
	if !rules.ValidHost(d.PublicHost) {
		return d, fmt.Errorf("read public_host: repair the hostname or IP in settings")
	}
	if d.WebPort, err = db.GetSetting(ctx, tx, db.WebPort); err != nil {
		return d, err
	}
	if d.Rules, err = db.GetSetting(ctx, tx, db.Rules); err != nil {
		return d, err
	}
	if d.AutoGroup, err = db.ReadSubscriptionAutoGroup(ctx, tx); err != nil {
		return d, err
	}
	if err = rules.Validate(d.Rules); err != nil {
		return d, fmt.Errorf("read subscription rules: %w", err)
	}
	fingerprints, err := db.GetSetting(ctx, tx, db.Hysteria2Fingerprints)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	var serverCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM servers WHERE status<>'removed'").Scan(&serverCount); err != nil {
		return d, fmt.Errorf("count subscription servers: %w", err)
	}
	// Only servers the user may use; see db.UserServerAccess.
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.remark,i.protocol,i.port,i.options,s.public_host,s.name,s.is_local,s.id
		FROM inbounds i JOIN servers s ON s.id=i.server_id JOIN users u ON u.id=?
		WHERE i.enabled=1 AND s.status<>'removed' AND (s.is_local=1 OR i.agent_keys_ready=1)
		AND (u.all_servers=1 OR EXISTS (SELECT 1 FROM user_servers us WHERE us.user_id=u.id AND us.server_id=s.id))
		ORDER BY s.is_local DESC,s.id,i.id`, u.ID)
	if err != nil {
		return d, fmt.Errorf("read subscription inbounds: %w", err)
	}
	d.Nodes = []share.Node{}
	localCertificates := map[int64]bool{}
	var previousServer int64
	for rows.Next() {
		var n share.Node
		var protocol, raw, serverName string
		var local bool
		var serverID int64
		if err := rows.Scan(&n.ID, &n.Name, &protocol, &n.Port, &raw, &n.Host, &serverName, &local, &serverID); err != nil {
			rows.Close()
			return d, fmt.Errorf("read subscription inbound: %w", err)
		}
		if !rules.ValidHost(n.Host) {
			rows.Close()
			return d, fmt.Errorf("read subscription inbound %d: repair its server hostname or IP", n.ID)
		}
		n.UUID = u.UUID
		n.Password = u.Password
		switch protocol {
		case "vless_reality":
			var v confgen.RealityOptions
			if json.Unmarshal([]byte(raw), &v) != nil || len(v.ShortIDs) == 0 {
				rows.Close()
				return d, fmt.Errorf("read Reality options: repair the inbound")
			}
			n.Reality = &share.Reality{ServerName: v.ServerName, PublicKey: v.PublicKey, ShortID: v.ShortIDs[0], Fingerprint: v.Fingerprint}
		case "hysteria2":
			var h struct {
				confgen.Hysteria2Options
				Fingerprint string `json:"fingerprint"`
				Certificate string `json:"certificate"`
			}
			if json.Unmarshal([]byte(raw), &h) != nil {
				rows.Close()
				return d, fmt.Errorf("read Hysteria2 options: repair the inbound")
			}
			n.Hysteria2 = &h.Hysteria2Options
			if local {
				n.Fingerprint = fingerprints[fmt.Sprint(n.ID)]
				localCertificates[n.ID] = true
			} else {
				n.Fingerprint, n.Certificate = h.Fingerprint, h.Certificate
			}
		default:
			rows.Close()
			return d, fmt.Errorf("read subscription inbound: unsupported protocol")
		}
		if serverCount > 1 && (len(d.Nodes) == 0 || previousServer != serverID) {
			d.nodeGroups = append(d.nodeGroups, nodeGroup{name: serverName, first: len(d.Nodes)})
		}
		previousServer = serverID
		d.Nodes = append(d.Nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return d, fmt.Errorf("read subscription inbounds: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return d, fmt.Errorf("finish subscription snapshot: %w", err)
	}
	// Read local public PEM after releasing the snapshot connection.
	// Remote public PEM was loaded with its options in the snapshot.
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Hysteria2 == nil {
			continue
		}
		raw := []byte(n.Certificate)
		if localCertificates[n.ID] {
			if s.Certificate == nil {
				return d, ErrUnavailable
			}
			raw, err = s.Certificate(n.ID, n.Hysteria2.SNI)
			if err != nil {
				return d, ErrUnavailable
			}
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return d, ErrUnavailable
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil || leaf.VerifyHostname(n.Hysteria2.SNI) != nil || s.now().Before(leaf.NotBefore) || !s.now().Before(leaf.NotAfter) {
			return d, ErrUnavailable
		}
		hash := sha256.Sum256(block.Bytes)
		if hex.EncodeToString(hash[:]) != n.Fingerprint {
			return d, ErrUnavailable
		}
		n.Certificate = string(pem.EncodeToMemory(block))
	}
	d.Nodes = uniqueNames(d.Nodes, d.nodeGroups)
	return d, nil
}

// Resolve collisions in display order and keep names safe in rule strings.
// All subscription formats and node links share these display names.
func uniqueNames(nodes []share.Node, groups []nodeGroup) []share.Node {
	result := append([]share.Node{}, nodes...)
	text := web.SubscriptionText()
	used := map[string]bool{"proxy": true, "auto": true, "manual": true, "direct": true, "DIRECT": true, "REJECT": true,
		text["select"]: true, text["autoSelect"]: true, text["migrationNode"]: true}
	var serverName string
	for i := range result {
		if len(groups) > 0 && groups[0].first == i {
			serverName, groups = groups[0].name, groups[1:]
		}
		n := &result[i]
		n.Name = normalizeName(n.Name)
		if n.Name == "" {
			n.Name = defaultName(*n, text)
			if serverName != "" {
				n.Name = normalizeName(serverName + " · " + n.Name)
			}
		}
		base := n.Name
		for suffix := 2; used[n.Name]; suffix++ {
			n.Name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		used[n.Name] = true
	}
	return result
}

func normalizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		if r == ',' {
			return '，'
		}
		return r
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

func defaultName(n share.Node, text map[string]string) string {
	switch {
	case n.Reality != nil:
		return fmt.Sprintf(text["realityName"], n.Port)
	case n.Hysteria2 != nil:
		return fmt.Sprintf(text["hysteria2Name"], n.Port)
	default:
		return fmt.Sprintf("node-%d", n.ID)
	}
}
