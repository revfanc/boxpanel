package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
)

type upgradeRunner struct{ calls [][]string }

func (r *upgradeRunner) LookPath(name string) (string, error) { return name, nil }
func (r *upgradeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, nil
}

func TestAutoUpgraderFollowsNewerMasterRelease(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	runner := &upgradeRunner{}
	now := time.Unix(1_800_000_000, 0)
	var fetched []string
	fetchErr := error(nil)
	u := &autoUpgrader{conn: conn, version: "v0.3.1", runner: runner, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		scriptDir: t.TempDir(), now: func() time.Time { return now },
		executable: func() (string, error) { return installedBinary, nil },
		fetch: func(_ context.Context, url string) ([]byte, error) {
			fetched = append(fetched, url)
			return []byte("#!/usr/bin/env bash\necho ok\n"), fetchErr
		}}

	// Disabled, same, older or development versions never start an upgrade.
	for _, reply := range []agent.HeartbeatReply{
		{Version: "v0.4.0"},
		{Version: "v0.3.1", AutoUpgrade: true},
		{Version: "v0.3.0", AutoUpgrade: true},
		{Version: "dev", AutoUpgrade: true},
	} {
		u.Handle(ctx, reply)
	}
	if len(runner.calls) != 0 || len(fetched) != 0 {
		t.Fatalf("unexpected upgrade: %v %v", runner.calls, fetched)
	}

	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.4.0", AutoUpgrade: true})
	if !slices.Equal(fetched, []string{"https://raw.githubusercontent.com/revfanc/boxpanel/v0.4.0/scripts/install.sh"}) || len(runner.calls) != 1 {
		t.Fatalf("fetched %v, calls %v", fetched, runner.calls)
	}
	script := filepath.Join(u.scriptDir, "boxpanel-upgrade-v0.4.0.sh")
	want := []string{"systemd-run", "--quiet", "--collect", "--unit=boxpanel-upgrade-v0.4.0", "--property=Type=exec", "--",
		"/bin/bash", script, "update", "--version", "v0.4.0", "--non-interactive"}
	if !slices.Equal(runner.calls[0], want) {
		t.Fatalf("command = %v", runner.calls[0])
	}
	if info, err := os.Stat(script); err != nil || info.Size() == 0 {
		t.Fatalf("installer not saved: %v", err)
	}

	// A failed or slow upgrade is not restarted on every heartbeat or restart.
	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.4.0", AutoUpgrade: true})
	restarted := *u
	restarted.Handle(ctx, agent.HeartbeatReply{Version: "v0.4.0", AutoUpgrade: true})
	if len(runner.calls) != 1 {
		t.Fatalf("retried too early: %v", runner.calls)
	}
	now = now.Add(upgradeRetry)
	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.4.0", AutoUpgrade: true})
	if len(runner.calls) != 2 {
		t.Fatal("did not retry after the back-off")
	}
	// A newer target is tried immediately.
	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.4.1", AutoUpgrade: true})
	if len(runner.calls) != 3 {
		t.Fatal("did not follow a newer master release")
	}

	// Unexpected content and non-installer layouts are refused.
	fetchErr = errors.New("offline")
	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.5.0", AutoUpgrade: true})
	u.executable = func() (string, error) { return "/opt/boxpanel", nil }
	u.Handle(ctx, agent.HeartbeatReply{Version: "v0.6.0", AutoUpgrade: true})
	if len(runner.calls) != 3 {
		t.Fatalf("unsafe upgrade started: %v", runner.calls)
	}
}

type workerNotRunning struct{}

func (workerNotRunning) Error() string { return "unit inactive" }
func (workerNotRunning) ExitCode() int { return 3 }

type panelUpgradeRunner struct {
	running  bool
	launches int
}

func (*panelUpgradeRunner) LookPath(name string) (string, error) { return name, nil }
func (r *panelUpgradeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && !r.running {
		return nil, workerNotRunning{}
	}
	if name == "systemd-run" {
		r.running = true
		r.launches++
	}
	return nil, nil
}

func TestPanelUpgrade(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	runner := &panelUpgradeRunner{}
	u := newAutoUpgrader(conn, "v0.6.0", slog.Default())
	u.runner, u.scriptDir = runner, t.TempDir()
	u.fetch = func(ctx context.Context, url string) ([]byte, error) {
		if url != installScriptURL("v0.6.1") {
			t.Fatal(url)
		}
		return []byte("#!/usr/bin/env bash\n"), nil
	}
	latest := "v0.6.0"
	p := &panelUpgrader{worker: u, latest: func(context.Context) (string, error) { return latest, nil }}
	for _, tag := range []string{"v0.6.1-rc.1", "invalid"} {
		latest = tag
		if _, err := p.Start(ctx); err == nil {
			t.Fatalf("accepted %s", tag)
		}
	}
	for _, tag := range []string{"v0.6.0", "v0.5.9"} {
		latest = tag
		if s, err := p.Start(ctx); err != nil || s.State != "current" || runner.launches != 0 {
			t.Fatal(s, err)
		}
	}
	latest = "v0.6.1"
	for range 2 {
		if s, err := p.Start(ctx); err != nil || s.State != "running" || runner.launches != 1 {
			t.Fatal(s, err)
		}
	}
	// A newly started panel must still see the independent worker and rollback.
	restarted := &panelUpgrader{worker: u, latest: p.latest}
	runner.running = false
	if s, err := restarted.Status(ctx); err != nil || s.State != "failed" || s.Target != latest {
		t.Fatal(s, err)
	}
	if s, err := restarted.Start(ctx); err != nil || s.State != "running" || runner.launches != 2 {
		t.Fatal(s, err)
	}
	u.version = latest
	if s, err := restarted.Status(ctx); err != nil || s.State != "running" {
		t.Fatal(s, err)
	}
	runner.running = false
	if s, err := restarted.Status(ctx); err != nil || s.State != "succeeded" || s.Current != latest {
		t.Fatal(s, err)
	}
}
