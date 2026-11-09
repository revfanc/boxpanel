package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type singboxPaths struct{ binary, config string }

func addSingboxPaths(flags *flag.FlagSet) *singboxPaths {
	p := new(singboxPaths)
	flags.StringVar(&p.binary, "singbox-path", os.Getenv("BOXPANEL_SINGBOX_PATH"), "managed binary (BOXPANEL_SINGBOX_PATH; default /usr/local/share/boxpanel/sing-box)")
	flags.StringVar(&p.config, "singbox-config", os.Getenv("BOXPANEL_SINGBOX_CONFIG"), "core config (BOXPANEL_SINGBOX_CONFIG; default DATA_DIR/sing-box.json)")
	return p
}

func (p singboxPaths) resolve(paths *pathFlags, dev bool) singboxPaths {
	if p.binary == "" {
		p.binary = singbox.DefaultBinary
	}
	if p.config == "" {
		dir := paths.dataDir
		if dir == "" {
			dir = "/etc/boxpanel"
			if dev {
				dir = "."
			}
		}
		p.config = filepath.Join(dir, "sing-box.json")
	}
	return p
}

type singboxOptions struct {
	command, version, unitDir, panelBinary string
	dev                                    bool
	paths                                  app.Paths
	core                                   singboxPaths
}

func parseSingboxOptions(args []string, output io.Writer) (singboxOptions, error) {
	var opts singboxOptions
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(output, "Usage: boxpanel singbox install|status [options]\nUse 'boxpanel singbox install --help' for paths and version options.")
		return opts, flag.ErrHelp
	}
	opts.command = args[0]
	if opts.command != "install" && opts.command != "status" {
		return opts, fmt.Errorf("unknown singbox command")
	}
	flags := flag.NewFlagSet("singbox "+opts.command, flag.ContinueOnError)
	flags.SetOutput(output)
	paths := addPathFlags(flags)
	core := addSingboxPaths(flags)
	flags.BoolVar(&opts.dev, "dev", false, "mock sing-box management without downloads, file writes or systemctl")
	if opts.command == "install" {
		flags.StringVar(&opts.version, "version", singbox.PinnedVersion, "stable version published more than seven days ago")
		flags.StringVar(&opts.unitDir, "unit-dir", os.Getenv("BOXPANEL_UNIT_DIR"), "systemd unit directory (BOXPANEL_UNIT_DIR; default /etc/systemd/system)")
		flags.StringVar(&opts.panelBinary, "panel-binary", os.Getenv("BOXPANEL_BINARY"), "panel executable for systemd (BOXPANEL_BINARY; default /usr/local/bin/boxpanel)")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected singbox arguments")
	}
	var err error
	if opts.command == "install" {
		opts.version, err = singbox.NormalizeVersion(opts.version)
		if err != nil {
			return opts, err
		}
	}
	opts.paths, err = paths.resolve(opts.dev)
	if err != nil {
		return opts, err
	}
	opts.core = core.resolve(paths, opts.dev)
	if opts.unitDir == "" {
		opts.unitDir = singbox.DefaultUnitDir
	}
	if opts.panelBinary == "" {
		opts.panelBinary = "/usr/local/bin/boxpanel"
	}
	return opts, nil
}

type binaryInstaller interface {
	Install(context.Context, string, string) error
}
type singboxDependencies struct {
	os         string
	installer  binaryInstaller
	controller singbox.Controller
	runner     host.Runner
	files      host.Files
}

func runSingbox(args []string, stdout, stderr io.Writer) int {
	opts, err := parseSingboxOptions(args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v; 请运行 'boxpanel singbox --help' 检查参数。\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := singboxCommand(ctx, opts, singboxDependencies{}, stdout); err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v; 请检查 GitHub 连接、路径权限与 systemd 状态，并按 'boxpanel singbox %s --help' 重试。\n", err, opts.command)
		return 1
	}
	return 0
}

