package singbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/firewall"
)

func snapshot(ctx context.Context, tx *sql.Tx) (confgen.Snapshot, []string, error) {
	var s confgen.Snapshot
	var secrets []string
	var err error
	s.ServerID, err = db.LocalServerID(ctx, tx)
	if err != nil {
		return s, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,tag,protocol,port,enabled,options FROM inbounds WHERE server_id=? ORDER BY id", s.ServerID)
	if err != nil {
		return s, nil, fmt.Errorf("read inbounds: %w", err)
	}
	for rows.Next() {
		in := confgen.Inbound{ServerID: s.ServerID}
		var options string
		if err := rows.Scan(&in.ID, &in.Tag, &in.Protocol, &in.Port, &in.Enabled, &options); err != nil {
			rows.Close()
			return s, nil, fmt.Errorf("scan inbound: %w", err)
		}
		in.Options = json.RawMessage(options)
		s.Inbounds = append(s.Inbounds, in)
		secrets = append(secrets, jsonStrings(in.Options)...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, nil, fmt.Errorf("read inbounds: %w", err)
	}
	// Users without access to this server stay out of its config; their
	// credentials are still redacted from diagnostics.
	rows, err = tx.QueryContext(ctx, "SELECT id,name,uuid,password,enabled,traffic_limit,traffic_up,traffic_down,expire_at,sub_token,"+
		db.UserServerAccess+" FROM users ORDER BY id", s.ServerID)
	if err != nil {
		return s, nil, fmt.Errorf("read users: %w", err)
	}
	for rows.Next() {
		var u confgen.User
		var token string
		var allowed bool
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.Password, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &token, &allowed); err != nil {
			rows.Close()
			return s, nil, fmt.Errorf("scan user: %w", err)
		}
		if allowed {
			s.Users = append(s.Users, u)
		}
		secrets = append(secrets, u.UUID, u.Password, token)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, nil, fmt.Errorf("read users: %w", err)
	}
	s.BlockBittorrent, err = db.GetSetting(ctx, tx, db.BlockBittorrent)
	if err != nil {
		return s, secrets, err
	}
	s.Exit, err = db.LocalExitConfig(ctx, tx, s.ServerID)
	if err != nil {
		return s, secrets, err
	}
	for _, preset := range db.ExitPresets {
		if !s.Exit.Outgoing.UsesPreset(preset.Name) {
			continue
		}
		rules, err := db.GetSetting(ctx, tx, preset.Rules)
		if err == nil {
			if s.PresetRules == nil {
				s.PresetRules = make(map[string]exitlink.DestinationRules)
			}
			s.PresetRules[preset.Name] = rules
		} else if !errors.Is(err, sql.ErrNoRows) {
			return s, secrets, err
		}
	}
	for _, in := range s.Exit.Incoming {
		secrets = append(secrets, in.Password)
	}
	if s.Exit.Outgoing != nil {
		secrets = append(secrets, s.Exit.Outgoing.Password)
		if proxy := s.Exit.Outgoing.External; proxy != nil {
			secrets = append(secrets, proxy.Password)
		}
		s.ExitProbe, err = db.GetSetting(ctx, tx, db.ExitProbe)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	return s, secrets, err
}

func planFor(s confgen.Snapshot) (firewall.Plan, error) {
	p := firewall.Plan{}
	for _, in := range s.Inbounds {
		if !in.Enabled || in.ServerID != s.ServerID {
			continue
		}
		proto := "tcp"
		if in.Protocol == "hysteria2" {
			proto = "udp"
			var opts confgen.Hysteria2Options
			if json.Unmarshal(in.Options, &opts) != nil {
				return p, fmt.Errorf("decode Hysteria2 options; repair the inbound")
			}
			if h := opts.PortHopping; h.Enabled {
				p.Hops = append(p.Hops, firewall.Hop{Start: h.Start, End: h.End, Port: in.Port})
			}
		}
		p.Ports = append(p.Ports, firewall.Port{Port: in.Port, Proto: proto})
	}
	for _, in := range s.Exit.Incoming {
		p.ExitPorts = append(p.ExitPorts, firewall.ExitPort{Port: in.Port, Source: in.Source})
	}
	return p, p.Validate()
}

func storedPlan(ctx context.Context, tx *sql.Tx) (firewall.Plan, error) {
	raw, err := db.GetSetting(ctx, tx, db.AppliedFirewall)
	if errors.Is(err, sql.ErrNoRows) {
		return firewall.Plan{}, nil
	}
	if err != nil {
		return firewall.Plan{}, err
	}
	var p firewall.Plan
	if json.Unmarshal(raw, &p) != nil {
		return p, fmt.Errorf("invalid stored firewall plan; restore the database")
	}
	return p, p.Validate()
}

func (m *Manager) runtimeOptions(ctx context.Context, tx *sql.Tx, s confgen.Snapshot) (confgen.Options, error) {
	opts := confgen.Options{UserRoutingMarks: !m.unsupported, Now: m.now(), Hysteria2TLS: map[int64]confgen.CertificateFiles{}}
	port, err := db.GetSetting(ctx, tx, db.ClashAPIPort)
	if errors.Is(err, sql.ErrNoRows) {
		// Select with crypto/rand, probe the loopback bind, then persist it.
		// A later port race is detected by restart/liveness and rolls back apply.
		port = 0
		for attempt := 0; attempt < 64; attempt++ {
			choice, e := rand.Int(rand.Reader, big.NewInt(50001))
			if e != nil {
				return opts, fmt.Errorf("select control port: %w", e)
			}
			candidate := 10000 + int(choice.Int64())
			conflict := false
			for _, in := range s.Inbounds {
				if in.Enabled && in.Protocol == "vless_reality" && in.Port == candidate {
					conflict = true
				}
			}
			for _, in := range s.Exit.Incoming {
				if in.Port == candidate {
					conflict = true
				}
			}
			panelPort, e := db.GetSetting(ctx, tx, db.WebPort)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return opts, e
			}
			if conflict || candidate == panelPort {
				continue
			}
			listener, e := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate)))
			if e != nil {
				continue
			}
			listener.Close()
			port = candidate
			break
		}
		if port == 0 {
			return opts, fmt.Errorf("select control port: free a loopback TCP port in 10000–60000 and retry")
		}

		err = db.SetSetting(ctx, tx, db.ClashAPIPort, port)
	}
	if err != nil {
		return opts, err
	}
	secret, err := db.GetSetting(ctx, tx, db.ClashAPISecret)
	if errors.Is(err, sql.ErrNoRows) {
		bytes := make([]byte, 32)
		if _, err = rand.Read(bytes); err != nil {
			return opts, fmt.Errorf("generate control secret: %w", err)
		}
		secret = base64.RawURLEncoding.EncodeToString(bytes)
		err = db.SetSetting(ctx, tx, db.ClashAPISecret, secret)
	}
	if err != nil {
		return opts, err
	}
	opts.ClashAPIPort, opts.ClashAPISecret = port, secret
	// Validate all input before creating any certificates.
	for _, in := range s.Inbounds {
		if in.Enabled && in.Protocol == "hysteria2" {
			opts.Hysteria2TLS[in.ID] = confgen.CertificateFiles{CertificatePath: "/validation/cert", KeyPath: "/validation/key"}
		}
	}
	if _, err := confgen.Build(s, opts); err != nil {
		return opts, err
	}
	fingerprints := map[string]string{}
	for _, in := range s.Inbounds {
		if in.Protocol != "hysteria2" {
			continue
		}
		var agentKeyID, certificateID string
		if err := tx.QueryRowContext(ctx, "SELECT agent_key_id,agent_cert_id FROM inbounds WHERE id=?", in.ID).Scan(&agentKeyID, &certificateID); err != nil {
			return opts, fmt.Errorf("read certificate identity: %w", err)
		}
		if !in.Enabled && agentKeyID == "" {
			continue
		}
		var hy confgen.Hysteria2Options
		if json.Unmarshal(in.Options, &hy) != nil {
			return opts, fmt.Errorf("decode Hysteria2 certificate options")
		}
		files, fingerprint, err := m.prepareHysteria2(ctx, tx, in.ID, hy.SNI, agentKeyID, certificateID, opts.Now)
		if err != nil {
			return opts, err
		}
		if in.Enabled {
			fingerprints[strconv.FormatInt(in.ID, 10)] = fingerprint
			opts.Hysteria2TLS[in.ID] = files
		}
	}
	if err := db.SetSetting(ctx, tx, db.Hysteria2Fingerprints, fingerprints); err != nil {
		return opts, err
	}
	return opts, nil
}

