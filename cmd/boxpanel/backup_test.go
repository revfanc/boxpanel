package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type restoreControl struct {
	active map[string]bool
	events []string
}

func (c *restoreControl) Reload(context.Context) error { return nil }
func (c *restoreControl) Enable(_ context.Context, u string) error {
	c.events = append(c.events, "enable:"+u)
	return nil
}
func (c *restoreControl) Start(_ context.Context, u string) error {
	c.active[u] = true
	c.events = append(c.events, "start:"+u)
	return nil
}
func (c *restoreControl) Stop(_ context.Context, u string) error {
	c.active[u] = false
	c.events = append(c.events, "stop:"+u)
	return nil
}
func (c *restoreControl) Restart(_ context.Context, u string) error {
	c.active[u] = true
	c.events = append(c.events, "restart:"+u)
	return nil
}
func (c *restoreControl) State(_ context.Context, u string) (singbox.ServiceState, error) {
	return singbox.ServiceState{Running: c.active[u]}, nil
}

type restoreTestFirewall struct{ plans []firewall.Plan }

func (f *restoreTestFirewall) Sync(_ context.Context, _, next firewall.Plan) error {
	f.plans = append(f.plans, next)
	return nil
}
func (f *restoreTestFirewall) Open(context.Context, int, string) error { return nil }

type restoreRunner struct {
	checks    int
	failCheck bool
	command   string
	args      []string
	err       error
}

func (r *restoreRunner) LookPath(s string) (string, error) { return s, nil }
func (r *restoreRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.command = name
	r.args = append([]string{}, args...)
	if len(args) > 0 && args[0] == "check" {
		r.checks++
		if r.failCheck {
			r.failCheck = false
			return nil, errors.New("test-check-failed")
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return []byte("active"), nil
}

func backupPaths(t *testing.T) (app.Paths, singboxPaths) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	p := app.Paths{DataDir: dir, Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "certs/panel.crt"), KeyFile: filepath.Join(dir, "certs/panel.key")}
	if _, err := app.Initialize(context.Background(), app.InitOptions{Paths: p, PublicIP: "127.0.0.1", Port: 2053}); err != nil {
		t.Fatal(err)
	}
	return p, singboxPaths{config: filepath.Join(dir, "sing-box.json"), binary: filepath.Join(filepath.Dir(dir), "sing-box")}
}

