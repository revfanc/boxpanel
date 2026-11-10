package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type setHostDependencies struct {
	os     string
	runner host.Runner
}

func runSetHost(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return setHostCommand(ctx, args, setHostDependencies{}, stdout, stderr)
}

func setHostCommand(ctx context.Context, args []string, deps setHostDependencies, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("set-host", flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	dev := flags.Bool("dev", false, "use the local development database; do not restart system services")
	flags.Usage = func() {
		fmt.Fprintln(stdout, "Usage: boxpanel set-host <IP|domain> [path options]\nPath options may also precede the host. Saves public_host, then attempts to restart boxpanel on Linux/systemd.")
		flags.PrintDefaults()
	}
	// flag stops at a positional argument; resume so paths work before or after
	// the host, without interpreting any value as a shell command.
	var names []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			fmt.Fprintln(stderr, "参数无效；请运行 boxpanel set-host --help 检查用法。")
			return 2
		}
		args = flags.Args()
		if len(args) > 0 {
			names = append(names, args[0])
			args = args[1:]
		}
	}
	if len(names) != 1 || !rules.ValidHost(names[0]) {
		fmt.Fprintln(stderr, "请运行 boxpanel set-host <IP|域名>，填写一个域名或裸 IPv4/IPv6 地址，不含协议、端口或路径。")
		return 2
	}
	resolved, err := paths.resolve(*dev)
	if err != nil {
		fmt.Fprintln(stderr, "路径参数无效；请运行 boxpanel set-host --help，检查数据、证书和私钥路径。")
		return 2
	}
	conn, err := app.OpenExisting(ctx, resolved.Database)
	if err != nil {
		fmt.Fprintln(stderr, "无法打开面板数据库；请确认已初始化，并使用相同的 --data-dir/--db-path 和正确的文件权限重试。")
		return 1
	}
	cfg, err := app.SetPublicHost(ctx, conn, names[0])
	_ = conn.Close()
	if err != nil {
		fmt.Fprintln(stderr, "节点地址未能保存；请检查数据库是否已初始化、是否可写，稍后使用相同路径参数重试。")
		return 1
	}
	fmt.Fprintf(stdout, "节点地址已保存。\n面板地址: %s\n", cfg.URL())
	if !*dev && cfg.TLSMode == "acme" {
		fmt.Fprintln(stdout, "运行中的证书服务会在下一轮检查（通常一分钟内）为新地址重新申请证书；请放行云安全组 TCP 80，域名须指向本机。签发结果请到面板设置查看。")
	}
	if *dev || cfg.TLSMode == "http" {
		fmt.Fprintln(stdout, "开发模式仅更新地址，面板仍监听 127.0.0.1:2053；请在生产环境验证节点连接。")
		return 0
	}
	fmt.Fprintln(stdout, "执行 systemctl restart boxpanel 立即生效；非 systemd 部署请重启使用相同数据路径的面板进程。")
	if deps.os == "" {
		deps.os = runtime.GOOS
	}
	if deps.os == "linux" {
		if deps.runner == nil {
			deps.runner = host.Native{}
		}
		_, err = deps.runner.LookPath("systemctl")
		if err == nil {
			err = singbox.NewSystemd(singbox.SystemdOptions{OS: deps.os, Runner: deps.runner}).Restart(ctx, singbox.PanelUnit)
		}
		if err != nil {
			fmt.Fprintln(stderr, "地址已保存，但自动重启面板未完成；请检查 systemd 和权限，执行 systemctl restart boxpanel 重试，并用 journalctl -u boxpanel 查看启动状态。")
		} else {
			fmt.Fprintln(stdout, "已重启 boxpanel 面板服务；证书签发异步进行。")
		}
	}
	return 0
}
