package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/traffic"
)

const removalHint = "已被主控移除，可执行 boxpanel leave 清理或重新加入。若主控仍列出此节点，请先检查节点时钟。"

func runAgentCommand(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	var masterURL, pin, token, publicIP string
	if command == "join" {
		flags.StringVar(&masterURL, "master", "", "master panel HTTPS URL, including its panel path")
		flags.StringVar(&pin, "master-pin", "", "base64url SHA-256 SPKI pin for the master fallback key")
		flags.StringVar(&token, "token", "", "one-use registration token")
		flags.StringVar(&publicIP, "public-ip", "", "public IPv4 or IPv6 address to advertise")
	}
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil || flags.NArg() != 0 || (command == "join" && (masterURL == "" || token == "")) {
		fmt.Fprintf(stderr, "参数无效，请运行 boxpanel %s --help 检查参数。\n", command)
		return 2
	}
	resolved, err := paths.resolve(false)
	if err == nil {
		err = app.ValidateAgentKeyPath(resolved)
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if command == "leave" {
			err = app.LeaveNode(ctx, resolved)
			if err == nil {
				fmt.Fprintln(stdout, "已清理节点身份。请在主控移除旧服务器记录，或使用新的加入命令重新注册。")
			}
		} else {
			err = joinNode(ctx, resolved, masterURL, pin, token, publicIP, stdout)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v\n请核对数据目录和主控面板上的加入命令后重试。\n", err)
		return 1
	}
	return 0
}

func joinNode(ctx context.Context, paths app.Paths, masterURL, pin, token, publicIP string, output io.Writer) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("读取本机主机名，请检查系统设置: %w", err)
	}
	if publicIP == "" {
		publicIP = os.Getenv("BOXPANEL_PUBLIC_IP")
	}
	if publicIP == "" {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ip, err := app.DefaultIPProbe(probeCtx)
		cancel()
		if err == nil {
			publicIP = ip.String()
		}
	} else if ip, err := netip.ParseAddr(publicIP); err != nil || ip.Zone() != "" {
		return agent.ErrPublicIP
	}
	identity, err := app.JoinNode(ctx, paths, masterURL, pin, token, hostname, publicIP, version)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "已加入主控（server_id=%d）。请启动 boxpanel 服务并在主控确认节点在线。\n", identity.ServerID)
	return nil
}

func printAgentInfo(ctx context.Context, conn *sql.DB, stdout io.Writer) error {
	master, err := db.GetSetting(ctx, conn, db.AgentMaster)
	if err != nil {
		return err
	}
	result, err := db.GetSetting(ctx, conn, db.AgentLastHeartbeat)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fmt.Fprintf(stdout, "节点模式（agent）\n主控: %s\nserver_id: %d\n", master.MasterURL, master.ServerID)
	if result.At == 0 {
		fmt.Fprintln(stdout, "最近心跳: 尚未发送，请启动 boxpanel 服务。")
		return nil
	}
	message := "未能联系主控，请核对网络和证书。"
	switch result.Status {
	case "ok":
		message = "成功"
	case "unauthorized":
		message = "认证失败，请检查节点时钟和主控中的服务器记录。"
	case "clock_skew":
		message = (&agent.ClockSkewError{Seconds: result.ClockSkewSeconds}).Error()
	}
	fmt.Fprintf(stdout, "最近心跳: %s（%s）\n", message, time.Unix(result.At, 0).UTC().Format(time.RFC3339))
	if result.RepeatedUnauthorized {
		fmt.Fprintln(stdout, removalHint)
	}
	return nil
}

