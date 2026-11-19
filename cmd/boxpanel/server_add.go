package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

// releaseTag matches published tags; other builds (dev, test) use main.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

// runServer implements `boxpanel server add <name>`: issue a one-time join
// token from the CLI, for administrators who only have SSH access.
func runServer(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stderr, "用法：boxpanel server add <服务器名称> [路径参数]")
		return 2
	}
	flags := flag.NewFlagSet("server add", flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	flags.Usage = func() {
		fmt.Fprintln(stdout, "Usage: boxpanel server add <name> [path options]\nIssue a one-time join token (valid 1 hour) and print the node join command.")
		flags.PrintDefaults()
	}
	rest := args[1:]
	var names []string
	for len(rest) > 0 {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			fmt.Fprintln(stderr, "参数无效；请运行 boxpanel server add --help 查看用法。")
			return 2
		}
		rest = flags.Args()
		if len(rest) > 0 {
			names = append(names, rest[0])
			rest = rest[1:]
		}
	}
	if len(names) != 1 {
		fmt.Fprintln(stderr, "请提供一个服务器名称，例如：boxpanel server add 日本")
		return 2
	}
	name := strings.TrimSpace(names[0])
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		fmt.Fprintln(stderr, "服务器名称须为 1–64 个字符，且不含控制字符。")
		return 2
	}
	resolved, err := paths.resolve(false)
	if err != nil {
		fmt.Fprintln(stderr, "路径参数无效；请运行 boxpanel server add --help 检查数据目录参数。")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	conn, err := app.OpenExisting(ctx, resolved.Database)
	if err != nil {
		fmt.Fprintln(stderr, "无法打开面板数据库；请在主控上以 root 运行，并确认已完成安装。")
		return 1
	}
	defer conn.Close()
	role, err := db.GetSetting(ctx, conn, db.NodeRole)
	if err != nil || role != "master" {
		fmt.Fprintln(stderr, "只有主控可以添加服务器；当前机器不是主控。")
		return 1
	}
	cfg, err := app.LoadConfig(ctx, conn)
	if err != nil || cfg.TLSMode == "http" {
		fmt.Fprintln(stderr, "加入节点需要主控启用 HTTPS；请先完成安装并检查面板证书。")
		return 1
	}
	pin, err := db.GetSetting(ctx, conn, db.PanelFallbackPin)
	if err != nil || pin == "" {
		fmt.Fprintln(stderr, "主控证书指纹尚未生成；请先启动一次面板服务（systemctl restart boxpanel）后重试。")
		return 1
	}
	token, plaintext, err := agent.NewMaster(conn, conn, nil).IssueToken(ctx, name)
	if err != nil {
		fmt.Fprintln(stderr, "生成加入令牌失败；请检查数据库是否可写后重试。")
		return 1
	}
	tag := ""
	if releaseTag.MatchString(version) {
		tag = version
	}
	script := "https://raw.githubusercontent.com/revfanc/boxpanel/" + map[bool]string{true: tag, false: "main"}[tag != ""] + "/scripts/install.sh"
	command := fmt.Sprintf("bash <(curl -fsSL %s) --join %s --token %s --master-pin %s", script, shellQuote(cfg.URL()), shellQuote(plaintext), shellQuote(pin))
	if tag != "" {
		command += " --version " + tag
	}
	fmt.Fprintf(stdout, "已为「%s」生成一次性加入令牌（编号 %d，1 小时内有效，只能使用一次）。\n在新服务器上以 root 执行：\n\n%s\n\n", token.Name, token.ID, command)
	return 0
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
