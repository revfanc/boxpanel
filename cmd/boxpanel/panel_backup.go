package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/host"
)

type panelBackups struct {
	service *backup.Service
	opts    serveOptions
	queued  atomic.Bool
	runner  host.Runner
}

func (p *panelBackups) Create(ctx context.Context, pass string) ([]byte, error) {
	if p == nil {
		return nil, backup.ErrInvalid
	}
	if p.queued.Load() {
		return nil, fmt.Errorf("restore queued")
	}
	return p.service.Create(ctx, pass)
}
func (p *panelBackups) Status() (backup.Status, error) {
	if p == nil {
		return backup.Status{}, fmt.Errorf("backup unavailable")
	}
	return p.service.Status()
}
func (p *panelBackups) Restore(ctx context.Context, data []byte, pass string) error {
	if p == nil || p.opts.dev || runtime.GOOS != "linux" && p.runner == nil {
		return fmt.Errorf("restore requires Linux/systemd")
	}
	if !p.queued.CompareAndSwap(false, true) {
		return fmt.Errorf("restore already queued")
	}
	ok := false
	defer func() {
		if !ok {
			p.queued.Store(false)
		}
	}()
	a, err := backup.Decode(data, pass, true)
	if err != nil {
		return err
	}
	stage, err := backup.Stage(ctx, a, filepath.Dir(p.opts.paths.DataDir))
	if err != nil {
		return err
	}
	defer func() {
		if !ok {
			os.RemoveAll(stage)
		}
	}()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--quiet", "--collect", "--unit=boxpanel-restore-" + filepath.Base(stage), "--on-active=3s", "--property=Type=exec", "--", exe, "restore-staged", "--data-dir", p.opts.paths.DataDir, "--db-path", p.opts.paths.Database, "--cert-file", p.opts.paths.CertFile, "--key-file", p.opts.paths.KeyFile, "--singbox-path", p.opts.core.binary, "--singbox-config", p.opts.core.config, stage}
	// systemd performs its own variable expansion even without a shell. Older
	// supported systems lack --expand-environment=no; escape dollars explicitly.
	command := false
	for i, arg := range args {
		if command {
			args[i] = strings.ReplaceAll(arg, "$", "$$")
		}
		if arg == "--" {
			command = true
		}
	}
	runner := p.runner
	if runner == nil {
		runner = host.Native{}
	}
	if _, err = runner.Run(ctx, "systemd-run", args...); err != nil {
		return fmt.Errorf("queue restore worker; check systemd-run and retry")
	}
	ok = true
	return nil
}
