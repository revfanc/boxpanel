package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/revfanc/boxpanel/internal/acme"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
)

// T7.5 deliberately supports one portable layout. Reject external overrides
// rather than producing an archive which silently loses pinned certificates.
func backupLayout(p app.Paths, c singboxPaths) error {
	dir, err := filepath.Abs(p.DataDir)
	if err != nil {
		return err
	}
	for _, item := range []struct{ actual, relative string }{{p.Database, "boxpanel.db"}, {p.CertFile, "certs/panel.crt"}, {p.KeyFile, "certs/panel.key"}, {c.config, "sing-box.json"}} {
		abs, e := filepath.Abs(item.actual)
		if e != nil || abs != filepath.Join(dir, item.relative) {
			return fmt.Errorf("备份恢复需要 DB、证书和配置位于 --data-dir 的标准布局；请先将自定义路径迁移到该布局")
		}
	}
	binary, e := filepath.Abs(c.binary)
	if e != nil {
		return e
	}
	if rel, e := filepath.Rel(dir, binary); e != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("受管内核必须放在数据目录之外；请调整 --singbox-path 后重试")
	}
	if dir == string(filepath.Separator) {
		return fmt.Errorf("数据目录不能是根目录；请检查 --data-dir")
	}
	if info, e := os.Lstat(dir); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("数据目录不能是符号链接；请使用实际目录路径")
	}
	return nil
}

func readPassphrase(name string, output io.Writer) (string, error) {
	var b []byte
	var err error
	if name != "" {
		f, e := os.Open(name)
		if e != nil {
			return "", fmt.Errorf("读取口令文件失败；请检查路径及权限")
		}
		defer f.Close()
		b, err = io.ReadAll(io.LimitReader(f, 1027))
		if err != nil {
			return "", err
		}
	} else {
		// Read the controlling terminal, never piped installer input. Disable echo
		// using the platform termios helpers without adding x/term as a dependency.
		f, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil {
			return "", fmt.Errorf("无法读取交互口令；请使用 --passphrase-file 指定权限为 0600 的口令文件")
		}
		defer f.Close()
		restore, e := hideTerminal(f)
		if e != nil {
			return "", e
		}
		defer restore()
		fmt.Fprint(output, "备份口令（至少 12 字符，不回显）: ")
		b, err = bufio.NewReader(io.LimitReader(f, 1027)).ReadBytes('\n')
		fmt.Fprintln(output)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("读取口令失败；请重试或使用 --passphrase-file")
	}
	p := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	clear(b)
	if !backup.ValidPassphrase(p) {
		return "", fmt.Errorf("口令必须至少 12 字符且不超过 1024 字节；请修改口令后重试")
	}
	return p, nil
}

