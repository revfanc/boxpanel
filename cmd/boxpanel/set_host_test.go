package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

func hostFixture(t *testing.T) (app.Paths, *sql.DB) {
	t.Helper()
	clearEnvironment(t)
	dir := t.TempDir()
	paths := app.Paths{DataDir: dir, Database: filepath.Join(dir, "custom.db"), CertFile: filepath.Join(dir, "panel.crt"), KeyFile: filepath.Join(dir, "panel.key")}
	if _, err := app.Initialize(context.Background(), app.InitOptions{Paths: paths, Port: 24567, PublicIP: "2001:db8::1"}); err != nil {
		t.Fatal(err)
	}
	conn, err := app.OpenExisting(context.Background(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return paths, conn
}

type hostRunner struct {
	t       *testing.T
	lookErr error
	run     func([]string) error
	calls   int
}

func (r *hostRunner) LookPath(name string) (string, error) {
	if name != "systemctl" {
		r.t.Errorf("unexpected lookup %q", name)
	}
	return name, r.lookErr
}
func (r *hostRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	if name != "systemctl" || !reflect.DeepEqual(args, []string{"restart", "--", "boxpanel.service"}) {
		r.t.Errorf("unexpected command %q %v", name, args)
	}
	return nil, r.run(args)
}

func TestSetHostPersistsBeforeRestartAndPrintsURL(t *testing.T) {
	paths, conn := hostFixture(t)
	ctx := context.Background()
	before, err := app.LoadConfig(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	// A host-only recovery must not overwrite other settings or trigger Apply.
	if _, err := conn.Exec(`CREATE TRIGGER protect_settings BEFORE UPDATE ON settings WHEN NEW.key!='public_host' BEGIN SELECT RAISE(ABORT,'unrelated settings changed'); END`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"203.0.113.8", "2001:db8::8", "Node.Example.COM"} {
		t.Run(name, func(t *testing.T) {
			runner := &hostRunner{t: t, run: func([]string) error {
				// Read through another connection during the restart, proving the
				// write committed and its transaction was released first.
				got, err := db.GetSetting(ctx, conn, db.PublicHost)
				if err != nil || got != name {
					t.Fatalf("restart before commit: %q %v", got, err)
				}
				return nil
			}}
			var out, stderr bytes.Buffer
			args := []string{name, "--data-dir", paths.DataDir, "--db-path", paths.Database}
			if code := setHostCommand(ctx, args, setHostDependencies{os: "linux", runner: runner}, &out, &stderr); code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d err=%s", code, &stderr)
			}
			want := before
			want.PublicHost = name
			got, err := app.LoadConfig(ctx, conn)
			if err != nil || got != want || runner.calls != 1 || !strings.Contains(out.String(), want.URL()) || !strings.Contains(out.String(), "已重启 boxpanel") {
				t.Fatalf("cfg=%+v err=%v calls=%d output=%s", got, err, runner.calls, &out)
			}
			if strings.Contains(out.String(), "密码:") || strings.Contains(out.String(), "私钥") {
				t.Fatal("credentials in set-host output")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "sing-box.json")); !os.IsNotExist(err) {
		t.Fatal("set-host wrote a core configuration")
	}
}

func TestSetHostRestartFailureIsNonfatal(t *testing.T) {
	paths, conn := hostFixture(t)
	for _, missing := range []bool{false, true} {
		runner := &hostRunner{t: t, run: func([]string) error { return errors.New("sensitive-command-error") }}
		if missing {
			runner.lookErr = errors.New("missing systemctl")
		}
		var out, stderr bytes.Buffer
		code := setHostCommand(context.Background(), []string{"--db-path", paths.Database, "203.0.113.8", "--data-dir", paths.DataDir}, setHostDependencies{os: "linux", runner: runner}, &out, &stderr)
		got, err := db.GetSetting(context.Background(), conn, db.PublicHost)
		if code != 0 || err != nil || got != "203.0.113.8" || !strings.Contains(stderr.String(), "地址已保存") || !strings.Contains(stderr.String(), "systemctl restart boxpanel") || strings.Contains(stderr.String(), "sensitive-command-error") {
			t.Fatalf("code=%d host=%q err=%v output=%s %s", code, got, err, &out, &stderr)
		}
	}
}

func TestSetHostPathOverridesDispatchAndNoSystemEffects(t *testing.T) {
	paths, conn := hostFixture(t)
	t.Setenv("BOXPANEL_DATA_DIR", filepath.Join(t.TempDir(), "wrong"))
	t.Setenv("BOXPANEL_DB_PATH", filepath.Join(t.TempDir(), "wrong.db"))
	var out, stderr bytes.Buffer
	// Explicit flags override environment; --dev exercises real dispatch without
	// any possibility of touching systemd, even when these tests run on Linux.
	if code := run([]string{"set-host", "203.0.113.9", "--dev", "--data-dir", paths.DataDir, "--db-path", paths.Database}, &out, &stderr); code != 0 {
		t.Fatalf("dispatch failed: %d %s", code, &stderr)
	}
	t.Setenv("BOXPANEL_DATA_DIR", paths.DataDir)
	t.Setenv("BOXPANEL_DB_PATH", paths.Database)
	runner := &hostRunner{t: t, run: func([]string) error { t.Error("unexpected restart"); return nil }}
	for _, mode := range []string{"self_signed", "http"} {
		if err := db.SetSetting(context.Background(), conn, db.PanelTLSMode, mode); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if code := setHostCommand(context.Background(), []string{"example.com"}, setHostDependencies{os: "darwin", runner: runner}, &out, &stderr); code != 0 {
			t.Fatalf("environment paths failed: %d %s", code, &stderr)
		}
		if runner.calls != 0 || strings.Contains(out.String(), "已重启") || strings.Contains(out.String(), "重新申请证书") {
			t.Fatal("non-Linux/non-ACME output claims system effects")
		}
	}
}

func TestSetHostRejectsInvalidInputsAndStorageFailures(t *testing.T) {
	paths, conn := hostFixture(t)
	t.Setenv("BOXPANEL_DATA_DIR", paths.DataDir)
	t.Setenv("BOXPANEL_DB_PATH", paths.Database)
	for _, args := range [][]string{nil, {""}, {"a", "b"}, {"https://example.com"}, {"example.com:443"}, {"[2001:db8::1]"}, {"fe80::1%lo0"}, {"中文.example"}, {"a\nb"}, {"example.com/path"}, {"--unknown"}, {"--data-dir"}} {
		if code := run(append([]string{"set-host"}, args...), io.Discard, io.Discard); code != 2 {
			t.Errorf("args %q: exit %d", args, code)
		}
	}
	if got, _ := db.GetSetting(context.Background(), conn, db.PublicHost); got != "2001:db8::1" {
		t.Fatal("invalid input modified host")
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"set-host", "--help"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "-db-path") || !strings.Contains(out.String(), "<IP|domain>") {
		t.Fatal("missing CLI help")
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if code := run([]string{"set-host", "example.com", "--db-path", missing}, io.Discard, io.Discard); code != 1 {
		t.Fatal("missing DB accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("recovery created a new DB")
	}
	if _, err := conn.Exec(`CREATE TRIGGER reject_host BEFORE UPDATE ON settings WHEN NEW.key='public_host' BEGIN SELECT RAISE(ABORT,'secret-db-error'); END`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	runner := &hostRunner{t: t, run: func([]string) error { t.Error("restart after failed save"); return nil }}
	code := setHostCommand(context.Background(), []string{"example.com"}, setHostDependencies{os: "linux", runner: runner}, &out, &stderr)
	got, _ := db.GetSetting(context.Background(), conn, db.PublicHost)
	if code != 1 || got != "2001:db8::1" || runner.calls != 0 || out.Len() != 0 || strings.Contains(stderr.String(), "secret-db-error") {
		t.Fatalf("failed write: code=%d host=%q output=%s %s", code, got, &out, &stderr)
	}
}