func singboxCommand(ctx context.Context, opts singboxOptions, deps singboxDependencies, output io.Writer) error {
	if deps.os == "" {
		deps.os = runtime.GOOS
	}
	if opts.dev || (deps.os != "linux" && opts.command == "status") {
		fmt.Fprintf(output, "sing-box: 开发环境模拟状态（未安装、未运行）；锁定版本 %s。Linux 生产模式下可查看真实状态。\n", singbox.PinnedVersion)
		return nil
	}
	if deps.os != "linux" {
		return fmt.Errorf("服务安装需要 Linux/systemd；本机校验工具请运行 make tools")
	}
	if deps.runner == nil {
		deps.runner = host.Native{}
	}
	if deps.files == nil {
		deps.files = host.Native{}
	}
	if deps.installer == nil {
		deps.installer = singbox.Installer{OS: deps.os, Runner: deps.runner}
	}
	if deps.controller == nil {
		deps.controller = singbox.NewSystemd(singbox.SystemdOptions{OS: deps.os, Runner: deps.runner, Files: deps.files})
	}
	inspector := singbox.Inspector{OS: deps.os, Binary: opts.core.binary, Runner: deps.runner, Controller: deps.controller}
	if opts.command == "status" {
		status, err := inspector.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "sing-box: installed=%t version=%s running=%t uptime=%ds pinned=%s\n", status.Installed, status.Version, status.Running, status.Uptime, singbox.PinnedVersion)
		if !status.Installed {
			fmt.Fprintln(output, "请先运行 boxpanel singbox install 安装内核。")
		}
		return nil
	}
	// Resolve every path before writing either binary or unit. Reject collisions
	// with the database, credentials, configuration and service definitions.
	seen := map[string]bool{}
	for _, pointer := range []*string{&opts.panelBinary, &opts.core.binary, &opts.core.config, &opts.paths.Database, &opts.paths.CertFile, &opts.paths.KeyFile, &opts.unitDir} {
		abs, err := filepath.Abs(*pointer)
		if err != nil {
			return fmt.Errorf("resolve installation path: %w", err)
		}
		if seen[abs] {
			return fmt.Errorf("installation paths must be distinct")
		}
		seen[abs], *pointer = true, abs
	}
	for _, unit := range []string{singbox.PanelUnit, singbox.SingboxUnit} {
		if seen[filepath.Join(opts.unitDir, unit)] {
			return fmt.Errorf("installation path conflicts with a systemd unit")
		}
	}
	units, err := singbox.GenerateUnits(singbox.UnitPaths{PanelBinary: opts.panelBinary, Binary: opts.core.binary, Config: opts.core.config, Database: opts.paths.Database, CertFile: opts.paths.CertFile, KeyFile: opts.paths.KeyFile})
	if err != nil {
		return err
	}
	conn, err := app.OpenExisting(ctx, opts.paths.Database)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := app.LoadConfig(ctx, conn); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.core.binary), 0755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	lock := filepath.Join(filepath.Dir(opts.core.binary), ".boxpanel-singbox-install.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("lock installation (if interrupted, confirm no installer is running before removing %s): %w", lock, err)
	}
	f.Close()
	defer os.Remove(lock)
	inspector.Binary = opts.core.binary
	status, err := inspector.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Installed || status.Version != opts.version {
		// Live upgrades require configuration validation and rollback (T7.6).
		// In T2.1 a version switch is allowed only before a config exists.
		state, err := deps.controller.State(ctx, singbox.SingboxUnit)
		if err != nil {
			return err
		}
		if status.Running || state.Running {
			return fmt.Errorf("sing-box is running; stop the service before installing a different binary")
		}
		if _, err := os.Lstat(opts.core.config); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return fmt.Errorf("inspect existing sing-box config: %w", err)
			}
			return fmt.Errorf("existing sing-box configuration requires a validated upgrade; T2.1 only supports installation/version selection before configuration")
		}
		if err := deps.installer.Install(ctx, opts.core.binary, opts.version); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(opts.unitDir, 0755); err != nil {
		return fmt.Errorf("create systemd unit directory: %w", err)
	}
	for _, unit := range []string{singbox.PanelUnit, singbox.SingboxUnit} {
		if err := deps.files.WriteFile(filepath.Join(opts.unitDir, unit), units[unit], 0644); err != nil {
			return fmt.Errorf("binary installed; write %s (retry install to finish): %w", unit, err)
		}
	}
	if err := deps.controller.Reload(ctx); err != nil {
		return fmt.Errorf("binary and units installed; retry install to finish: %w", err)
	}
	if err := db.SetSetting(ctx, conn, db.SingboxVersion, opts.version); err != nil {
		return fmt.Errorf("binary and units installed; record version (retry install to finish): %w", err)
	}
	fmt.Fprintf(output, "sing-box %s 已安装，systemd 单元已写入。尚未启用或启动服务；请在配置生成与生效流程可用后继续配置节点。\n", opts.version)
	return nil
}