func TestBackupCLIEncryptedOutputAndNoOverwrite(t *testing.T) {
	clearEnvironment(t)
	p, _ := backupPaths(t)
	passfile := filepath.Join(t.TempDir(), "pass")
	pass := "cli-test-passphrase"
	if err := os.WriteFile(passfile, []byte(pass+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "backup.tar.gz.enc")
	args := []string{"backup", "--data-dir", p.DataDir, "--output", output, "--passphrase-file", passfile}
	var out, stderr bytes.Buffer
	if exit := run(args, &out, &stderr); exit != 0 {
		t.Fatal(exit, stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Decode(data, pass, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+stderr.String(), pass) {
		t.Fatal("passphrase exposed")
	}
	before := bytes.Clone(data)
	if run(args, io.Discard, io.Discard) == 0 {
		t.Fatal("overwrote existing backup")
	}
	data, _ = os.ReadFile(output)
	if !bytes.Equal(data, before) {
		t.Fatal("output changed")
	}
	if info, _ := os.Stat(output); info.Mode().Perm() != 0600 {
		t.Fatal("unsafe permissions")
	}
	for _, cmd := range []string{"backup", "restore"} {
		if run([]string{cmd, "--help"}, io.Discard, io.Discard) != 0 {
			t.Fatal("help")
		}
	}
	for _, p := range []string{"short", strings.Repeat("x", 1025)} {
		os.WriteFile(passfile, []byte(p), 0600)
		if _, err := readPassphrase(passfile, io.Discard); err == nil {
			t.Fatal("invalid passphrase accepted")
		}
	}
}

func TestRestoreUsesFullApplyAndRollsBack(t *testing.T) {
	for _, failure := range []string{"none", "check", "ready"} {
		t.Run(failure, func(t *testing.T) {
			p, c := backupPaths(t)
			ctx := context.Background()
			conn, err := app.OpenExisting(ctx, p.Database)
			if err != nil {
				t.Fatal(err)
			}
			// Archive the source identity, then change the live target metadata.
			if err = db.SetSetting(ctx, conn, db.PanelName, "source"); err != nil {
				t.Fatal(err)
			}
			encrypted, err := backup.Create(ctx, conn, p.CertificateDir(), "test-version", "cli-test-passphrase", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err = db.SetSetting(ctx, conn, db.PanelName, "target"); err != nil {
				t.Fatal(err)
			}
			controller := &restoreControl{active: map[string]bool{singbox.PanelUnit: true, singbox.SingboxUnit: true}}
			runner := &restoreRunner{}
			fw := &restoreTestFirewall{}
			opts := singbox.ApplyOptions{OS: "linux", Runner: runner, Controller: controller, Firewall: fw}
			manager, err := newApplyManager(conn, p, c, opts)
			if err != nil {
				t.Fatal(err)
			}
			if err = manager.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			conn.Close()
			originalConfig, err := os.ReadFile(c.config)
			if err != nil {
				t.Fatal(err)
			}
			a, err := backup.Decode(encrypted, "cli-test-passphrase", true)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := backup.Stage(ctx, a, filepath.Dir(p.DataDir))
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(stage)
			runner.checks = 0
			runner.failCheck = failure == "check"
			controller.events = nil
			readyCalls := 0
			deps := restoreDependencies{controller: controller, fw: fw, apply: opts, ready: func(context.Context, app.Paths) error {
				readyCalls++
				if failure == "ready" && readyCalls == 1 {
					return errors.New("ready failed")
				}
				return nil
			}}
			err = restoreWith(ctx, p, c, stage, false, "", io.Discard, deps)
			if (err != nil) != (failure != "none") {
				t.Fatal(err)
			}
			restored, e := app.OpenExisting(ctx, p.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer restored.Close()
			name, e := db.GetSetting(ctx, restored, db.PanelName)
			if e != nil {
				t.Fatal(e)
			}
			want := "source"
			if failure != "none" {
				want = "target"
			}
			if name != want {
				t.Fatalf("identity=%s", name)
			}
			if runner.checks < 1 {
				t.Fatal("bypassed check")
			}
			if !reflect.DeepEqual(controller.events[:2], []string{"stop:" + singbox.PanelUnit, "stop:" + singbox.SingboxUnit}) {
				t.Fatal("replaced data before stopping services")
			}
			if !controller.active[singbox.PanelUnit] || !controller.active[singbox.SingboxUnit] {
				t.Fatal("services not restored")
			}
			if failure != "none" {
				got, e := os.ReadFile(c.config)
				if e != nil || !bytes.Equal(got, originalConfig) {
					t.Fatal("original config not restored")
				}
			}
			if failure == "none" {
				names, _ := filepath.Glob(p.DataDir + ".pre-restore-*")
				if len(names) != 1 {
					t.Fatal("no complete pre-restore directory")
				}
			}
		})
	}
}

func TestPanelRestoreHandoffHasNoPassphrase(t *testing.T) {
	p, c := backupPaths(t)
	ctx := context.Background()
	conn, err := app.OpenExisting(ctx, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pass := "handoff-test-passphrase"
	data, err := backup.Create(ctx, conn, p.CertificateDir(), "test-version", pass, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := &restoreRunner{}
	panel := &panelBackups{service: &backup.Service{DB: conn, DataDir: p.DataDir, Version: "test"}, opts: serveOptions{paths: p, core: c}, runner: r}
	if err = panel.Restore(ctx, data, "wrong-passphrase-123"); !errors.Is(err, backup.ErrInvalid) {
		t.Fatal(err)
	}
	r.err = errors.New("queue failed")
	if err = panel.Restore(ctx, data, pass); err == nil {
		t.Fatal("queue failure ignored")
	}
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(p.DataDir), ".boxpanel-restore-*"))
	if len(names) != 0 {
		t.Fatal("failed handoff leaked staging")
	}
	r.err = nil
	if err = panel.Restore(ctx, data, pass); err != nil {
		t.Fatal(err)
	}
	if r.command != "systemd-run" || strings.Contains(strings.Join(r.args, " "), pass) || strings.Contains(strings.Join(r.args, " "), "passphrase-file") {
		t.Fatal("unsafe handoff")
	}
	stage := r.args[len(r.args)-1]
	defer os.RemoveAll(stage)
	if _, err := os.Stat(filepath.Join(stage, "boxpanel.db")); err != nil {
		t.Fatal("no staged data")
	}
	if err = panel.Restore(ctx, data, pass); err == nil {
		t.Fatal("duplicate accepted")
	}
	current, err := app.LoadConfig(ctx, conn)
	if err != nil || current.PublicHost != "127.0.0.1" {
		t.Fatal("live DB replaced during API request")
	}
}

func TestFreshInstallRestoreSkipsInitializationAndRollsBackFailure(t *testing.T) {
	source, _ := backupPaths(t)
	ctx := context.Background()
	conn, err := app.OpenExisting(ctx, source.Database)
	if err != nil {
		t.Fatal(err)
	}
	username, err := app.AdminUsername(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	data, err := backup.Create(ctx, conn, source.CertificateDir(), "test", "", time.Now())
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	a, err := backup.Decode(data, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "install failure"}[fail], func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "data")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".installing"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			p := app.Paths{DataDir: dir, Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "certs/panel.crt"), KeyFile: filepath.Join(dir, "certs/panel.key")}
			c := singboxPaths{binary: filepath.Join(parent, "sing-box"), config: filepath.Join(dir, "sing-box.json")}
			stage, err := backup.Stage(ctx, a, parent)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(stage)
			controller := &restoreControl{active: map[string]bool{}}
			runner := &restoreRunner{}
			fw := &restoreTestFirewall{}
			installed := false
			deps := restoreDependencies{controller: controller, fw: fw, apply: singbox.ApplyOptions{OS: "linux", Runner: runner}, ready: func(context.Context, app.Paths) error { return nil }, install: func(ctx context.Context) error {
				installed = true
				restored, e := app.OpenExisting(ctx, p.Database)
				if e != nil {
					return e
				}
				defer restored.Close()
				name, e := app.AdminUsername(ctx, restored)
				if e != nil {
					return e
				}
				if name != username {
					t.Fatal("new administrator generated")
				}
				if fail {
					return errors.New("install failed")
				}
				return nil
			}}
			err = restoreWith(ctx, p, c, stage, true, "", io.Discard, deps)
			if !installed || (err != nil) != fail {
				t.Fatal("install lifecycle", err)
			}
			if fail {
				if _, err := os.Stat(p.Database); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed fresh install retained restored DB")
				}
				if _, err := os.Stat(filepath.Join(dir, ".installing")); err != nil {
					t.Fatal("lost original directory")
				}
			}
		})
	}
}
