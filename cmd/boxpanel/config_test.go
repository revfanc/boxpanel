package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"BOXPANEL_DATA_DIR", "BOXPANEL_DB_PATH", "BOXPANEL_CERT_FILE", "BOXPANEL_KEY_FILE", "BOXPANEL_PUBLIC_IP", "BOXPANEL_LISTEN_HOST", "BOXPANEL_SINGBOX_PATH", "BOXPANEL_SINGBOX_CONFIG", "BOXPANEL_UNIT_DIR", "BOXPANEL_BINARY"} {
		t.Setenv(key, "")
	}
}

func TestConfigCommandsAndOverrides(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	t.Setenv("BOXPANEL_DATA_DIR", dir)
	t.Setenv("BOXPANEL_PUBLIC_IP", "127.0.0.1")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--port", "25432"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://127.0.0.1:25432/") || !strings.Contains(stdout.String(), "密码:") {
		t.Fatal("missing initial information")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatal("duplicate init did not refuse")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"info"}, &stdout, &stderr); code != 0 || strings.Contains(stdout.String(), "密码") || strings.Contains(stdout.String(), "argon2") {
		t.Fatalf("info leaked password or failed: %s %s", stdout.String(), stderr.String())
	}
	opts, err := parseServeOptions(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := app.OpenExisting(context.Background(), opts.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cfg, err := resolveServeConfig(context.Background(), conn, opts)
	if err != nil || cfg.WebPort != 25432 || len(cfg.WebPath) != 12 || cfg.TLSMode != "self_signed" {
		t.Fatalf("serve did not read DB: %+v %v", cfg, err)
	}
	opts, err = parseServeOptions([]string{"--port", "25433", "--web-path", "override", "--listen-host", "127.0.0.1"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	override, err := resolveServeConfig(context.Background(), conn, opts)
	if err != nil || override.WebPort != 25433 || override.WebPath != "override" {
		t.Fatal("CLI overrides ignored")
	}
	persisted, err := app.LoadConfig(context.Background(), conn)
	if err != nil || persisted != cfg {
		t.Fatal("serve overrides modified DB")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"reset-admin"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "密码:") {
		t.Fatalf("reset failed: %s", stderr.String())
	}
	t.Setenv("BOXPANEL_DB_PATH", filepath.Join(dir, "env.db"))
	t.Setenv("BOXPANEL_CERT_FILE", filepath.Join(dir, "env.crt"))
	t.Setenv("BOXPANEL_KEY_FILE", filepath.Join(dir, "env.key"))
	opts, err = parseServeOptions([]string{"--db-path", filepath.Join(dir, "flag.db"), "--cert-file", filepath.Join(dir, "flag.crt"), "--key-file", filepath.Join(dir, "flag.key")}, io.Discard)
	if err != nil || opts.paths.Database != filepath.Join(dir, "flag.db") || opts.paths.CertFile != filepath.Join(dir, "flag.crt") || opts.paths.KeyFile != filepath.Join(dir, "flag.key") {
		t.Fatal("flags did not override environment")
	}
	opts, err = parseServeOptions(nil, io.Discard)
	if err != nil || opts.paths.Database != filepath.Join(dir, "env.db") || opts.paths.CertFile != filepath.Join(dir, "env.crt") || opts.paths.KeyFile != filepath.Join(dir, "env.key") {
		t.Fatal("environment paths ignored")
	}
}

func TestDevConfigAndCLIValidation(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	opts, err := parseServeOptions([]string{"--dev", "--data-dir", dir}, io.Discard)
	if err != nil || opts.paths.Database != filepath.Join(dir, "boxpanel.db") {
		t.Fatal("explicit dev data directory ignored")
	}
	result, err := app.Initialize(context.Background(), app.InitOptions{Paths: opts.paths, Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := app.OpenExisting(context.Background(), opts.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := resolveServeConfig(context.Background(), conn, serveOptions{}); err == nil {
		t.Fatal("production accepted HTTP DB")
	}
	if err := db.SetSetting(context.Background(), conn, db.WebPort, 24567); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveServeConfig(context.Background(), conn, opts)
	if err != nil || cfg != result.Config {
		t.Fatal("dev settings not fixed")
	}
	for _, command := range []string{"init", "info", "reset-admin"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command, "--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "-data-dir") {
			t.Fatal("missing command help")
		}
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command, "extra"}, &stdout, &stderr); code != 2 {
			t.Fatal("extra arguments accepted")
		}
	}
	for _, args := range [][]string{{"init", "--port", "0"}, {"init", "--port", "65536"}, {"init", "--port", "-1"}, {"init", "--unknown"}} {
		if code := run(args, io.Discard, io.Discard); code != 2 {
			t.Fatal("invalid init arguments accepted")
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, command := range []string{"info", "reset-admin", "serve"} {
		if code := run([]string{command, "--data-dir", missing}, io.Discard, io.Discard); code != 1 {
			t.Fatal("uninitialized database accepted")
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("non-init commands created data directory")
	}
}
