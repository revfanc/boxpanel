package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/firewall"
)

type fakeFiles struct {
	data   map[string]string
	errs   map[string]error
	writes []string
}

func (f *fakeFiles) ReadFile(path string) ([]byte, error) {
	if err := f.errs[path]; err != nil {
		return nil, err
	}
	data, ok := f.data[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(data), nil
}
func (f *fakeFiles) WriteFile(path string, data []byte, mode fs.FileMode) error {
	f.writes = append(f.writes, path)
	if err := f.errs["write:"+path]; err != nil {
		return err
	}
	if mode != 0644 {
		return fmt.Errorf("unexpected mode %v", mode)
	}
	f.data[path] = string(data)
	return nil
}
func (f *fakeFiles) Remove(path string) error {
	if err := f.errs["remove:"+path]; err != nil {
		return err
	}
	delete(f.data, path)
	return nil
}

type fakeRunner struct {
	paths map[string]bool
	calls []string
	run   func(context.Context, string, ...string) ([]byte, error)
}

func (r *fakeRunner) LookPath(name string) (string, error) {
	if r.paths[name] {
		return name, nil
	}
	return "", fs.ErrNotExist
}
func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if r.run == nil {
		return nil, fmt.Errorf("unexpected command: %s", name)
	}
	return r.run(ctx, name, args...)
}

type fakeFirewall struct{ err error }

type fakeSingbox struct {
	status Singbox
	err    error
}

func (f fakeSingbox) Status(context.Context) (Singbox, error) { return f.status, f.err }

func (f fakeFirewall) Status(context.Context) (firewall.Status, error) {
	return firewall.Status{Type: "ufw", Active: true}, f.err
}

func fixture() (*Service, *fakeFiles, *fakeRunner) {
	f := &fakeFiles{data: map[string]string{
		"/etc/os-release": "ID=debian\nVERSION_ID=\"12\"\n",
		"/proc/stat":      "cpu 100 0 50 800 50 0 0 0 70 0\n",
		"/proc/meminfo":   "MemTotal: 1000 kB\nMemAvailable: 600 kB\n",
		"/proc/uptime":    "123.45 200.00\n",
		"/proc/net/dev":   networkFixture(100, 200),
		congestionPath:    "cubic\n", availablePath: "reno cubic bbr\n", qdiscPath: "fq_codel\n",
	}, errs: map[string]error{}}
	r := &fakeRunner{paths: map[string]bool{"systemd-detect-virt": true}}
	r.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemd-detect-virt":
			return []byte("kvm\n"), nil
		case "ss":
			return []byte("tcp LISTEN 0 4096 [::]:443 [::]:*\nudp UNCONN 0 0 0.0.0.0:53 0.0.0.0:*\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command %s", name)
		}
	}
	s := New(Options{OS: "linux", Arch: "arm64", Files: f, Runner: r, Firewall: fakeFirewall{}, Singbox: fakeSingbox{},
		DiskUsage: func(string) (Usage, error) { return Usage{Used: 10, Total: 100}, nil },
		Probe: func(context.Context) (PublicIPs, error) {
			return PublicIPs{IPv4: "203.0.113.1", IPv6: "2001:db8::1"}, nil
		},
		Now: func() time.Time { return time.Unix(1000, 0) },
	})
	return s, f, r
}
func networkFixture(rx, tx uint64) string {
	return fmt.Sprintf("Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\nlo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\neth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", rx, tx)
}

