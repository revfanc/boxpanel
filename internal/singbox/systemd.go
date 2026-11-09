package singbox

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/host"
)

const PanelUnit = "boxpanel.service"
const SingboxUnit = "boxpanel-singbox.service"
const DefaultBinary = "/usr/local/share/boxpanel/sing-box"
const DefaultUnitDir = "/etc/systemd/system"

type ServiceState struct {
	Running bool
	Uptime  int64
}

// Controller only accepts the two managed unit names, never shell commands.
type Controller interface {
	Reload(context.Context) error
	Enable(context.Context, string) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Restart(context.Context, string) error
	State(context.Context, string) (ServiceState, error)
}

type SystemdOptions struct {
	OS     string
	Dev    bool
	Runner host.Runner
	Files  host.Files
}

type systemd struct {
	runner host.Runner
	files  host.Files
	mock   bool
}

func NewSystemd(opts SystemdOptions) Controller {
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Runner == nil {
		opts.Runner = host.Native{}
	}
	if opts.Files == nil {
		opts.Files = host.Native{}
	}
	return &systemd{runner: opts.Runner, files: opts.Files, mock: opts.Dev || opts.OS != "linux"}
}

func managedUnit(unit string) error {
	if unit != PanelUnit && unit != SingboxUnit {
		return fmt.Errorf("refusing unmanaged systemd unit")
	}
	return nil
}

func (s *systemd) Reload(ctx context.Context) error {
	if s.mock {
		return nil
	}
	if _, err := s.runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd units: %w", err)
	}
	return nil
}

func (s *systemd) command(ctx context.Context, command, unit string) error {
	if err := managedUnit(unit); err != nil {
		return err
	}
	if s.mock {
		return nil
	}
	if _, err := s.runner.Run(ctx, "systemctl", command, "--", unit); err != nil {
		return fmt.Errorf("systemctl %s %s: %w", command, unit, err)
	}
	return nil
}
func (s *systemd) Enable(ctx context.Context, unit string) error {
	return s.command(ctx, "enable", unit)
}
func (s *systemd) Start(ctx context.Context, unit string) error { return s.command(ctx, "start", unit) }
func (s *systemd) Stop(ctx context.Context, unit string) error  { return s.command(ctx, "stop", unit) }
func (s *systemd) Restart(ctx context.Context, unit string) error {
	return s.command(ctx, "restart", unit)
}

func (s *systemd) State(ctx context.Context, unit string) (ServiceState, error) {
	var state ServiceState
	if err := managedUnit(unit); err != nil {
		return state, err
	}
	if s.mock {
		return state, nil
	}
	output, err := s.runner.Run(ctx, "systemctl", "show", "--property=LoadState,ActiveState,ActiveEnterTimestampMonotonic", "--", unit)
	if err != nil {
		return state, fmt.Errorf("inspect systemd service: %w", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return state, fmt.Errorf("invalid systemctl show output")
		}
		if _, exists := values[key]; exists {
			return state, fmt.Errorf("duplicate systemctl property")
		}
		values[key] = value
	}
	if values["LoadState"] == "not-found" {
		return state, nil
	}
	if values["LoadState"] != "loaded" && values["LoadState"] != "masked" {
		return state, fmt.Errorf("systemd unit is not loaded; inspect the unit and run daemon-reload")
	}
	switch values["ActiveState"] {
	case "inactive", "failed", "activating", "deactivating":
		return state, nil
	case "active", "reloading":
		state.Running = true
	default:
		return state, fmt.Errorf("unknown systemd active state")
	}
	started, err := strconv.ParseUint(values["ActiveEnterTimestampMonotonic"], 10, 64)
	if err != nil || started == 0 {
		return ServiceState{}, fmt.Errorf("invalid systemd start timestamp")
	}
	data, err := s.files.ReadFile("/proc/uptime")
	if err != nil {
		return ServiceState{}, fmt.Errorf("read service uptime: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ServiceState{}, fmt.Errorf("missing system uptime")
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	elapsed := uptime - float64(started)/1e6
	if err != nil || math.IsNaN(elapsed) || math.IsInf(elapsed, 0) || elapsed < 0 || elapsed >= math.MaxInt64 {
		return ServiceState{}, fmt.Errorf("invalid service uptime")
	}
	state.Uptime = int64(elapsed)
	return state, nil
}

type UnitPaths struct {
	PanelBinary string
	Binary      string
	Config      string
	Database    string
	CertFile    string
	KeyFile     string
}

// GenerateUnits is pure. Installation/enabling belongs to its caller; neither
// template creates a configuration or starts a service without one.
func GenerateUnits(paths UnitPaths) (map[string][]byte, error) {
	quote := func(value string) (string, error) {
		if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n\t") {
			return "", fmt.Errorf("systemd paths must be absolute and contain no control characters")
		}
		value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%", "$", "$$").Replace(value)
		return "\"" + value + "\"", nil
	}
	values := make([]string, 6)
	for index, path := range []string{paths.PanelBinary, paths.Binary, paths.Config, paths.Database, paths.CertFile, paths.KeyFile} {
		value, err := quote(path)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	panel := fmt.Sprintf("%s serve --db-path %s --cert-file %s --key-file %s --singbox-path %s --singbox-config %s", values[0], values[3], values[4], values[5], values[1], values[2])
	core := fmt.Sprintf("%s run -c %s", values[1], values[2])
	template := "[Unit]\nDescription=%s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nExecStart=%s\nRestart=on-failure\nRestartSec=3\nLimitNOFILE=%d\n%s\n[Install]\nWantedBy=multi-user.target\n"
	return map[string][]byte{
		PanelUnit:   []byte(fmt.Sprintf(template, "boxpanel web panel", panel, 65535, "")),
		SingboxUnit: []byte(fmt.Sprintf(template, "sing-box managed by boxpanel", core, 1048576, "CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nAmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\n")),
	}, nil
}
