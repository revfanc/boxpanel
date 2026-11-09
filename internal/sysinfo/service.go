// Package sysinfo inspects a Linux server through injectable host operations.
package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/host"
)

var ErrUnsupported = errors.New("system operation requires Linux without --dev")

type Firewall interface {
	Status(context.Context) (firewall.Status, error)
}

type Options struct {
	OS, Arch  string
	Dev       bool
	Files     host.Files
	Runner    host.Runner
	DiskUsage func(string) (Usage, error)
	DiskPath  string
	Now       func() time.Time
	Probe     PublicIPProbe
	Firewall  Firewall
}

type Service struct {
	opts     Options
	mu       sync.Mutex
	bbrMu    sync.Mutex
	previous *sample
}

func New(opts Options) *Service {
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}
	if opts.Files == nil {
		opts.Files = host.Native{}
	}
	if opts.Runner == nil {
		opts.Runner = host.Native{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Probe == nil {
		opts.Probe = DefaultPublicIPs
	}
	if opts.DiskPath == "" {
		opts.DiskPath = "/"
	}
	if opts.DiskUsage == nil {
		opts.DiskUsage = diskUsage
	}
	if opts.Firewall == nil {
		opts.Firewall = firewall.New(firewall.Options{OS: opts.OS, Dev: opts.Dev, Runner: opts.Runner})
	}
	return &Service{opts: opts}
}

func (s *Service) supported() bool { return s.opts.OS == "linux" && !s.opts.Dev }

type Item struct {
	ID     string `json:"id"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Hint   string `json:"hint"`
	Action string `json:"action,omitempty"`
}

type Port struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"`
	Free  *bool  `json:"free"`
}

type Singbox struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	Uptime    int64  `json:"uptime"`
}

type Check struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Virt string `json:"virt"`
	PublicIPs
	BBR      BBR             `json:"bbr"`
	Firewall firewall.Status `json:"firewall"`
	Singbox  Singbox         `json:"singbox"`
	Ports    []Port          `json:"ports"`
	Items    []Item          `json:"items"`
}

func (s *Service) PublicIP(ctx context.Context) (PublicIPs, error) {
	if !s.supported() {
		return PublicIPs{}, ErrUnsupported
	}
	return s.opts.Probe(ctx)
}

