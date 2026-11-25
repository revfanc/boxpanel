package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

func runSetMaster(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("set-master", flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	flags.Usage = func() {
		fmt.Fprintln(stdout, "Usage: boxpanel set-master <HTTPS URL> [path options]")
		flags.PrintDefaults()
	}
	var addresses []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		args = flags.Args()
		if len(args) > 0 {
			addresses, args = append(addresses, args[0]), args[1:]
		}
	}
	if len(addresses) != 1 {
		fmt.Fprintln(stderr, "请运行 boxpanel set-master <HTTPS URL>，填写新主控的完整面板地址。")
		return 2
	}
	resolved, err := paths.resolve(false)
	if err != nil {
		fmt.Fprintln(stderr, "请检查 --data-dir 和 --db-path 后重试。")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	address, err := setMaster(ctx, resolved, addresses[0])
	if err != nil {
		fmt.Fprintf(stderr, "主控地址未能保存：%v。请核对新主控的地址、证书和节点记录后重试。\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "主控地址已保存：%s\n节点下次心跳采用新地址。可运行 boxpanel info 查看结果。\n", address)
	return 0
}

func setMaster(ctx context.Context, paths app.Paths, address string) (string, error) {
	conn, err := app.OpenExisting(ctx, paths.Database)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if role, err := db.GetSetting(ctx, conn, db.NodeRole); err != nil || role != db.RoleAgent {
		return "", fmt.Errorf("此命令适用于已加入的节点，请在节点上执行")
	}
	current, err := db.GetSetting(ctx, conn, db.AgentMaster)
	if err != nil {
		return "", err
	}
	key, err := app.LoadAgentKey(paths, current.PublicKey)
	if err != nil {
		return "", err
	}
	return agent.SetMaster(ctx, conn, current, key, address, agent.Heartbeat{Version: version})
}