func TestParsers(t *testing.T) {
	for _, tt := range []struct{ input, want string }{{"ID=ubuntu\nVERSION_ID=\"24.04\"", "ubuntu 24.04"}, {"ID='alpine'", "alpine"}, {"ID=\"broken", ""}, {"PRETTY_NAME=Linux", ""}} {
		got, err := parseOSRelease([]byte(tt.input))
		if (err != nil) != (tt.want == "") || got != tt.want {
			t.Errorf("os: %q %v", got, err)
		}
	}
	cpu, err := parseCPU([]byte("cpu 1 2 3 4 5 6 7 8 100 200\ncpu0 0 0 0 0"))
	if err != nil || cpu.total != 36 || cpu.idle != 9 {
		t.Fatalf("cpu=%+v %v", cpu, err)
	}
	mem, err := parseMemory([]byte("MemTotal: 100 kB\nMemAvailable: 40 kB\n"))
	if err != nil || mem != (Usage{Used: 60 * 1024, Total: 100 * 1024}) {
		t.Fatalf("memory=%+v %v", mem, err)
	}
	net, err := parseNetwork([]byte(networkFixture(42, 84)))
	if err != nil || len(net) != 1 || net["eth0"] != (netCounters{42, 84}) {
		t.Fatalf("network=%+v %v", net, err)
	}
	for _, input := range []string{"", "cpu 0 1", "cpu 1 -1 3 4", "cpu 18446744073709551615 1 0 0"} {
		if _, err := parseCPU([]byte(input)); err == nil {
			t.Errorf("accepted CPU %q", input)
		}
	}
	for _, input := range []string{"", "MemTotal: 100 kB", "MemTotal: 100 MB\nMemAvailable: 1 kB", "MemTotal: 100 kB\nMemAvailable: 101 kB", "MemTotal: 18446744073709551615 kB\nMemAvailable: 1 kB"} {
		if _, err := parseMemory([]byte(input)); err == nil {
			t.Errorf("accepted memory %q", input)
		}
	}
	for _, input := range []string{"", "Inter-| Receive\neth0: bad", strings.Replace(networkFixture(1, 2), "eth0: 1", "eth0: -1", 1)} {
		if _, err := parseNetwork([]byte(input)); err == nil {
			t.Errorf("accepted network %q", input)
		}
	}
	for _, input := range []string{"", "-1 0", "NaN 0", "+Inf 0", "9223372036854775808 0"} {
		if _, err := parseUptime([]byte(input)); err == nil {
			t.Errorf("accepted uptime %q", input)
		}
	}
	if n, err := parseUptime([]byte("15.99 42.00")); err != nil || n != 15 {
		t.Errorf("uptime=%d %v", n, err)
	}
}

func TestSocketParsing(t *testing.T) {
	data := "tcp LISTEN 0 128 127.0.0.1:443 0.0.0.0:*\ntcp LISTEN 0 128 [::]:443 [::]:*\nudp UNCONN 0 0 *:8443 *:*\ntcp LISTEN 0 5 [fe80::1%eth0]:2053 [::]:*\n"
	ports, err := parseSockets([]byte(data))
	if err != nil || len(ports) != 3 || !ports[portKey{443, "tcp"}] || ports[portKey{443, "udp"}] || !ports[portKey{8443, "udp"}] {
		t.Fatalf("ports=%v %v", ports, err)
	}
	for _, input := range []string{"tcp LISTEN", "tcp LISTEN 0 0 *:https *:*", "tcp LISTEN 0 0 *:65536 *:*", "tcp LISTEN 0 0 missing *:*", "sctp LISTEN 0 0 *:443 *:*"} {
		if _, err := parseSockets([]byte(input)); err == nil {
			t.Errorf("accepted socket %q", input)
		}
	}
	if ports, err := parseSockets(nil); err != nil || len(ports) != 0 {
		t.Fatal("empty socket list is valid")
	}
}

func TestStatusSampling(t *testing.T) {
	s, f, _ := fixture()
	first, err := s.Status(context.Background())
	if err != nil || !first.Supported || first.CPU != 0 || first.Net != (Rates{}) || first.Mem.Used != 400*1024 || first.Uptime != 123 {
		t.Fatalf("first=%+v %v", first, err)
	}
	s.opts.Now = func() time.Time { return time.Unix(1002, 0) }
	f.data["/proc/stat"] = "cpu 150 0 50 850 50 0 0 0\n"
	f.data["/proc/net/dev"] = networkFixture(300, 600)
	second, err := s.Status(context.Background())
	if err != nil || second.CPU != 50 || second.Net != (Rates{RX: 100, TX: 200}) {
		t.Fatalf("second=%+v %v", second, err)
	}
	// Reset counters and a newly added interface must not produce underflow or spikes.
	s.opts.Now = func() time.Time { return time.Unix(1004, 0) }
	f.data["/proc/stat"] = "cpu 0 0 0 0\n"
	f.data["/proc/net/dev"] = networkFixture(1, 2) + "eth1: 999999 0 0 0 0 0 0 0 999999 0 0 0 0 0 0 0\n"
	reset, err := s.Status(context.Background())
	if err != nil || reset.CPU != 0 || reset.Net != (Rates{}) {
		t.Fatalf("reset=%+v %v", reset, err)
	}
	for _, path := range []string{"/proc/stat", "/proc/meminfo", "/proc/uptime", "/proc/net/dev"} {
		f.errs[path] = fs.ErrPermission
		if _, err := s.Status(context.Background()); err == nil {
			t.Errorf("ignored failure %s", path)
		}
		delete(f.errs, path)
	}
	s.opts.DiskUsage = func(string) (Usage, error) { return Usage{}, fs.ErrPermission }
	if _, err := s.Status(context.Background()); err == nil {
		t.Fatal("ignored disk failure")
	}
}

