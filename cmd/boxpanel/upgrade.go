package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
)

const (
	// installedBinary is where install.sh places boxpanel; update only
	// knows how to replace that copy.
	installedBinary  = "/usr/local/bin/boxpanel"
	upgradeRetry     = 6 * time.Hour
	maxInstallScript = 1 << 20
)

// autoUpgrader starts `install.sh update --version <master>` in a transient
// systemd unit when the master enables auto-upgrade and runs a newer release.
// The unit outlives the boxpanel.service restart that update performs, and
// update itself verifies the release checksum and restores the old binary on failure.
type autoUpgrader struct {
	conn       *sql.DB
	version    string
	runner     host.Runner
	logger     *slog.Logger
	scriptDir  string
	now        func() time.Time
	executable func() (string, error)
	fetch      func(context.Context, string) ([]byte, error)
	warned     bool
}

func newAutoUpgrader(conn *sql.DB, version string, logger *slog.Logger) *autoUpgrader {
	return &autoUpgrader{conn: conn, version: version, runner: host.Native{}, logger: logger, scriptDir: "/run",
		now: time.Now, executable: os.Executable, fetch: fetchInstallScript}
}

func (u *autoUpgrader) Handle(ctx context.Context, reply agent.HeartbeatReply) {
	if !reply.AutoUpgrade || !agent.NewerRelease(reply.Version, u.version) {
		return
	}
	if err := u.start(ctx, reply.Version); err != nil && !u.warned {
		u.warned = true
		u.logger.Warn("自动升级未能启动；节点继续使用当前版本。请在主控服务器卡片复制升级命令手动执行。", "target", reply.Version, "reason", err.Error())
	}
}

func (u *autoUpgrader) checkInstalled() error {
	exe, err := u.executable()
	if err != nil {
		return fmt.Errorf("locate boxpanel binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if exe != installedBinary {
		return fmt.Errorf("binary is not at %s; this node was not installed by install.sh", installedBinary)
	}
	return nil
}

func (u *autoUpgrader) start(ctx context.Context, tag string) error {
	if err := u.checkInstalled(); err != nil {
		return err
	}
	last, err := db.GetSetting(ctx, u.conn, db.AgentUpgradeAttempt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := u.now()
	if last.Tag == tag && now.Sub(time.Unix(last.At, 0)) < upgradeRetry {
		return nil // already started or failed recently; retry later
	}
	// Record before starting so a crash or failed update cannot loop on restart.
	if err := db.SetSetting(ctx, u.conn, db.AgentUpgradeAttempt, db.UpgradeAttempt{Tag: tag, At: now.Unix()}); err != nil {
		return err
	}
	if err := u.queue(ctx, tag); err != nil {
		return err
	}
	u.warned = false
	u.logger.Info("已开始自动升级到主控版本。", "target", tag)
	return nil
}

func (u *autoUpgrader) queue(ctx context.Context, tag string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	script, err := u.fetch(fetchCtx, installScriptURL(tag))
	cancel()
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(script, []byte("#!/usr/bin/env bash\n")) {
		return fmt.Errorf("downloaded installer is not the boxpanel script")
	}
	path := filepath.Join(u.scriptDir, "boxpanel-upgrade-"+tag+".sh")
	if err := (host.Native{}).WriteFile(path, script, 0700); err != nil {
		return fmt.Errorf("save installer: %w", err)
	}
	// The tag matches agent.ReleaseTag, so it holds no shell or systemd specials.
	if _, err := u.runner.Run(ctx, "systemd-run", "--quiet", "--collect", "--unit=boxpanel-upgrade-"+tag,
		"--property=Type=exec", "--", "/bin/bash", path, "update", "--version", tag, "--non-interactive"); err != nil {
		return fmt.Errorf("queue upgrade worker: %w", err)
	}
	return nil
}

func fetchInstallScript(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download installer: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInstallScript+1))
	if err != nil || len(body) > maxInstallScript {
		return nil, fmt.Errorf("download installer: read failed or too large")
	}
	return body, nil
}
