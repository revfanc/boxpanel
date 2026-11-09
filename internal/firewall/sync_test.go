package firewall

import (
	"context"
	"io/fs"
	"strings"
	"testing"
)

func TestHoppingNFTAtomicReplaceAndCleanup(t *testing.T) {
	desired := Plan{Ports: []Port{{8443, "udp"}}, Hops: []Hop{{20000, 30000, 8443}}}
	batch := "add table inet boxpanel_hopping\nadd chain inet boxpanel_hopping prerouting { type nat hook prerouting priority dstnat; policy accept; }\nadd rule inet boxpanel_hopping prerouting fib daddr type local udp dport 20000-30000 redirect to :8443 comment \"boxpanel:hy2\""
	existing := `{"nftables":[{"table":{"family":"inet","name":"administrator"}},{"table":{"family":"inet","name":"boxpanel_hopping"}}]}`
	m, _ := fake(t, []string{"nft"},
		cmd(`{"nftables":[]}`, nil, "nft", "-j", "list", "ruleset"), cmd("", nil, "nft", batch),
		cmd(existing, nil, "nft", "-j", "list", "ruleset"), cmd("", nil, "nft", "delete table inet boxpanel_hopping\n"+batch),
		cmd(existing, nil, "nft", "-j", "list", "ruleset"), cmd("", nil, "nft", "delete table inet boxpanel_hopping"),
		cmd(`{"nftables":[]}`, nil, "nft", "-j", "list", "ruleset"),
	)
	for _, plans := range [][2]Plan{{{}, desired}, {desired, desired}, {desired, {}}, {{}, {}}} {
		if err := m.syncHops(context.Background(), plans[0], plans[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHoppingIPTablesDualStackReplayAndCleanup(t *testing.T) {
	var commands []command
	for phase := 0; phase < 3; phase++ {
		for _, name := range []string{"iptables", "ip6tables"} {
			prefix := []string{name, "-w", "5", "-t", "nat"}
			add := func(output string, err error, args ...string) {
				commands = append(commands, cmd(output, err, append(append([]string{}, prefix...), args...)...))
			}
			listing := "-P PREROUTING ACCEPT\n-N OTHER\n"
			if phase > 0 {
				listing += "-N BOXPANEL_HY2\n"
			}
			add(listing, nil, "-S")
			if phase == 0 {
				add("", nil, "-N", hoppingChain)
			}
			jump := []string{"PREROUTING", "-m", "addrtype", "--dst-type", "LOCAL", "-m", "comment", "--comment", "boxpanel:hy2", "-j", hoppingChain}
			var checkErr error
			if phase == 0 {
				checkErr = exitError(1)
			}
			add("", checkErr, append([]string{"-C"}, jump...)...)
			if phase > 0 {
				add("", nil, append([]string{"-D"}, jump...)...)
			}
			add("", nil, "-F", hoppingChain)
			if phase == 2 {
				add("", nil, "-X", hoppingChain)
				continue
			}
			add("", nil, "-A", hoppingChain, "-p", "udp", "--dport", "20000:30000", "-j", "REDIRECT", "--to-ports", "8443")
			add("", nil, append([]string{"-I"}, jump...)...)
		}
	}
	m, _ := fake(t, []string{"iptables", "ip6tables"}, commands...)
	desired := Plan{Ports: []Port{{8443, "udp"}}, Hops: []Hop{{20000, 30000, 8443}}}
	for _, plans := range [][2]Plan{{{}, desired}, {desired, desired}, {desired, {}}} {
		if err := m.syncHops(context.Background(), plans[0], plans[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHoppingFailureDoesNotFallBack(t *testing.T) {
	m, r := fake(t, []string{"nft", "iptables", "ip6tables"}, cmd("permission denied", fs.ErrPermission, "nft", "-j", "list", "ruleset"))
	p := Plan{Ports: []Port{{8443, "udp"}}, Hops: []Hop{{20000, 30000, 8443}}}
	if err := m.Sync(context.Background(), Plan{}, p); err == nil {
		t.Fatal("ignored failure")
	}
	if r.lookups != 1 {
		t.Fatal("fell back after nft failure")
	}
	m, _ = fake(t, []string{"iptables"})
	if err := m.Sync(context.Background(), Plan{}, p); err == nil {
		t.Fatal("missing IPv6 support accepted")
	}
}

func TestSyncServicePortsAndHoppingValidation(t *testing.T) {
	previous := Plan{Ports: []Port{{443, "tcp"}}}
	desired := Plan{Ports: []Port{{8443, "udp"}}}
	m, _ := fake(t, []string{"ufw"},
		cmd("Status: active", nil, "ufw", "status"), cmd("", nil, "ufw", "allow", "8443/udp", "comment", "boxpanel:udp:8443"),
		cmd("Status: active", nil, "ufw", "status"), cmd("", nil, "ufw", "--force", "delete", "allow", "443/tcp", "comment", "boxpanel:tcp:443"),
	)
	if err := m.Sync(context.Background(), previous, desired); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Plan{
		{Ports: []Port{{0, "udp"}}}, {Ports: []Port{{8443, "udp;reboot"}}},
		{Ports: desired.Ports, Hops: []Hop{{30000, 20000, 8443}}},
		{Ports: desired.Ports, Hops: []Hop{{0, 30000, 8443}}},
		{Ports: desired.Ports, Hops: []Hop{{20000, 65536, 8443}}},
		{Ports: desired.Ports, Hops: []Hop{{20000, 30000, 8444}}},
		{Ports: desired.Ports, Hops: []Hop{{20000, 30000, 8443}, {30000, 40000, 8443}}},
		{Ports: []Port{{8443, "udp"}, {22222, "udp"}}, Hops: []Hop{{20000, 30000, 8443}}},
	} {
		if err := m.Sync(context.Background(), Plan{}, p); err == nil {
			t.Fatalf("accepted invalid plan %+v", p)
		}
	}
	for _, opts := range []Options{{OS: "darwin"}, {OS: "linux", Dev: true}} {
		opts.Runner = &runner{t: t}
		if err := New(opts).Sync(context.Background(), previous, desired); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIPTablesHoppingPermissionFailure(t *testing.T) {
	m, _ := fake(t, []string{"iptables", "ip6tables"}, cmd("denied", fs.ErrPermission, "iptables", "-w", "5", "-t", "nat", "-S"))
	p := Plan{Ports: []Port{{8443, "udp"}}, Hops: []Hop{{20000, 30000, 8443}}}
	if err := m.Sync(context.Background(), Plan{}, p); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatal("missing recoverable error")
	}
}
