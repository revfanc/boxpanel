// Package app assembles persistent panel configuration and initialization.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/spa"
)

type Paths struct {
	DataDir  string
	Database string
	CertFile string
	KeyFile  string
}

func (p Paths) CertificateDir() string {
	if p.DataDir != "" {
		return filepath.Join(p.DataDir, "certs")
	}
	return filepath.Join(filepath.Dir(p.Database), "certs")
}

func (p Paths) Validate() error {
	seen := map[string]bool{}
	for _, name := range []string{p.Database, p.CertFile, p.KeyFile} {
		if name == "" {
			return fmt.Errorf("configure paths: paths must not be empty; use --data-dir or explicit file options")
		}
		abs, err := filepath.Abs(name)
		if err != nil {
			return fmt.Errorf("resolve configured path: %w", err)
		}
		if seen[abs] {
			return fmt.Errorf("configure paths: database, certificate and key must use different files")
		}
		certDir, err := filepath.Abs(p.CertificateDir())
		if err != nil {
			return fmt.Errorf("resolve certificate directory: %w", err)
		}
		base := filepath.Base(abs)
		if filepath.Dir(abs) == certDir && (strings.HasPrefix(base, "account-") || strings.HasPrefix(base, "panel-") && strings.HasSuffix(base, ".pem")) {
			return fmt.Errorf("configure paths: account-* and panel-*.pem in certs are reserved for managed certificates; choose another file")
		}
		seen[abs] = true
	}
	return nil
}

// OpenExisting never creates a database as a side effect of info/reset/serve.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open panel database (run boxpanel init with the same --data-dir/--db-path first): %w", err)
	}
	return db.Open(ctx, path)
}

type Config struct {
	WebPort    int
	WebPath    string
	PublicHost string
	TLSMode    string
}

func LoadConfig(ctx context.Context, conn *sql.DB) (Config, error) {
	var cfg Config
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return cfg, fmt.Errorf("read panel configuration: %w", err)
	}
	defer tx.Rollback()
	if cfg.WebPort, err = db.GetSetting(ctx, tx, db.WebPort); err != nil {
		return cfg, err
	}
	if cfg.WebPath, err = db.GetSetting(ctx, tx, db.WebPath); err != nil {
		return cfg, err
	}
	if cfg.PublicHost, err = db.GetSetting(ctx, tx, db.PublicHost); err != nil {
		return cfg, err
	}
	if cfg.TLSMode, err = db.GetSetting(ctx, tx, db.PanelTLSMode); err != nil {
		return cfg, err
	}
	if cfg.WebPort < 1 || cfg.WebPort > 65535 {
		return cfg, fmt.Errorf("read web_port: restore a port between 1 and 65535")
	}
	if err := spa.ValidateWebPath(cfg.WebPath); err != nil {
		return cfg, err
	}
	if cfg.TLSMode != "self_signed" && cfg.TLSMode != "acme" && cfg.TLSMode != "http" {
		return cfg, fmt.Errorf("read panel_tls_mode: restore self_signed/acme, or use a separate dev database")
	}
	if cfg.PublicHost == "" || strings.ContainsAny(cfg.PublicHost, "/?#@\\ \t\r\n") {
		return cfg, fmt.Errorf("read public_host: restore a hostname or IP without a URL scheme or path")
	}
	return cfg, nil
}

func (cfg Config) URL() string {
	scheme := "https"
	if cfg.TLSMode == "http" {
		scheme = "http"
	}
	path := "/"
	if cfg.WebPath != "" {
		path += cfg.WebPath + "/"
	}
	return scheme + "://" + net.JoinHostPort(cfg.PublicHost, strconv.Itoa(cfg.WebPort)) + path
}

func AdminUsername(ctx context.Context, conn *sql.DB) (string, error) {
	var username string
	if err := conn.QueryRowContext(ctx, "SELECT username FROM admins ORDER BY id LIMIT 1").Scan(&username); err != nil {
		return "", fmt.Errorf("read administrator (run boxpanel init on an empty database first): %w", err)
	}
	return username, nil
}
