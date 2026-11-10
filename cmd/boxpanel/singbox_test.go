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

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type cliInstaller struct {
	calls int
	err   error
}

func (i *cliInstaller) Install(_ context.Context, dest, version string) error {
	i.calls++
	if i.err != nil {
		return i.err
	}
	return os.WriteFile(dest, []byte(version), 0755)
}

type cliRunner struct{ t *testing.T }

func (r cliRunner) LookPath(string) (string, error) { return "", os.ErrNotExist }
func (r cliRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if !reflect.DeepEqual(args, []string{"version"}) {
		r.t.Fatalf("unexpected command: %s %v", name, args)
	}
	data, err := os.ReadFile(name)
	return append([]byte("sing-box version "), data...), err
}

type cliController struct {
	singbox.Controller
	reloads int
	err     error
	running bool
}

func (c *cliController) Reload(context.Context) error { c.reloads++; return c.err }
func (c *cliController) State(context.Context, string) (singbox.ServiceState, error) {
	return singbox.ServiceState{Running: c.running}, nil
}

func cliFixture(t *testing.T) (singboxOptions, singboxDependencies, *cliInstaller, *cliController) {
	t.Helper()
	clearEnvironment(t)
	dir := t.TempDir()
	opts, err := parseSingboxOptions([]string{"install", "--data-dir", dir, "--singbox-path", filepath.Join(dir, "bin", "sing-box"), "--unit-dir", filepath.Join(dir, "units"), "--panel-binary", filepath.Join(dir, "boxpanel")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Initialize(context.Background(), app.InitOptions{Paths: opts.paths, Dev: true}); err != nil {
		t.Fatal(err)
	}
	installer := &cliInstaller{}
	controller := &cliController{Controller: singbox.NewSystemd(singbox.SystemdOptions{Dev: true})}
	deps := singboxDependencies{os: "linux", installer: installer, controller: controller, runner: cliRunner{t}, files: host.Native{}}
	return opts, deps, installer, controller
}

func TestSingboxInstallCLIAndIdempotency(t *testing.T) {
	opts, deps, installer, controller := cliFixture(t)
	opts.acmeDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	var out bytes.Buffer
	for range 2 {
		if err := singboxCommand(context.Background(), opts, deps, &out); err != nil {
			t.Fatal(err)
		}
	}
	if installer.calls != 1 || controller.reloads != 2 {
		t.Fatalf("download=%d reload=%d", installer.calls, controller.reloads)
	}
	for _, unit := range []string{singbox.PanelUnit, singbox.SingboxUnit} {
		info, err := os.Stat(filepath.Join(opts.unitDir, unit))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("unit=%v %v", info, err)
		}
	}
	unit, err := os.ReadFile(filepath.Join(opts.unitDir, singbox.PanelUnit))
	if err != nil || !strings.Contains(string(unit), `--acme-directory "`+opts.acmeDirectory+`"`) || !strings.Contains(string(unit), `--data-dir "`+opts.paths.DataDir+`"`) {
		t.Fatalf("panel unit lost certificate configuration: %s, %v", unit, err)
	}
	if _, err := os.Stat(opts.core.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created sing-box config")
	}
	conn, err := app.OpenExisting(context.Background(), opts.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	version, err := db.GetSetting(context.Background(), conn, db.SingboxVersion)
	if err != nil || version != singbox.PinnedVersion {
		t.Fatalf("recorded version=%s %v", version, err)
	}
	opts.command = "status"
	if err := singboxCommand(context.Background(), opts, deps, &out); err != nil || !strings.Contains(out.String(), "running=false") {
		t.Fatal(out.String(), err)
	}
}

func TestSingboxInstallFailuresAndRecovery(t *testing.T) {
	for _, scenario := range []string{"download", "reload", "configured", "running", "collision", "uninitialized"} {
		t.Run(scenario, func(t *testing.T) {
			opts, deps, installer, controller := cliFixture(t)
			switch scenario {
			case "download":
				installer.err = errors.New("offline")
			case "reload":
				controller.err = errors.New("systemd unavailable")
			case "configured":
				if err := os.WriteFile(opts.core.config, []byte("private configuration"), 0600); err != nil {
					t.Fatal(err)
				}
			case "running":
				controller.running = true
			case "collision":
				opts.core.binary = opts.paths.Database
			case "uninitialized":
				opts.paths.Database = filepath.Join(t.TempDir(), "missing.db")
			}
			if err := singboxCommand(context.Background(), opts, deps, io.Discard); err == nil {
				t.Fatal("expected failure")
			}
			if scenario == "reload" {
				controller.err = nil
				if err := singboxCommand(context.Background(), opts, deps, io.Discard); err != nil {
					t.Fatal(err)
				}
				if installer.calls != 1 {
					t.Fatal("recovery redownloaded verified binary")
				}
			} else if scenario != "download" && installer.calls != 0 {
				t.Fatal("download ran before validation")
			}
			if scenario == "configured" {
				data, _ := os.ReadFile(opts.core.config)
				if string(data) != "private configuration" {
					t.Fatal("configuration changed")
				}
			}
		})
	}
}

func TestRestoreMissingRecordedCoreWithRetainedConfig(t *testing.T) {
	opts, deps, installer, _ := cliFixture(t)
	ctx := context.Background()
	if err := singboxCommand(ctx, opts, deps, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.core.config, []byte("retained config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(opts.core.binary); err != nil {
		t.Fatal(err)
	}
	other := opts
	other.version = "1.0.0"
	if singboxCommand(ctx, other, deps, io.Discard) == nil {
		t.Fatal("restored different version with existing config")
	}
	other.preserveVersion = true
	if err := singboxCommand(ctx, other, deps, io.Discard); err != nil {
		t.Fatal(err)
	}
	if installer.calls != 2 {
		t.Fatal(installer.calls)
	}
	data, err := os.ReadFile(opts.core.config)
	if err != nil || string(data) != "retained config" {
		t.Fatal("changed retained config", err)
	}
}

func TestSingboxCLIArgumentsAndMock(t *testing.T) {
	clearEnvironment(t)
	for _, args := range [][]string{{"singbox", "--help"}, {"singbox", "install", "--help"}, {"singbox", "status", "--help"}, {"singbox", "install", "--dev"}, {"singbox", "status", "--dev"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatal(args, code, stderr.String())
		}
	}
	for _, args := range [][]string{{"singbox", "restart"}, {"singbox", "install", "--preserve-version", "--version", singbox.PinnedVersion}, {"singbox", "install", "--version", "1.15.0-rc.1"}, {"singbox", "install", "--version", "../1.2.3"}, {"singbox", "status", "extra"}, {"singbox", "status", "--version", "1.2.3"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "--help") {
			t.Fatal(args, code, stderr.String())
		}
	}
	t.Setenv("BOXPANEL_SINGBOX_PATH", "/tmp/from-env")
	t.Setenv("BOXPANEL_SINGBOX_CONFIG", "/tmp/config-env")
	t.Setenv("BOXPANEL_UNIT_DIR", "/tmp/units-env")
	opts, err := parseSingboxOptions([]string{"install", "--singbox-path", "/tmp/from-flag", "--data-dir", "/tmp/data"}, io.Discard)
	if err != nil || opts.core.binary != "/tmp/from-flag" || opts.core.config != "/tmp/config-env" || opts.unitDir != "/tmp/units-env" {
		t.Fatal(opts, err)
	}
	var out bytes.Buffer
	if err := singboxCommand(context.Background(), opts, singboxDependencies{os: "darwin"}, &out); err == nil {
		t.Fatal("installed service on macOS")
	}
	opts.command = "status"
	if err := singboxCommand(context.Background(), opts, singboxDependencies{os: "darwin"}, &out); err != nil || !strings.Contains(out.String(), "模拟") {
		t.Fatal(out.String(), err)
	}
}
