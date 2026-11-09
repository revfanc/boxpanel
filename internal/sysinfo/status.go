package sysinfo

import (
	"context"
	"fmt"
	"time"
)

type Usage struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}
type Rates struct {
	RX float64 `json:"rx_rate"`
	TX float64 `json:"tx_rate"`
}
type Status struct {
	Supported bool    `json:"supported"`
	CPU       float64 `json:"cpu"`
	Mem       Usage   `json:"mem"`
	Disk      Usage   `json:"disk"`
	Uptime    int64   `json:"uptime"`
	Net       Rates   `json:"net"`
	Singbox   Singbox `json:"singbox"`
}
type sample struct {
	cpu cpuCounters
	net map[string]netCounters
	at  time.Time
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	var result Status
	if !s.supported() {
		return result, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("collect status: %w", err)
	}
	read := func(path string) ([]byte, error) {
		data, err := s.opts.Files.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return data, nil
	}
	data, err := read("/proc/stat")
	if err != nil {
		return result, err
	}
	cpu, err := parseCPU(data)
	if err != nil {
		return result, err
	}
	data, err = read("/proc/meminfo")
	if err != nil {
		return result, err
	}
	result.Mem, err = parseMemory(data)
	if err != nil {
		return result, err
	}
	data, err = read("/proc/uptime")
	if err != nil {
		return result, err
	}
	result.Uptime, err = parseUptime(data)
	if err != nil {
		return result, err
	}
	data, err = read("/proc/net/dev")
	if err != nil {
		return result, err
	}
	network, err := parseNetwork(data)
	if err != nil {
		return result, err
	}
	result.Disk, err = s.opts.DiskUsage(s.opts.DiskPath)
	if err != nil {
		return result, fmt.Errorf("read filesystem usage: %w", err)
	}
	if result.Disk.Total == 0 || result.Disk.Used > result.Disk.Total {
		return result, fmt.Errorf("invalid filesystem usage")
	}
	result.Singbox, err = s.opts.Singbox.Status(ctx)
	if err != nil {
		return result, fmt.Errorf("read sing-box status: %w", err)
	}
	current := sample{cpu: cpu, net: network, at: s.opts.Now()}
	if prev := s.previous; prev != nil && current.at.After(prev.at) {
		if cpu.total > prev.cpu.total && cpu.idle >= prev.cpu.idle {
			total, idle := cpu.total-prev.cpu.total, cpu.idle-prev.cpu.idle
			if idle <= total {
				result.CPU = 100 * float64(total-idle) / float64(total)
			}
		}
		seconds := current.at.Sub(prev.at).Seconds()
		for name, counters := range network {
			old, ok := prev.net[name]
			if !ok {
				continue
			}
			if counters.rx >= old.rx {
				result.Net.RX += float64(counters.rx-old.rx) / seconds
			}
			if counters.tx >= old.tx {
				result.Net.TX += float64(counters.tx-old.tx) / seconds
			}
		}
	}
	s.previous = &current
	result.Supported = true
	return result, nil
}