func (m *Manager) hysteria2Files(id int64, sni, revision string) confgen.CertificateFiles {
	hash := sha256.Sum256([]byte(sni))
	stem := filepath.Join(filepath.Dir(m.config), "certs", fmt.Sprintf("hy2-%d-%x", id, hash[:8]))
	if revision != "" {
		stem += "-" + revision
	}
	return confgen.CertificateFiles{CertificatePath: stem + ".crt", KeyPath: stem + ".key"}
}

func (m *Manager) prepareHysteria2(ctx context.Context, tx *sql.Tx, id int64, sni, agentKeyID, revision string, now time.Time) (confgen.CertificateFiles, string, error) {
	agent := agentKeyID != ""
	files := m.hysteria2Files(id, sni, revision)
	if !agent || validCertificateRevision(revision) {
		_, fingerprint, err := m.readHysteria2(files, sni, now)
		if err == nil {
			return files, fingerprint, nil
		}
		if !agent && !errors.Is(err, fs.ErrNotExist) {
			return files, "", err
		}
	}
	if agent {
		// Use a new immutable path to keep rollback references valid. A
		// retry derives the same candidate and reuses a pair staged earlier.
		digest := sha256.Sum256([]byte(agentKeyID + "\n" + revision))
		revision = base64.RawURLEncoding.EncodeToString(digest[:])
		files = m.hysteria2Files(id, sni, revision)
		if _, fingerprint, err := m.readHysteria2(files, sni, now); err == nil {
			return files, fingerprint, recordAgentCertificate(ctx, tx, id, revision)
		}
	}
	pair, err := certs.Hysteria2(sni, now)
	if err != nil {
		return files, "", fmt.Errorf("prepare Hysteria2 certificate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(files.KeyPath), 0700); err != nil {
		return files, "", fmt.Errorf("create certificate directory: %w", err)
	}
	if err := m.files.WriteFile(files.KeyPath, pair.PrivateKey, 0600); err != nil {
		return files, "", fmt.Errorf("save Hysteria2 key: %w", err)
	}
	if err := m.files.WriteFile(files.CertificatePath, pair.Certificate, 0600); err != nil {
		_ = m.files.Remove(files.KeyPath)
		return files, "", fmt.Errorf("save Hysteria2 certificate: %w", err)
	}
	if agent {
		return files, pair.Fingerprint, recordAgentCertificate(ctx, tx, id, revision)
	}
	return files, pair.Fingerprint, nil
}

func recordAgentCertificate(ctx context.Context, tx *sql.Tx, id int64, revision string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE inbounds SET agent_cert_id=? WHERE id=?", revision, id); err != nil {
		return fmt.Errorf("record node certificate identity: %w", err)
	}
	return nil
}