func TestStatusConcurrent(t *testing.T) {
	s, _, _ := fixture()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Status(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestCheckItemsAndFailures(t *testing.T) {
	s, f, r := fixture()
	check, err := s.Check(context.Background())
	if err != nil || check.OS != "debian 12" || check.Arch != "arm64" || check.Virt != "kvm" || !check.BBR.Available || check.BBR.Enabled || check.IPv4 == "" {
		t.Fatalf("check=%+v %v", check, err)
	}
	if *check.Ports[0].Free || !*check.Ports[1].Free {
		t.Fatal("incorrect occupancy")
	}
	items := map[string]Item{}
	for _, item := range check.Items {
		items[item.ID] = item
		if item.Hint == "" {
			t.Fatal("missing next step")
		}
	}
	if items["bbr"].Action != "enable_bbr" || items["port_443_tcp"].Level != "warn" {
		t.Fatalf("items=%v", items)
	}
	f.data["/etc/os-release"] = "broken"
	f.errs[congestionPath] = fs.ErrPermission
	s.opts.Arch = "386"
	s.opts.Probe = func(context.Context) (PublicIPs, error) { return PublicIPs{}, errors.New("offline") }
	s.opts.Firewall = fakeFirewall{err: fs.ErrPermission}
	r.run = func(context.Context, string, ...string) ([]byte, error) { return nil, fs.ErrPermission }
	check, err = s.Check(context.Background())
	if err != nil || check.Ports[0].Free != nil || check.Firewall.Type != "unknown" {
		t.Fatalf("partial check=%+v %v", check, err)
	}
	items = map[string]Item{}
	for _, item := range check.Items {
		items[item.ID] = item
	}
	for _, id := range []string{"arch", "public_ip", "firewall", "ports"} {
		if items[id].Level != "error" {
			t.Errorf("missing error %s", id)
		}
	}
}

func TestVirtualizationFallback(t *testing.T) {
	for _, tt := range []struct{ cgroup, dmi, want string }{{"/docker/123", "", "docker"}, {"/", "KVM", "kvm"}, {"/", "VMware Virtual Platform", "vmware"}, {"/", "Virtual Machine", "microsoft"}, {"/", "physical machine", "unknown"}} {
		s, f, r := fixture()
		r.paths = nil
		f.data["/proc/1/cgroup"] = tt.cgroup
		f.data["/sys/class/dmi/id/product_name"] = tt.dmi
		got, err := s.virtualization(context.Background())
		if got != tt.want || (err != nil) != (tt.want == "unknown") {
			t.Errorf("virt=%s %v", got, err)
		}
	}
}

func TestUnsupportedDoesNoHostIO(t *testing.T) {
	for _, opts := range []Options{{OS: "darwin"}, {OS: "linux", Dev: true}} {
		s, _, r := fixture()
		s.opts.OS = opts.OS
		s.opts.Dev = opts.Dev
		s.opts.Files = nil
		s.opts.DiskUsage = nil
		s.opts.Probe = nil
		s.opts.Firewall = nil
		if status, err := s.Status(context.Background()); err != nil || status.Supported {
			t.Fatal("unsupported status")
		}
		if check, err := s.Check(context.Background()); err != nil || check.Items[0].Level != "warn" || check.Ports[0].Free != nil {
			t.Fatal("unsupported check")
		}
		if _, err := s.PublicIP(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		if _, err := s.EnableBBR(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.calls, []string(nil)) {
			t.Fatal("executed host command")
		}
	}
}

func TestSingboxStatusIntegration(t *testing.T) {
	for _, tc := range []struct {
		status Singbox
		err    error
		level  string
	}{
		{Singbox{}, nil, "warn"}, {Singbox{Installed: true, Version: "1.14.4"}, nil, "warn"},
		{Singbox{Installed: true, Running: true, Version: "1.14.4", Uptime: 12}, nil, "ok"},
		{Singbox{Installed: true}, errors.New("private command output"), "error"},
	} {
		s, _, _ := fixture()
		s.opts.Singbox = fakeSingbox{status: tc.status, err: tc.err}
		check, err := s.Check(context.Background())
		if err != nil || check.Singbox != tc.status {
			t.Fatal(check, err)
		}
		last := check.Items[len(check.Items)-1]
		if last.ID != "singbox" || last.Level != tc.level || strings.Contains(last.Hint, "private command output") {
			t.Fatal(last)
		}
		status, err := s.Status(context.Background())
		if (err != nil) != (tc.err != nil) || status.Singbox != tc.status {
			t.Fatal(status, err)
		}
	}
}
