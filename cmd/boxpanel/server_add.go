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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	if len(args) > 0 && (args[0] == "list" || args[0] == "remove") {
		return runServerManage(args[0], args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stderr, "用法：boxpanel server add <名称> | server list | server remove <编号>")
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
	release, err := beginMasterWrite(ctx, conn, resolved)
	if err != nil {
		fmt.Fprintln(stderr, "无法生成加入令牌。请检查主控迁移状态和数据目录权限后重试。")
		return 1
	}
	defer release()
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
	script := installScriptURL(map[bool]string{true: tag, false: "main"}[tag != ""])
	command := fmt.Sprintf("bash <(curl -fsSL %s) --join %s --token %s --master-pin %s", script, shellQuote(cfg.URL()), shellQuote(plaintext), shellQuote(pin))
	if tag != "" {
		command += " --version " + tag
	}
	fmt.Fprintf(stdout, "已为「%s」生成一次性加入令牌（编号 %d，1 小时内有效，只能使用一次）。\n在新服务器上以 root 执行：\n\n%s\n\n", token.Name, token.ID, command)
	return 0
}

// installScriptURL is the installer published with a tag or branch.
func installScriptURL(ref string) string {
	return "https://raw.githubusercontent.com/revfanc/boxpanel/" + ref + "/scripts/install.sh"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// runServerManage implements `server list` and `server remove <id>` for
// SSH-only administrators; removal uses the same path as the panel.
func runServerManage(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("server "+command, flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			fmt.Fprintln(stderr, "参数无效；请运行 boxpanel server "+command+" --help。")
			return 2
		}
		args = flags.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if command == "list" && len(positional) != 0 || command == "remove" && len(positional) != 1 {
		fmt.Fprintln(stderr, "用法：boxpanel server list | server remove <编号>")
		return 2
	}
	resolved, err := paths.resolve(false)
	if err != nil {
		fmt.Fprintln(stderr, "路径参数无效；请检查数据目录参数。")
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
	if role, err := db.GetSetting(ctx, conn, db.NodeRole); err != nil || role != "master" {
		fmt.Fprintln(stderr, "只有主控可以管理服务器；当前机器不是主控。")
		return 1
	}
	if command == "remove" {
		id, err := strconv.ParseInt(positional[0], 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintln(stderr, "请填写 boxpanel server list 中显示的服务器编号。")
			return 2
		}
		release, err := beginMasterWrite(ctx, conn, resolved)
		if err != nil {
			fmt.Fprintln(stderr, "无法移除节点。请检查主控迁移状态和数据目录权限后重试。")
			return 1
		}
		defer release()
		if err := agent.NewMaster(conn, conn, nil).Remove(ctx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fmt.Fprintln(stderr, "没有可移除的该编号节点（本机不能移除，已移除的节点无需重复操作）。")
				return 1
			}
			fmt.Fprintln(stderr, "移除失败；请检查数据库是否可写后重试。")
			return 1
		}
		fmt.Fprintf(stdout, "已移除服务器 %d：其密钥已撤销，节点之后的请求都会被拒绝；该节点不再出现在订阅中，历史流量保留。\n", id)
		return 0
	}
	rows, err := conn.QueryContext(ctx, "SELECT id,name,public_host,status,version,COALESCE(last_seen_at,0) FROM servers ORDER BY id")
	if err != nil {
		fmt.Fprintln(stderr, "读取服务器列表失败；请检查数据库权限。")
		return 1
	}
	defer rows.Close()
	fmt.Fprintf(stdout, "%-4s %-10s %-9s %-14s %-20s %s\n", "编号", "状态", "版本", "最近心跳(UTC)", "地址", "名称")
	for rows.Next() {
		var id, seen int64
		var name, host, status, ver string
		if err := rows.Scan(&id, &name, &host, &status, &ver, &seen); err != nil {
			fmt.Fprintln(stderr, "读取服务器列表失败；请检查数据库权限。")
			return 1
		}
		last := "-"
		if seen > 0 {
			last = time.Unix(seen, 0).UTC().Format("01-02 15:04:05")
		}
		fmt.Fprintf(stdout, "%-4d %-10s %-9s %-14s %-20s %s\n", id, status, ver, last, host, name)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(stderr, "读取服务器列表失败；请检查数据库权限。")
		return 1
	}
	return 0
}
