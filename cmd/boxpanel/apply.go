package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/singbox"
)

func runApply(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	core := addSingboxPaths(flags)
	dev := flags.Bool("dev", false, "development environment: apply is unsupported without Linux/systemd")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "请运行 boxpanel apply --help 检查参数。")
		return 2
	}
	resolved, err := paths.resolve(*dev)
	if err != nil {
		fmt.Fprintln(stderr, "请检查 boxpanel apply --help 中的数据路径参数。")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	conn, err := app.OpenExisting(ctx, resolved.Database)
	if err == nil {
		defer conn.Close()
		if _, err = app.LoadConfig(ctx, conn); err == nil {
			var manager *singbox.Manager
			manager, err = newApplyManager(conn, resolved, core.resolve(paths, *dev), singbox.ApplyOptions{Dev: *dev})
			if err == nil {
				err = manager.Restart(ctx)
			}
		}
	}
	if err != nil {
		// Never print SQL/command output, which can contain stored credentials.
		fmt.Fprintln(stderr, "生效操作未能确认成功。请确认已初始化数据库并安装内核，在 Linux 上关闭 --dev，检查配置、证书、systemd 和防火墙权限后运行 boxpanel apply 重试。")
		var apply *singbox.ApplyError
		if errors.As(err, &apply) {
			fmt.Fprintln(stderr, apply.Error())
			if apply.Detail != "" {
				fmt.Fprintln(stderr, apply.Detail)
			}
		}
		return 1
	}
	fmt.Fprintln(stdout, "sing-box 配置已校验并生效，服务存活检查与防火墙同步成功。")
	return 0
}

func newApplyManager(conn *sql.DB, paths app.Paths, core singboxPaths, opts singbox.ApplyOptions) (*singbox.Manager, error) {
	// Include auxiliary files in collision checks: apply must never replace the
	// database, panel TLS keys, executable, or either systemd unit.
	protected := []string{paths.Database, paths.Database + "-wal", paths.Database + "-shm", paths.CertFile, paths.KeyFile, core.binary}
	for _, unit := range []string{singbox.PanelUnit, singbox.SingboxUnit} {
		protected = append(protected, filepath.Join(singbox.DefaultUnitDir, unit))
	}
	for _, suffix := range []string{"", ".tmp", ".bak", ".pending", ".lock"} {
		candidate, err := canonicalPath(core.config + suffix)
		if err != nil {
			return nil, err
		}
		for _, path := range protected {
			other, err := canonicalPath(path)
			if err != nil {
				return nil, err
			}
			if candidate == other {
				return nil, fmt.Errorf("config path conflicts with protected files; choose a separate --singbox-config")
			}
		}
	}
	opts.Binary, opts.Config = core.binary, core.config
	return singbox.NewManager(conn, opts)
}
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(abs); e == nil {
		return resolved, nil
	}
	dir, e := filepath.EvalSymlinks(filepath.Dir(abs))
	if e == nil {
		return filepath.Join(dir, filepath.Base(abs)), nil
	}
	return abs, nil
}