func serveAgent(ctx context.Context, conn *sql.DB, opts serveOptions, logger *slog.Logger) error {
	master, err := db.GetSetting(ctx, conn, db.AgentMaster)
	if err != nil {
		return err
	}
	key, err := app.LoadAgentKey(opts.paths, master.PublicKey)
	if err != nil {
		return err
	}
	client, err := agent.NewClient(master.MasterURL, master.Pin)
	if err != nil {
		return err
	}
	defer client.Close()
	reader, err := db.OpenReader(ctx, opts.paths.Database)
	if err != nil {
		return err
	}
	defer reader.Close()
	manager, err := newApplyManager(conn, opts.paths, opts.core, singbox.ApplyOptions{Dev: opts.dev, ReadDB: reader})
	if err != nil {
		return err
	}
	if err := manager.Startup(ctx); err != nil {
		return fmt.Errorf("节点内核启动失败，请检查 sing-box 安装与系统服务后重试: %w", err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !opts.dev && runtime.GOOS == "linux" {
		native := host.Native{}
		network := alerts.New(conn, reader, alerts.Options{Network: &alerts.Network{Files: native, Runner: native}, Logger: logger})
		scheduler := traffic.NewScheduler(traffic.NewCollector(conn, reader, nil), manager, nil, logger)
		netDone, trafficDone := make(chan struct{}), make(chan struct{})
		go func() { defer close(netDone); network.RunTraffic(workerCtx) }()
		go func() { defer close(trafficDone); scheduler.Run(workerCtx) }()
		defer func() { cancel(); <-netDone; <-trafficDone }()
	}
	logger.Info("已启动节点模式", "server_id", master.ServerID)
	started := time.Now()
	send := func(ctx context.Context) error {
		return client.Heartbeat(ctx, master.ServerID, key, agent.Heartbeat{
			Version: version, SingboxActive: !opts.dev && manager.CoreActive(ctx), Uptime: int64(time.Since(started) / time.Second),
		})
	}
	runHeartbeats(ctx, conn, master, send, logger)
	return nil
}

func runHeartbeats(ctx context.Context, conn *sql.DB, master db.AgentConnection, send func(context.Context) error, logger *slog.Logger) {
	last, _ := db.GetSetting(ctx, conn, db.AgentLastHeartbeat)
	streak, backoff := 0, 5*time.Second
	for ctx.Err() == nil {
		current, err := db.GetSetting(ctx, conn, db.AgentMaster)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current != master) {
			return
		}
		delay := backoff
		if err == nil {
			sendErr := send(ctx)
			if ctx.Err() != nil {
				return
			}
			next, wait := nextHeartbeat(last, sendErr, &streak, &backoff, time.Now())
			delay = wait
			saved, saveErr := recordHeartbeatResult(ctx, conn, master, next)
			err = saveErr
			if errors.Is(err, sql.ErrNoRows) || (err == nil && !saved) {
				return
			}
			if err == nil {
				logHeartbeatResult(logger, last, next)
			}
			last = next
		}
		if err != nil && ctx.Err() == nil {
			logger.Warn("无法更新最近心跳结果，请检查节点数据库权限。")
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func logHeartbeatResult(logger *slog.Logger, last, next db.AgentHeartbeatResult) {
	if next.Status == "clock_skew" && last.Status != next.Status {
		logger.Warn((&agent.ClockSkewError{Seconds: next.ClockSkewSeconds}).Error())
	}
	if next.RepeatedUnauthorized && !last.RepeatedUnauthorized {
		logger.Warn(removalHint)
	}
}

func nextHeartbeat(last db.AgentHeartbeatResult, err error, streak *int, backoff *time.Duration, now time.Time) (db.AgentHeartbeatResult, time.Duration) {
	result := db.AgentHeartbeatResult{At: now.Unix(), RepeatedUnauthorized: last.RepeatedUnauthorized}
	if err == nil {
		*streak, *backoff = 0, 5*time.Second
		result.Status, result.RepeatedUnauthorized = "ok", false
		return result, agent.HeartbeatInterval
	}
	var skew *agent.ClockSkewError
	switch {
	case errors.As(err, &skew):
		*streak = 0
		result.Status, result.ClockSkewSeconds = "clock_skew", skew.Seconds
		result.RepeatedUnauthorized = false
	case errors.Is(err, agent.ErrUnauthorized):
		*streak = min(*streak+1, 3)
		result.Status = "unauthorized"
		result.RepeatedUnauthorized = last.RepeatedUnauthorized || *streak >= 3
	default:
		*streak = 0
		result.Status = "unreachable"
	}
	delay := *backoff
	*backoff = min(*backoff*2, time.Minute)
	return result, delay
}

func recordHeartbeatResult(ctx context.Context, conn *sql.DB, master db.AgentConnection, next db.AgentHeartbeatResult) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin recording heartbeat result: %w", err)
	}
	defer tx.Rollback()
	current, err := db.GetSetting(ctx, tx, db.AgentMaster)
	if err != nil || current != master {
		return false, err
	}
	if err := db.SetSetting(ctx, tx, db.AgentLastHeartbeat, next); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit heartbeat result: %w", err)
	}
	return true, nil
}

func checkAgentCorePath(paths app.Paths, names ...string) error {
	key, err := canonicalPath(app.AgentKeyPath(paths))
	if err != nil {
		return err
	}
	for _, name := range names {
		other, err := canonicalPath(name)
		if err != nil {
			return err
		}
		if key == other || key == filepath.Dir(other) {
			return fmt.Errorf("sing-box 路径与 agent.key 冲突，请使用独立的内核路径")
		}
	}
	return nil
}