func runBackup(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	paths.addACME(flags)
	core := addSingboxPaths(flags)
	passfile := flags.String("passphrase-file", "", "read passphrase from a file; never put the passphrase in command arguments")
	output := ""
	install := false
	if command == "backup" {
		flags.StringVar(&output, "output", "", "new output file; default boxpanel-backup-TIMESTAMP.tar.gz.enc")
	} else {
		flags.BoolVar(&install, "install", false, "install missing core and units on a fresh machine (installer only)")
	}
	// Options may appear before, after or around the file (as with set-host):
	// flag stops at a positional argument, so resume parsing after each one.
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		args = flags.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if command == "backup" && len(positional) != 0 || command != "backup" && len(positional) != 1 {
		fmt.Fprintln(stderr, "参数有误；请运行 boxpanel "+command+" --help。")
		return 2
	}
	p, err := paths.resolve(false)
	c := core.resolve(paths, false)
	if err == nil {
		p, c, err = absoluteBackupPaths(p, c)
	}
	if err == nil {
		err = backupLayout(p, c)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err == nil && command == "backup" {
		var pass string
		pass, err = readPassphrase(*passfile, stderr)
		if err == nil {
			var service *backup.Service
			var data []byte
			conn, e := app.OpenExisting(ctx, p.Database)
			err = e
			if err == nil {
				service = &backup.Service{DB: conn, DataDir: p.DataDir, Version: version}
				data, err = service.Create(ctx, pass)
				conn.Close()
			}
			pass = ""
			if output == "" {
				output = "boxpanel-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz.enc"
			}
			if err == nil {
				err = backup.WriteNew(output, data)
			}
			clear(data)
			if err == nil {
				fmt.Fprintf(stdout, "加密备份已保存：%s。请将文件与口令分别保存在服务器之外。\n", output)
			}
		}
	} else if err == nil {
		if runtime.GOOS != "linux" {
			err = fmt.Errorf("恢复服务需要 Linux/systemd；请在目标 VPS 执行此命令")
		} else {
			var stage string
			var host string
			if command == "restore-staged" {
				// This private handoff contains no passphrase and is owned by root. Validate
				// again using the same snapshot/archive pipeline before stopping services.
				incoming := positional[0]
				if filepath.Dir(incoming) != filepath.Dir(p.DataDir) || !strings.HasPrefix(filepath.Base(incoming), ".boxpanel-restore-") {
					err = backup.ErrInvalid
				} else {
					stage, host, err = restage(ctx, incoming, filepath.Dir(p.DataDir))
					defer os.RemoveAll(incoming)
				}
			} else {
				var data []byte
				data, err = readBackupFile(positional[0])
				pass := ""
				if err == nil && (backup.IsEncrypted(data) || *passfile != "") {
					pass, err = readPassphrase(*passfile, stderr)
				}
				var a backup.Archive
				if err == nil {
					a, err = backup.Decode(data, pass, false)
				}
				clear(data)
				pass = ""
				if err == nil {
					host = a.Manifest.PublicHost
					stage, err = backup.Stage(ctx, a, filepath.Dir(p.DataDir))
				}
			}
			if stage != "" {
				defer os.RemoveAll(stage)
			}
			if err == nil {
				err = restoreCommand(ctx, p, c, stage, install, paths.acmeDirectory, stdout)
			}
			if err == nil {
				fmt.Fprintln(stdout, "恢复完成，所有旧会话已失效。请使用备份中的管理员凭证登录。")
				if net.ParseIP(host) != nil {
					ip, e := app.DefaultIPProbe(ctx)
					if e != nil || ip.String() != host {
						fmt.Fprintln(stdout, "请核对本机公网 IP；若已改变，可运行 boxpanel set-host <新IP或域名> 更新地址，或修改域名解析。IP 订阅地址变更需更新客户端地址。")
					}
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v；请检查备份文件、目标路径、磁盘空间及 systemd 权限后重试。\n", err)
		return 1
	}
	return 0
}

func readBackupFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("打开备份失败；请检查文件路径和权限")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, backup.MaxSize+1))
	if err != nil || len(b) > backup.MaxSize {
		return nil, backup.ErrInvalid
	}
	return b, nil
}

func restage(ctx context.Context, incoming, parent string) (string, string, error) {
	info, err := os.Lstat(incoming)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", backup.ErrInvalid
	}
	dbInfo, e := os.Lstat(filepath.Join(incoming, "boxpanel.db"))
	if e != nil || !dbInfo.Mode().IsRegular() {
		return "", "", backup.ErrInvalid
	}
	conn, err := app.OpenExisting(ctx, filepath.Join(incoming, "boxpanel.db"))
	if err != nil {
		return "", "", backup.ErrInvalid
	}
	data, err := backup.Create(ctx, conn, filepath.Join(incoming, "certs"), version, "", time.Now())
	conn.Close()
	if err != nil {
		return "", "", err
	}
	a, err := backup.Decode(data, "", false)
	if err != nil {
		return "", "", err
	}
	stage, err := backup.Stage(ctx, a, parent)
	return stage, a.Manifest.PublicHost, err
}

func restoreCommand(ctx context.Context, p app.Paths, c singboxPaths, stage string, install bool, directory string, out io.Writer) error {
	if _, err := os.Stat(p.Database); err == nil {
		conn, err := app.OpenExisting(ctx, p.Database)
		if err != nil {
			return err
		}
		release, err := beginMasterWrite(ctx, conn, p)
		conn.Close()
		if err != nil {
			return err
		}
		defer release()
	}
	return restoreWith(ctx, p, c, stage, install, directory, out, restoreDependencies{})
}

type restoreFirewall interface {
	Sync(context.Context, firewall.Plan, firewall.Plan) error
	Open(context.Context, int, string) error
}
type restoreDependencies struct {
	controller singbox.Controller
	fw         restoreFirewall
	apply      singbox.ApplyOptions
	ready      func(context.Context, app.Paths) error
	install    func(context.Context) error
}

