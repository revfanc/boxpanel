package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
)

var ErrInitialized = errors.New("panel already initialized; use info or reset-admin with the same data directory")

type InitOptions struct {
	Paths    Paths
	Port     int
	PublicIP string
	Dev      bool
	Probe    IPProbe
}

// Credentials are returned only to the CLI caller, never to logs or HTTP APIs.
type Credentials struct {
	Username string
	Password string
}

type InitResult struct {
	Config
	Credentials
}

func RandomPath(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate random path: %w", err)
		}
		result[i] = alphabet[n.Int64()]
	}
	return string(result), nil
}

func newCredentials() (Credentials, string, error) {
	suffix, err := RandomPath(8)
	if err != nil {
		return Credentials{}, "", err
	}
	password, err := auth.RandomToken(24)
	if err != nil {
		return Credentials{}, "", err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Credentials{}, "", err
	}
	return Credentials{Username: "admin-" + suffix, Password: password}, hash, nil
}

func Initialize(ctx context.Context, opts InitOptions) (InitResult, error) {
	var result InitResult
	if err := opts.Paths.Validate(); err != nil {
		return result, err
	}
	if opts.Port < 0 || opts.Port > 65535 {
		return result, fmt.Errorf("initialize panel: choose --port between 1 and 65535, or omit it for a random port")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Paths.Database), 0700); err != nil {
		return result, fmt.Errorf("create data directory (choose a writable --data-dir): %w", err)
	}
	file, err := os.OpenFile(opts.Paths.Database, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return result, fmt.Errorf("create panel database (choose a writable --db-path): %w", err)
	}
	if err := file.Close(); err != nil {
		return result, fmt.Errorf("close new database: %w", err)
	}
	if err := os.Chmod(opts.Paths.Database, 0600); err != nil {
		return result, fmt.Errorf("secure database permissions: %w", err)
	}
	conn, err := db.Open(ctx, opts.Paths.Database)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin initialization: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM admins) + (SELECT count(*) FROM settings)
		+ (SELECT count(*) FROM users) + (SELECT count(*) FROM inbounds) + (SELECT count(*) FROM sessions)`).Scan(&count); err != nil {
		return result, fmt.Errorf("check initialization state: %w", err)
	}
	if count != 0 {
		return result, ErrInitialized
	}
	// The transaction's IMMEDIATE lock prevents concurrent initializers from
	// overwriting credentials, settings or certificate files.
	ip, err := initializationIP(ctx, opts)
	if err != nil {
		return result, err
	}
	port := opts.Port
	if opts.Dev {
		port = 2053
	} else if port == 0 {
		port, err = randomPort()
		if err != nil {
			return result, err
		}
	}
	webPath, err := RandomPath(12)
	if err != nil {
		return result, err
	}
	subPath, err := RandomPath(12)
	if err != nil {
		return result, err
	}
	for subPath == webPath {
		subPath, err = RandomPath(12)
		if err != nil {
			return result, err
		}
	}
	mode := "self_signed"
	if opts.Dev {
		webPath, mode = "", "http"
	}
	credentials, hash, err := newCredentials()
	if err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO admins (username, password_hash, created_at) VALUES (?, ?, ?)", credentials.Username, hash, time.Now().Unix()); err != nil {
		return result, fmt.Errorf("create administrator: %w", err)
	}
	for key, value := range map[db.SettingKey[string]]string{
		db.WebPath: webPath, db.SubPath: subPath, db.PublicHost: ip.String(), db.PanelTLSMode: mode,
		db.SetupStep: "welcome", db.PanelName: "boxpanel", db.SingboxVersion: "",
	} {
		if err := db.SetSetting(ctx, tx, key, value); err != nil {
			return result, err
		}
	}
	if err := db.SetSetting(ctx, tx, db.WebPort, port); err != nil {
		return result, err
	}
	if err := db.SetSetting(ctx, tx, db.SetupCompleted, false); err != nil {
		return result, err
	}
	if err := db.SetSetting(ctx, tx, db.BlockBittorrent, true); err != nil {
		return result, err
	}
	rules := db.RulesSettings{Preset: "bypass_cn", Source: "metacubex", UpdateInterval: "1d",
		CustomDirect: []string{}, CustomProxy: []string{},
		DNS: db.DNSSettings{Remote: "https://1.1.1.1/dns-query", Local: "https://223.5.5.5/dns-query"}}
	if err := db.SetSetting(ctx, tx, db.Rules, rules); err != nil {
		return result, err
	}
	created := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, name := range created {
				_ = os.Remove(name)
			}
		}
	}()
	if !opts.Dev {
		certificate, key, err := certs.Panel(ip, time.Now())
		if err != nil {
			return result, err
		}
		for _, item := range []struct {
			path string
			data []byte
		}{{opts.Paths.KeyFile, key}, {opts.Paths.CertFile, certificate}} {
			if err := writeNewPrivateFile(item.path, item.data); err != nil {
				return result, err
			}
			created = append(created, item.path)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit initialization: %w", err)
	}
	committed = true
	return InitResult{Config: Config{WebPort: port, WebPath: webPath, PublicHost: ip.String(), TLSMode: mode}, Credentials: credentials}, nil
}

func initializationIP(ctx context.Context, opts InitOptions) (netip.Addr, error) {
	if opts.Dev {
		return netip.MustParseAddr("127.0.0.1"), nil
	}
	if opts.PublicIP != "" {
		ip, err := netip.ParseAddr(opts.PublicIP)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() && !ip.IsLoopback() {
			return netip.Addr{}, fmt.Errorf("initialize IP: provide an IPv4/IPv6 address via --public-ip; use 127.0.0.1 for local testing")
		}
		return ip.Unmap(), nil
	}
	probe := opts.Probe
	if probe == nil {
		probe = DefaultIPProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ip, err := probe(ctx)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("initialize public IP: %w", err)
	}
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return netip.Addr{}, fmt.Errorf("initialize public IP: probe returned an invalid address; supply --public-ip")
	}
	return ip.Unmap(), nil
}

func randomPort() (int, error) {
	for range 32 {
		n, err := rand.Int(rand.Reader, big.NewInt(50001))
		if err != nil {
			return 0, fmt.Errorf("choose random port: %w", err)
		}
		port := int(n.Int64()) + 10000
		listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
		if err == nil {
			_ = listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("choose random port: no available port found; specify --port with a free port")
}

func writeNewPrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create certificate directory (choose writable --cert-file/--key-file paths): %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create certificate file (preserve existing certificates and choose unused --cert-file/--key-file paths): %w", err)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write certificate file (check available space and retry): %w", err)
	}
	return nil
}

func ResetAdmin(ctx context.Context, conn *sql.DB) (Credentials, error) {
	credentials, hash, err := newCredentials()
	if err != nil {
		return Credentials{}, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("begin administrator reset: %w", err)
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM admins ORDER BY id LIMIT 1").Scan(&id); err != nil {
		return Credentials{}, fmt.Errorf("read administrator (run boxpanel init first): %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE admins SET username = ?, password_hash = ? WHERE id = ?", credentials.Username, hash, id); err != nil {
		return Credentials{}, fmt.Errorf("reset administrator credentials: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id = ?", id); err != nil {
		return Credentials{}, fmt.Errorf("revoke reset administrator sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Credentials{}, fmt.Errorf("commit administrator reset: %w", err)
	}
	return credentials, nil
}
