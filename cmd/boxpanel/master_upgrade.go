package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/httpapi"
)

type panelUpgrader struct {
	mu     sync.Mutex
	worker *autoUpgrader
	latest func(context.Context) (string, error)
}

func (p *panelUpgrader) Status(ctx context.Context) (httpapi.UpgradeStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status(ctx)
}

func (p *panelUpgrader) status(ctx context.Context) (httpapi.UpgradeStatus, error) {
	u := p.worker
	s := httpapi.UpgradeStatus{State: "idle", Current: u.version}
	tag, err := db.GetSetting(ctx, u.conn, db.MasterUpgradeTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !agent.ReleaseTag(tag) {
		return s, fmt.Errorf("invalid saved upgrade target")
	}
	s.Target = tag
	_, err = u.runner.Run(ctx, "systemctl", "is-active", "--quiet", "boxpanel-upgrade-"+tag+".service")
	switch {
	case err == nil:
		s.State = "running"
	case !host.ExitCode(err, 3) && !host.ExitCode(err, 4):
		return s, fmt.Errorf("read upgrade worker state: %w", err)
	case tag == u.version || agent.NewerRelease(u.version, tag):
		s.State = "succeeded"
	default:
		s.State = "failed"
	}
	return s, nil
}

func (p *panelUpgrader) Start(ctx context.Context) (httpapi.UpgradeStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.status(ctx)
	if err != nil || s.State == "running" {
		return s, err
	}
	tag, err := p.latest(ctx)
	if err != nil {
		return s, err
	}
	if !agent.ReleaseTag(tag) || strings.Contains(tag, "-") {
		return s, fmt.Errorf("expected an official stable release")
	}
	if !agent.NewerRelease(tag, s.Current) {
		s.State, s.Target = "current", tag
		return s, nil
	}
	// Persist before launch: the worker can replace and restart this process.
	if err := db.SetSetting(ctx, p.worker.conn, db.MasterUpgradeTarget, tag); err != nil {
		return s, err
	}
	if err := p.worker.queue(ctx, tag); err != nil {
		return s, err
	}
	s.State, s.Target = "running", tag
	return s, nil
}

func latestStableRelease(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/revfanc/boxpanel/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("read latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read latest release: HTTP %d", resp.StatusCode)
	}
	var release struct {
		Tag               string `json:"tag_name"`
		Draft, Prerelease bool
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return "", fmt.Errorf("latest release is not stable")
	}
	return release.Tag, nil
}