func restoreWith(ctx context.Context, p app.Paths, c singboxPaths, stage string, install bool, directory string, out io.Writer, deps restoreDependencies) error {
	controller := deps.controller
	if controller == nil {
		controller = singbox.NewSystemd(singbox.SystemdOptions{})
	}
	if deps.fw == nil {
		deps.fw = firewall.New(firewall.Options{})
	}
	if deps.ready == nil {
		deps.ready = waitRestoredPanel
	}
	deps.apply.Controller = controller
	deps.apply.Firewall = deps.fw
	panelBefore, err := controller.State(ctx, singbox.PanelUnit)
	if err != nil {
		return err
	}
	coreBefore, err := controller.State(ctx, singbox.SingboxUnit)
	if err != nil {
		return err
	}
	var before, after firewall.Plan
	captured := false
	stop := func(ctx context.Context) error {
		for _, unit := range []string{singbox.PanelUnit, singbox.SingboxUnit} {
			if install && deps.install == nil {
				if _, e := os.Stat(filepath.Join(singbox.DefaultUnitDir, unit)); errors.Is(e, os.ErrNotExist) {
					continue
				}
			}
			if e := controller.Stop(ctx, unit); e != nil {
				return e
			}
		}
		if !captured {
			captured = true
			if _, e := os.Stat(p.Database); e == nil {
				conn, e := app.OpenExisting(ctx, p.Database)
				if e != nil {
					return e
				}
				defer conn.Close()
				before, e = readPlan(ctx, conn)
				if e != nil {
					return e
				}
				ports, e := db.GetSetting(ctx, conn, db.PanelFirewallPorts)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				for _, port := range ports {
					before.Ports = append(before.Ports, firewall.Port{Port: port, Proto: "tcp"})
				}
				manager, e := newApplyManager(conn, p, c, deps.apply)
				if e != nil {
					return e
				}
				return manager.CleanupFirewall(ctx)
			}
		}
		return nil
	}
	apply := func(ctx context.Context) error {
		if install && deps.install != nil {
			if e := deps.install(ctx); e != nil {
				return e
			}
		} else if install {
			if e := singboxCommand(ctx, singboxOptions{command: "install", version: singbox.PinnedVersion, preserveVersion: true, paths: p, core: c, acmeDirectory: directory, unitDir: singbox.DefaultUnitDir, panelBinary: "/usr/local/bin/boxpanel"}, singboxDependencies{}, out); e != nil {
				return e
			}
		}
		conn, e := app.OpenExisting(ctx, p.Database)
		if e != nil {
			return e
		}
		defer conn.Close()
		// Source-machine runtime tracking is not target-machine firewall state.
		// Old target rules were cleaned before swapping directories.
		if e = writePlan(ctx, conn, firewall.Plan{}); e != nil {
			return e
		}
		if e = db.SetSetting(ctx, conn, db.PanelFirewallPorts, []int{}); e != nil {
			return e
		}
		manager, e := newApplyManager(conn, p, c, deps.apply)
		if e == nil {
			e = manager.Restart(ctx)
		}
		after, _ = readPlan(ctx, conn)
		if e != nil {
			return fmt.Errorf("恢复配置未能生效；请检查内核版本、证书和防火墙权限")
		}
		cfg, e := app.LoadConfig(ctx, conn)
		if e != nil {
			return e
		}
		// Record even partial port opening for rollback cleanup.
		after.Ports = append(after.Ports, firewall.Port{Port: cfg.WebPort, Proto: "tcp"}, firewall.Port{Port: 80, Proto: "tcp"})
		return app.OpenPanelFirewall(ctx, conn, cfg.WebPort, true, deps.fw)
	}
	start := func(ctx context.Context) error {
		if install {
			for _, unit := range []string{singbox.SingboxUnit, singbox.PanelUnit} {
				if e := controller.Enable(ctx, unit); e != nil {
					return e
				}
			}
		}
		if e := controller.Start(ctx, singbox.PanelUnit); e != nil {
			return e
		}
		return deps.ready(ctx, p)
	}
	recover := func(ctx context.Context, exists bool) error {
		fw := deps.fw
		if e := fw.Sync(ctx, after, before); e != nil {
			return e
		}
		if coreBefore.Running {
			conn, e := app.OpenExisting(ctx, p.Database)
			if e != nil {
				return e
			}
			manager, e := newApplyManager(conn, p, c, deps.apply)
			if e == nil {
				e = manager.Restart(ctx)
			}
			conn.Close()
			if e != nil {
				return e
			}
		}
		if panelBefore.Running && exists {
			if e := controller.Start(ctx, singbox.PanelUnit); e != nil {
				return e
			}
			return deps.ready(ctx, p)
		}
		return nil
	}
	saved, err := backup.Restore(ctx, p.DataDir, stage, backup.Hooks{Stop: stop, Apply: apply, Start: start, Recover: recover})
	if err == nil && saved != "" {
		fmt.Fprintf(out, "恢复前的数据完整保存在：%s\n", saved)
	}
	return err
}

func waitRestoredPanel(ctx context.Context, p app.Paths) error {
	// Production readiness verifies the local TLS trust anchor, never insecure.
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	for {
		conn, err := app.OpenExisting(ctx, p.Database)
		if err == nil {
			cfg, e := app.LoadConfig(ctx, conn)
			var file string
			if e == nil {
				file, e = acme.ActiveFile(ctx, conn, p.CertificateDir(), p.CertFile)
			}
			conn.Close()
			if e == nil && panelReady(ctx, cfg, file) == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("面板未就绪；请检查 systemd 日志与恢复的面板端口")
		case <-time.After(time.Second):
		}
	}
}

func readPlan(ctx context.Context, conn *sql.DB) (firewall.Plan, error) {
	var plan firewall.Plan
	raw, err := db.GetSetting(ctx, conn, db.AppliedFirewall)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &plan)
	}
	return plan, err
}
func writePlan(ctx context.Context, conn *sql.DB, plan firewall.Plan) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return db.SetSetting(ctx, conn, db.AppliedFirewall, json.RawMessage(raw))
}

func absoluteBackupPaths(p app.Paths, c singboxPaths) (app.Paths, singboxPaths, error) {
	for _, v := range []*string{&p.DataDir, &p.Database, &p.CertFile, &p.KeyFile, &c.binary, &c.config} {
		abs, err := filepath.Abs(*v)
		if err != nil {
			return p, c, err
		}
		*v = abs
	}
	return p, c, nil
}