func (s *Service) Check(ctx context.Context) (Check, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := Check{OS: s.opts.OS, Arch: s.opts.Arch, Virt: "unknown", Firewall: firewall.Status{Type: "none"},
		Ports: []Port{{Port: 443, Proto: "tcp"}, {Port: 8443, Proto: "udp"}}, Items: []Item{}}
	add := func(id, level, title, hint string) {
		result.Items = append(result.Items, Item{ID: id, Level: level, Title: title, Hint: hint})
	}
	if !s.supported() {
		add("platform", "warn", "当前为开发环境或非 Linux 系统", "请在 Linux VPS 上运行生产模式后执行完整自检；当前不会修改系统配置。")
		return result, nil
	}
	data, err := s.opts.Files.ReadFile("/etc/os-release")
	if err == nil {
		result.OS, err = parseOSRelease(data)
	}
	if err != nil {
		result.OS = "linux"
		add("os", "warn", "无法识别 Linux 发行版", "请检查 /etc/os-release 是否存在且可读。")
	} else {
		add("os", "ok", "已识别 Linux 系统", "可继续检查服务器环境。")
	}
	if result.Arch != "amd64" && result.Arch != "arm64" {
		add("arch", "error", "当前架构不受支持", "请使用 amd64 或 arm64 的 Linux VPS。")
	} else {
		add("arch", "ok", "处理器架构受支持", "可使用对应架构的安装包。")
	}
	result.Virt, err = s.virtualization(ctx)
	if err != nil {
		add("virt", "warn", "无法识别虚拟化环境", "请安装 systemd-detect-virt 或向主机商确认虚拟化类型。")
	} else {
		add("virt", "ok", "已识别虚拟化环境", "容器内如无法修改内核参数，请联系宿主机管理员。")
	}
	result.PublicIPs, err = s.PublicIP(ctx)
	if err != nil {
		add("public_ip", "error", "公网 IP 探测失败", "请检查服务器出站 HTTPS 和 DNS，修复后重试公网 IP 探测。")
	} else {
		add("public_ip", "ok", "已探测到公网 IP", "如使用域名，请确认其解析指向此服务器。")
	}
	result.BBR, err = s.DetectBBR(ctx)
	switch {
	case err != nil:
		add("bbr", "warn", "无法检测 BBR", "请检查 /proc/sys/net 是否可读，并确认主机允许读取内核参数。")
	case result.BBR.Enabled:
		add("bbr", "ok", "已开启 BBR", "可继续配置节点。")
	case result.BBR.Available:
		result.Items = append(result.Items, Item{ID: "bbr", Level: "warn", Title: "未开启 BBR", Hint: "建议开启以提升 TCP 传输性能。", Action: "enable_bbr"})
	default:
		add("bbr", "warn", "当前内核未提供 BBR", "请升级支持 BBR 的内核；容器用户请联系宿主机管理员。")
	}
	result.Firewall, err = s.opts.Firewall.Status(ctx)
	if err != nil {
		result.Firewall.Type = "unknown"
		add("firewall", "error", "无法检测防火墙", "请检查防火墙工具及 root 权限，修复后重新自检。")
	} else if result.Firewall.Active {
		add("firewall", "ok", "已检测到活动防火墙", "配置节点时需放行本机端口，并在云服务商安全组中放行对应端口。")
	} else {
		add("firewall", "warn", "未检测到活动的本机防火墙", "请检查云服务商安全组，仅开放需要的端口；面板不会自动启用防火墙。")
	}
	output, err := s.opts.Runner.Run(ctx, "ss", "-H", "-lntu")
	var occupied map[portKey]bool
	if err == nil {
		occupied, err = parseSockets(output)
	}
	if err != nil {
		add("ports", "error", "无法检查端口占用", "请安装 iproute2 并确认有权限运行 ss，再重新自检。")
	} else {
		for i := range result.Ports {
			p := &result.Ports[i]
			free := !occupied[portKey{p.Port, p.Proto}]
			p.Free = &free
			id := fmt.Sprintf("port_%d_%s", p.Port, p.Proto)
			if free {
				add(id, "ok", fmt.Sprintf("%d/%s 端口可用", p.Port, p.Proto), "创建节点前仍需再次检查，端口可能被其他进程占用。")
			} else {
				add(id, "warn", fmt.Sprintf("%d/%s 端口已被占用", p.Port, p.Proto), "请停止占用此端口的服务，或为新节点选择其他端口。")
			}
		}
	}
	add("singbox", "warn", "尚未接入 sing-box 管理", "请在支持内核管理的面板版本中安装 sing-box 后重试。")
	return result, nil
}

func (s *Service) virtualization(ctx context.Context) (string, error) {
	if _, err := s.opts.Runner.LookPath("systemd-detect-virt"); err == nil {
		output, err := s.opts.Runner.Run(ctx, "systemd-detect-virt")
		value := strings.TrimSpace(string(output))
		if (err == nil || (host.ExitCode(err, 1) && value == "none")) && value != "" && !strings.ContainsAny(value, " \t\r\n") {
			return value, nil
		}
		return "unknown", fmt.Errorf("detect virtualization: unexpected command result")
	}
	// Read-only fallback for minimal images lacking systemd's diagnostic tool.
	data, err := s.opts.Files.ReadFile("/proc/1/cgroup")
	if err == nil {
		for _, name := range []string{"docker", "lxc", "kubepods"} {
			if strings.Contains(string(data), name) {
				return name, nil
			}
		}
	}
	data, err = s.opts.Files.ReadFile("/sys/class/dmi/id/product_name")
	if err == nil {
		value := strings.ToLower(string(data))
		for _, pair := range [][2]string{{"kvm", "kvm"}, {"qemu", "qemu"}, {"vmware", "vmware"}, {"virtualbox", "oracle"}, {"virtual machine", "microsoft"}, {"xen", "xen"}} {
			if strings.Contains(value, pair[0]) {
				return pair[1], nil
			}
		}
	}
	return "unknown", fmt.Errorf("detect virtualization: no recognized evidence")
}
