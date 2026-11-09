package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

const (
	congestionPath = "/proc/sys/net/ipv4/tcp_congestion_control"
	availablePath  = "/proc/sys/net/ipv4/tcp_available_congestion_control"
	qdiscPath      = "/proc/sys/net/core/default_qdisc"
	bbrConfigPath  = "/etc/sysctl.d/99-boxpanel-bbr.conf"
	bbrConfig      = "# Managed by boxpanel\nnet.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n"
)

var ErrBBRUnavailable = errors.New("BBR is unavailable in the current kernel")

type BBR struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
}

func (s *Service) DetectBBR(ctx context.Context) (BBR, error) {
	s.bbrMu.Lock()
	defer s.bbrMu.Unlock()
	return s.detectBBR(ctx)
}

func (s *Service) detectBBR(ctx context.Context) (BBR, error) {
	if !s.supported() {
		return BBR{}, ErrUnsupported
	}
	current, err := s.readParameter(congestionPath)
	if err != nil {
		return BBR{}, err
	}
	data, err := s.opts.Files.ReadFile(availablePath)
	if err != nil {
		return BBR{}, fmt.Errorf("read available congestion controls: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return BBR{}, fmt.Errorf("empty congestion controls")
	}
	result := BBR{Enabled: current == "bbr"}
	for _, name := range fields {
		if name == "bbr" {
			result.Available = true
		}
	}
	if result.Enabled {
		result.Available = true
	}
	if !result.Available {
		if _, err := s.opts.Runner.LookPath("modinfo"); err == nil {
			_, err := s.opts.Runner.Run(ctx, "modinfo", "tcp_bbr")
			result.Available = err == nil
		}
	}
	return result, nil
}

func (s *Service) readParameter(path string) (string, error) {
	data, err := s.opts.Files.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read kernel parameter %s: %w", path, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("empty kernel parameter %s", path)
	}
	for _, ch := range value {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return "", fmt.Errorf("invalid kernel parameter %s", path)
		}
	}
	return value, nil
}

func (s *Service) EnableBBR(ctx context.Context) (BBR, error) {
	s.bbrMu.Lock()
	defer s.bbrMu.Unlock()
	state, err := s.detectBBR(ctx)
	if err != nil {
		return BBR{}, err
	}
	if !state.Available {
		return state, ErrBBRUnavailable
	}
	oldCC, err := s.readParameter(congestionPath)
	if err != nil {
		return state, err
	}
	oldQdisc, err := s.readParameter(qdiscPath)
	if err != nil {
		return state, err
	}
	oldFile, err := s.opts.Files.ReadFile(bbrConfigPath)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state, fmt.Errorf("read previous BBR config: %w", err)
	}
	// sysctl may load the module itself, but explicitly load an available module
	// before changing persistent settings so failures leave the config untouched.
	data, err := s.opts.Files.ReadFile(availablePath)
	if err != nil {
		return state, fmt.Errorf("read BBR availability: %w", err)
	}
	loaded := false
	for _, name := range strings.Fields(string(data)) {
		if name == "bbr" {
			loaded = true
		}
	}
	if !loaded {
		if _, err := s.opts.Runner.Run(ctx, "modprobe", "tcp_bbr"); err != nil {
			return state, fmt.Errorf("load BBR module (check host privileges): %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return state, fmt.Errorf("enable BBR: %w", err)
	}
	if err := s.opts.Files.WriteFile(bbrConfigPath, []byte(bbrConfig), 0644); err != nil {
		return state, fmt.Errorf("persist BBR settings: %w", err)
	}
	_, applyErr := s.opts.Runner.Run(ctx, "sysctl", "-p", bbrConfigPath)
	if applyErr == nil {
		cc, ccErr := s.readParameter(congestionPath)
		qdisc, qdiscErr := s.readParameter(qdiscPath)
		if ccErr != nil || qdiscErr != nil || cc != "bbr" || qdisc != "fq" {
			applyErr = errors.Join(fmt.Errorf("BBR settings did not take effect"), ccErr, qdiscErr)
		}
	}
	if applyErr != nil {
		// A canceled HTTP request must not prevent best-effort rollback.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, runtimeErr := s.opts.Runner.Run(rollbackCtx, "sysctl", "-w", "net.core.default_qdisc="+oldQdisc, "net.ipv4.tcp_congestion_control="+oldCC)
		var fileErr error
		if existed {
			fileErr = s.opts.Files.WriteFile(bbrConfigPath, oldFile, 0644)
		} else {
			fileErr = s.opts.Files.Remove(bbrConfigPath)
			if errors.Is(fileErr, fs.ErrNotExist) {
				fileErr = nil
			}
		}
		return state, fmt.Errorf("apply BBR (check privileges and sysctl settings before retrying): %w", errors.Join(applyErr, runtimeErr, fileErr))
	}
	return BBR{Enabled: true, Available: true}, nil
}
