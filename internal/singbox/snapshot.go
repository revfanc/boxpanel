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
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
)

func snapshot(ctx context.Context, tx *sql.Tx) (confgen.Snapshot, []string, error) {
	var s confgen.Snapshot
	var secrets []string
	rows, err := tx.QueryContext(ctx, "SELECT id,tag,protocol,port,enabled,options FROM inbounds ORDER BY id")
	if err != nil {
		return s, nil, fmt.Errorf("read inbounds: %w", err)
	}
	for rows.Next() {
		var in confgen.Inbound
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
	rows, err = tx.QueryContext(ctx, "SELECT id,name,uuid,password,enabled,traffic_limit,traffic_up,traffic_down,expire_at,sub_token FROM users ORDER BY id")
	if err != nil {
		return s, nil, fmt.Errorf("read users: %w", err)
	}
	for rows.Next() {
		var u confgen.User
		var token string
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.Password, &u.Enabled, &u.TrafficLimit, &u.TrafficUp, &u.TrafficDown, &u.ExpireAt, &token); err != nil {
			rows.Close()
			return s, nil, fmt.Errorf("scan user: %w", err)
		}
		s.Users = append(s.Users, u)
		secrets = append(secrets, u.UUID, u.Password, token)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, nil, fmt.Errorf("read users: %w", err)
	}
	s.BlockBittorrent, err = db.GetSetting(ctx, tx, db.BlockBittorrent)
	return s, secrets, err
}

func planFor(s confgen.Snapshot) (firewall.Plan, error) {
	p := firewall.Plan{}
	for _, in := range s.Inbounds {
		if !in.Enabled {
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
	opts := confgen.Options{Now: m.now(), Hysteria2TLS: map[int64]confgen.CertificateFiles{}}
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
		if !in.Enabled || in.Protocol != "hysteria2" {
			continue
		}
		var hy confgen.Hysteria2Options
		_ = json.Unmarshal(in.Options, &hy) // validated by Build above
		hash := sha256.Sum256([]byte(hy.SNI))
		// SNI-specific immutable paths preserve the old certificate on rollback.
		stem := filepath.Join(filepath.Dir(m.config), "certs", fmt.Sprintf("hy2-%d-%x", in.ID, hash[:8]))
		files := confgen.CertificateFiles{CertificatePath: stem + ".crt", KeyPath: stem + ".key"}
		certificate, ce := m.files.ReadFile(files.CertificatePath)
		key, ke := m.files.ReadFile(files.KeyPath)
		if errors.Is(ce, fs.ErrNotExist) && errors.Is(ke, fs.ErrNotExist) {
			pair, e := certs.Hysteria2(hy.SNI, opts.Now)
			if e != nil {
				return opts, fmt.Errorf("prepare Hysteria2 certificate: %w", e)
			}
			if e = os.MkdirAll(filepath.Dir(stem), 0700); e != nil {
				return opts, fmt.Errorf("create certificate directory: %w", e)
			}
			certificate, key = pair.Certificate, pair.PrivateKey
			if e = m.files.WriteFile(files.KeyPath, key, 0600); e != nil {
				return opts, fmt.Errorf("save Hysteria2 key: %w", e)
			}
			if e = m.files.WriteFile(files.CertificatePath, certificate, 0600); e != nil {
				_ = m.files.Remove(files.KeyPath)
				return opts, fmt.Errorf("save Hysteria2 certificate: %w", e)
			}
		} else if ce != nil || ke != nil {
			return opts, fmt.Errorf("read Hysteria2 certificate pair; restore both certificate and key files")
		}
		pair, e := tls.X509KeyPair(certificate, key)
		if e != nil {
			return opts, fmt.Errorf("invalid Hysteria2 certificate pair; restore certificate and key")
		}
		leaf, e := x509.ParseCertificate(pair.Certificate[0])
		if e != nil || leaf.VerifyHostname(hy.SNI) != nil || opts.Now.Before(leaf.NotBefore) || !opts.Now.Before(leaf.NotAfter) {
			return opts, fmt.Errorf("Hysteria2 certificate is invalid or expired; renew the certificate pair")
		}
		fingerprint := sha256.Sum256(pair.Certificate[0])
		fingerprints[strconv.FormatInt(in.ID, 10)] = hex.EncodeToString(fingerprint[:])
		opts.Hysteria2TLS[in.ID] = files
	}
	if err := db.SetSetting(ctx, tx, db.Hysteria2Fingerprints, fingerprints); err != nil {
		return opts, err
	}
	return opts, nil
}