func (m *Manager) readHysteria2(files confgen.CertificateFiles, sni string, now time.Time) ([]byte, string, error) {
	certificate, ce := m.files.ReadFile(files.CertificatePath)
	key, ke := m.files.ReadFile(files.KeyPath)
	if errors.Is(ce, fs.ErrNotExist) && errors.Is(ke, fs.ErrNotExist) {
		return nil, "", fs.ErrNotExist
	}
	if ce != nil || ke != nil {
		return nil, "", fmt.Errorf("read Hysteria2 certificate pair; restore both certificate and key files")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return nil, "", fmt.Errorf("invalid Hysteria2 certificate pair; restore certificate and key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.VerifyHostname(sni) != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, "", fmt.Errorf("Hysteria2 certificate is invalid or expired; renew the certificate pair")
	}
	fingerprint := sha256.Sum256(pair.Certificate[0])
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), hex.EncodeToString(fingerprint[:]), nil
}

func validCertificateRevision(revision string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(revision)
	return err == nil && len(decoded) == 32
}

// AgentCertificate returns only the canonical public leaf and its fingerprint,
// after checking that the node still has the corresponding private key.
func (m *Manager) AgentCertificate(id int64, sni, revision string) ([]byte, string, error) {
	if !validCertificateRevision(revision) {
		return nil, "", fmt.Errorf("missing node certificate revision")
	}
	return m.readHysteria2(m.hysteria2Files(id, sni, revision), sni, m.now())
}
