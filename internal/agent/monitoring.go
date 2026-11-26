package agent

import "github.com/revfanc/boxpanel/internal/sysinfo"

// Metrics keeps the frequent heartbeat compact. Usage pairs are [used, total]
// in decimal MB, and network is [receive, transmit] in bytes per second.
type Metrics struct {
	CPU         float64      `json:"cpu"`
	Mem         []uint64     `json:"mem"`
	Disk        []uint64     `json:"disk"`
	Net         []float64    `json:"net"`
	Uptime      int64        `json:"uptime"`
	Unavailable bool         `json:"unavailable,omitempty"`
	Core        *CoreMetrics `json:"core,omitempty"`
}

type CoreMetrics struct {
	Version string `json:"version,omitempty"`
	Uptime  int64  `json:"up,omitempty"`
}

func NewMetrics(status sysinfo.Monitoring) *Metrics {
	out := &Metrics{Unavailable: status.Metrics == nil || !status.Metrics.Supported,
		Mem: []uint64{0, 0}, Disk: []uint64{0, 0}, Net: []float64{0, 0}}
	if sample := status.Metrics; sample != nil {
		out.CPU, out.Uptime = sample.CPU, sample.Uptime
		out.Mem[0], out.Mem[1] = sample.Mem.Used, sample.Mem.Total
		out.Disk[0], out.Disk[1] = sample.Disk.Used, sample.Disk.Total
		out.Net[0], out.Net[1] = sample.Net.RX, sample.Net.TX
	}
	if core := status.Core; core != nil {
		out.Core = &CoreMetrics{Version: core.Version, Uptime: core.Uptime}
	}
	return out
}

func (m *Metrics) Valid() bool {
	return m == nil || len(m.Mem) == 2 && len(m.Disk) == 2 && len(m.Net) == 2 &&
		m.CPU >= 0 && m.CPU <= 100 && m.Mem[0] <= m.Mem[1] && m.Disk[0] <= m.Disk[1] &&
		m.Uptime >= 0 && m.Net[0] >= 0 && m.Net[1] >= 0 &&
		(m.Core == nil || m.Core.Uptime >= 0 && (m.Core.Version == "" || ValidMetadata(m.Core.Version, 128)))
}

func (m *Metrics) monitoring(coreActive bool) sysinfo.Monitoring {
	out := sysinfo.Monitoring{Metrics: &sysinfo.Metrics{
		Supported: !m.Unavailable, CPU: m.CPU, Uptime: m.Uptime,
		Mem:  sysinfo.UsageMB{Used: m.Mem[0], Total: m.Mem[1]},
		Disk: sysinfo.UsageMB{Used: m.Disk[0], Total: m.Disk[1]},
		Net:  sysinfo.Rates{RX: m.Net[0], TX: m.Net[1]},
	}}
	if core := m.Core; core != nil {
		out.Core = &sysinfo.Singbox{Installed: core.Version != "" || coreActive, Running: coreActive, Version: core.Version, Uptime: core.Uptime}
	}
	return out
}
