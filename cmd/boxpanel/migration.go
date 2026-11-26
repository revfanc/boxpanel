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
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/migration"
)

func beginMasterWrite(ctx context.Context, conn *sql.DB, paths app.Paths) (func(), error) {
	if filepath.Base(paths.Database) != "boxpanel.db" {
		return func() {}, nil
	}
	return migration.BeginWrite(ctx, conn, filepath.Dir(paths.Database))
}

func runMigration(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	paths.addACME(flags)
	core := addSingboxPaths(flags)
	from := flags.String("from", "", "source master HTTPS panel URL")
	stdin := flags.Bool("token-stdin", false, "read the one-time migration token from stdin")
	pin := flags.String("master-pin", "", "source fallback SPKI SHA-256 pin")
	ip := flags.String("public-ip", "", "target public IP if different from its TCP peer address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*stdin || *from == "" || !migration.ValidSecret(*pin) || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "请从旧主控复制完整迁移命令，在全新 VPS 运行。")
		return 2
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintln(stderr, "主控迁移需要 Linux/systemd，请在新 VPS 执行。")
		return 1
	}
	resolved, err := paths.resolve(false)
	c := core.resolve(paths, false)
	if err == nil {
		resolved, c, err = absoluteBackupPaths(resolved, c)
	}
	if err == nil {
		err = backupLayout(resolved, c)
	}
	if err == nil {
		token, readErr := io.ReadAll(io.LimitReader(os.Stdin, 45))
		defer clear(token)
		value := strings.TrimSuffix(string(token), "\n")
		if readErr != nil || !migration.ValidSecret(value) {
			err = migration.ErrToken
		} else {
			// Closing the SSH session or killing the installer stops the transfer
			// and withdraws the claim instead of leaving the source frozen.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			if !stopWithParent() {
				stop()
			}
			err = receiveMigration(ctx, resolved, c, *from, *pin, value, *ip, paths.acmeDirectory, stdout, restoreDependencies{})
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "未能完成主控迁移：%v。请在旧主控检查状态并生成新命令。\n", err)
		return 1
	}
	return 0
}

func receiveMigration(ctx context.Context, p app.Paths, core singboxPaths, source, pin, token, publicIP, directory string, out io.Writer, deps restoreDependencies) error {
	entries, err := os.ReadDir(p.DataDir)
	if err == nil && len(entries) != 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("新机数据目录需要为空，请在另一台全新 VPS 上安装")
	}
	client, err := migration.NewClient(source, pin)
	if err != nil {
		return err
	}
	defer client.Close()
	secret, err := migration.NewSecret()
	if err != nil {
		return err
	}
	retained := false
	abort := func() error {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return client.Abort(cancelCtx, secret)
	}
	defer func() {
		if !retained {
			_ = abort()
		}
	}()
	transfer, cancel := context.WithTimeout(ctx, migration.Lifetime)
	defer cancel()
	fmt.Fprintln(out, "[5/8] 认领令牌并接收旧主控快照")
	archive, target, expires, err := client.Download(transfer, migration.Claim{Token: token, Secret: secret, PublicIP: publicIP, Version: version})
	if err != nil {
		return err
	}
	prepare, cancel := context.WithDeadline(transfer, time.Unix(expires, 0))
	defer cancel()
	fmt.Fprintln(out, "[6/8] 校验快照、恢复身份与证书")
	stage, err := backup.Stage(prepare, archive, filepath.Dir(p.DataDir))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	defer os.RemoveAll(stage + ".failed")
	incoming, err := migration.PrepareTarget(prepare, stage, source, target, secret, pin, expires)
	if err != nil {
		return err
	}
	ready := deps.ready
	if ready == nil {
		ready = waitRestoredPanel
	}
	activated := false
	deps.ready = func(ctx context.Context, paths app.Paths) error {
		if err := ready(ctx, paths); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The installer may receive a signal independently of this process.
		// Protect the live directory once the source could commit retirement.
		// A confirmed failure moves the directory out of the way during restore rollback.
		if err := (host.Native{}).WriteFile(filepath.Join(paths.DataDir, ".migration-handoff"), nil, 0600); err != nil {
			return fmt.Errorf("record pending migration handoff: %w", err)
		}
		report, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := client.ReportReady(report, migration.Ready{Secret: secret, TargetURL: target})
		cancel()
		if err != nil {
			decision := abort()
			if decision == nil {
				return err
			}
			if !errors.Is(decision, migration.ErrCommitted) {
				status, statusErr := migration.FetchStatus(context.Background(), source, pin, incoming.ID)
				if errors.Is(statusErr, migration.ErrToken) {
					return err
				}
				if statusErr != nil || status.Phase != db.MigrationRetired {
					// A lost reply can hide a committed retirement. Keep the ready
					// target read-only while its worker verifies the decision.
					return nil
				}
			}
		}
		activate, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := app.OpenExisting(activate, p.Database)
		if err == nil {
			activated = migration.Activate(activate, conn, incoming.ID) == nil
			conn.Close()
		}
		return nil
	}
	fmt.Fprintln(out, "[7/8] 启动服务并验证新主控就绪")
	if err := restoreWith(prepare, p, core, stage, true, directory, out, deps); err != nil {
		return err
	}
	retained = true
	if activated {
		fmt.Fprintf(out, "主控迁移完成：%s\n迁移会使原有登录会话失效，请使用原管理员账号密码重新登录，在「备份与迁移」查看节点进度。使用域名时，请将 DNS 改到新机。\n", target)
	} else {
		fmt.Fprintln(out, "新机已就绪，正在核对旧主控的迁移结果。请在两台主控的「备份与迁移」查看状态并保留新机，直至确认交接。")
	}
	return nil
}
