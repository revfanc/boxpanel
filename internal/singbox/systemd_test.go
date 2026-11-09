package singbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/host"
)

type testFiles struct {
	host.Native
	data string
	err  error
}

func (f testFiles) ReadFile(name string) ([]byte, error) {
	if name != "/proc/uptime" {
		return nil, errors.New("unexpected path")
	}
	return []byte(f.data), f.err
}

func TestSystemdCommandsAndMock(t *testing.T) {
	for _, opts := range []SystemdOptions{{OS: "linux"}, {OS: "linux", Dev: true}, {OS: "darwin"}} {
		var commands [][]string
		opts.Runner = testRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, append([]string{name}, args...))
			return []byte("LoadState=not-found\nActiveState=inactive\nActiveEnterTimestampMonotonic=0\n"), nil
		})
		s := NewSystemd(opts)
		ctx := context.Background()
		for _, err := range []error{s.Reload(ctx), s.Enable(ctx, PanelUnit), s.Start(ctx, SingboxUnit), s.Stop(ctx, SingboxUnit), s.Restart(ctx, SingboxUnit)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.State(ctx, SingboxUnit); err != nil {
			t.Fatal(err)
		}
		if err := s.Restart(ctx, "sing-box.service"); err == nil {
			t.Fatal("accepted unmanaged unit")
		}
		if _, err := s.State(ctx, "--all"); err == nil {
			t.Fatal("accepted unit injection")
		}
		if opts.Dev || opts.OS != "linux" {
			if len(commands) != 0 {
				t.Fatal("mock executed commands")
			}
			continue
		}
		want := [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "--", PanelUnit}, {"systemctl", "start", "--", SingboxUnit}, {"systemctl", "stop", "--", SingboxUnit}, {"systemctl", "restart", "--", SingboxUnit}, {"systemctl", "show", "--property=LoadState,ActiveState,ActiveEnterTimestampMonotonic", "--", SingboxUnit}}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("commands=%v", commands)
		}
	}
}

func TestSystemdStateAndErrors(t *testing.T) {
	for _, tt := range []struct {
		load, active, start, uptime string
		running                     bool
		seconds                     int64
		bad                         bool
	}{
		{"loaded", "active", "40000000", "100.5 30", true, 60, false},
		{"loaded", "reloading", "40000000", "100.5 30", true, 60, false},
		{"loaded", "inactive", "0", "", false, 0, false},
		{"loaded", "failed", "0", "", false, 0, false},
		{"not-found", "inactive", "0", "", false, 0, false},
		{"masked", "inactive", "0", "", false, 0, false},
		{"error", "active", "1", "100", false, 0, true},
		{"loaded", "unknown", "0", "", false, 0, true},
		{"loaded", "active", "0", "100", false, 0, true},
		{"loaded", "active", "invalid", "100", false, 0, true},
		{"loaded", "active", "1000000000", "100", false, 0, true},
		{"loaded", "active", "1", "NaN", false, 0, true},
	} {
		s := NewSystemd(SystemdOptions{OS: "linux", Files: testFiles{data: tt.uptime}, Runner: testRunner(func(context.Context, string, ...string) ([]byte, error) {
			return []byte("LoadState=" + tt.load + "\nActiveState=" + tt.active + "\nActiveEnterTimestampMonotonic=" + tt.start + "\n"), nil
		})})
		got, err := s.State(context.Background(), SingboxUnit)
		if (err != nil) != tt.bad || (!tt.bad && got != (ServiceState{tt.running, tt.seconds})) {
			t.Errorf("state=%+v error=%v for %+v", got, err, tt)
		}
	}
	want := errors.New("permission denied")
	s := NewSystemd(SystemdOptions{OS: "linux", Runner: testRunner(func(context.Context, string, ...string) ([]byte, error) { return []byte("secret output"), want })})
	if _, err := s.State(context.Background(), SingboxUnit); !errors.Is(err, want) || strings.Contains(err.Error(), "secret output") {
		t.Fatal(err)
	}
	if err := s.Reload(context.Background()); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := s.Restart(context.Background(), SingboxUnit); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestGenerateUnitsPathsAndCapabilities(t *testing.T) {
	paths := UnitPaths{PanelBinary: "/usr/local/bin/boxpanel", Binary: DefaultBinary, Config: "/etc/boxpanel/sing-box.json", Database: "/etc/boxpanel/boxpanel.db", CertFile: "/etc/boxpanel/certs/panel.crt", KeyFile: "/etc/boxpanel/certs/panel.key"}
	units, err := GenerateUnits(paths)
	if err != nil || len(units) != 2 {
		t.Fatal(err)
	}
	for _, unit := range []string{PanelUnit, SingboxUnit} {
		body := string(units[unit])
		for _, part := range []string{"After=network-online.target", "Wants=network-online.target", "Restart=on-failure", "RestartSec=3", "WantedBy=multi-user.target"} {
			if !strings.Contains(body, part) {
				t.Errorf("%s missing %s", unit, part)
			}
		}
	}
	core := string(units[SingboxUnit])
	if !strings.Contains(core, `ExecStart="/usr/local/share/boxpanel/sing-box" run -c "/etc/boxpanel/sing-box.json"`) || !strings.Contains(core, "LimitNOFILE=1048576") || !strings.Contains(core, "AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW") {
		t.Fatal(core)
	}
	paths.Database = `/tmp/a b/%n/$DB/"q".db`
	units, err = GenerateUnits(paths)
	if err != nil || !strings.Contains(string(units[PanelUnit]), `--db-path "/tmp/a b/%%n/$$DB/\"q\".db"`) {
		t.Fatalf("quoting: %s, %v", units[PanelUnit], err)
	}
	for _, value := range []string{"relative", "/tmp/a\nExecStart=evil", "/tmp/a\x00b"} {
		paths.Binary = value
		if _, err := GenerateUnits(paths); err == nil {
			t.Fatalf("accepted path %q", value)
		}
	}
}

func TestInspectorTruthfulState(t *testing.T) {
	ctx := context.Background()
	binary := filepath.Join(t.TempDir(), "sing-box")
	i := Inspector{OS: "linux", Binary: binary, Runner: testRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("sing-box version " + PinnedVersion), nil
	}), Controller: NewSystemd(SystemdOptions{Dev: true})}
	if got, err := i.Status(ctx); err != nil || got != (Status{}) {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := i.Status(ctx); err != nil || !got.Installed || got.Version != PinnedVersion || got.Running {
		t.Fatal(got, err)
	}
	i.Stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	if _, err := i.Status(ctx); !errors.Is(err, fs.ErrPermission) {
		t.Fatal(err)
	}
	i.Stat = func(string) (fs.FileInfo, error) { t.Fatal("unexpected filesystem access in mock"); return nil, nil }
	i.Dev = true
	if got, err := i.Status(ctx); err != nil || got != (Status{}) {
		t.Fatal(got, err)
	}
	i.Dev, i.OS = false, "darwin"
	if got, err := i.Status(ctx); err != nil || got != (Status{}) {
		t.Fatal(got, err)
	}
}
