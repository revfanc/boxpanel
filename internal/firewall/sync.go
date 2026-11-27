package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/fwmark"
	"github.com/revfanc/boxpanel/internal/host"
)

// Plan contains service ports, UDP redirects and traffic user IDs, never credentials.
// Previous is the last successful plan (plus any interrupted attempt).
type Plan struct {
	Ports        []Port     `json:"ports"`
	Hops         []Hop      `json:"hops"`
	TrafficUsers []int64    `json:"traffic_users,omitempty"`
	ExitPorts    []ExitPort `json:"exit_ports,omitempty"`
}
type Port struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"`
}
type Hop struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Port  int `json:"port"`
}

func (p Plan) Validate() error {
	seen := map[int64]bool{}
	for _, id := range p.TrafficUsers {
		if _, err := fwmark.User(id); err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("duplicate traffic user; restore the firewall plan before retrying")
		}
		seen[id] = true
	}
	for _, port := range p.Ports {
		if port.Port < 1 || port.Port > 65535 || (port.Proto != "tcp" && port.Proto != "udp") {
			return fmt.Errorf("invalid service port; use 1–65535 and tcp/udp")
		}
	}
	for i, hop := range p.Hops {
		if hop.Start < 1 || hop.End > 65535 || hop.Start > hop.End || hop.Port < 1 || hop.Port > 65535 {
			return fmt.Errorf("invalid UDP hopping range; use ordered ports within 1–65535")
		}
		if !slices.Contains(p.Ports, Port{hop.Port, "udp"}) {
			return fmt.Errorf("hopping target must be an enabled UDP service port")
		}
		for _, other := range p.Hops[:i] {
			if hop.Start <= other.End && other.Start <= hop.End {
				return fmt.Errorf("overlapping UDP hopping ranges; choose separate ranges")
			}
		}
		for _, port := range p.Ports {
			if port.Proto == "udp" && port.Port != hop.Port && port.Port >= hop.Start && port.Port <= hop.End {
				return fmt.Errorf("hopping range overlaps another UDP service; choose a separate range")
			}
		}
	}
	seenExit := map[int]bool{}
	for _, exit := range p.ExitPorts {
		if !exit.valid() || seenExit[exit.Port] {
			return fmt.Errorf("invalid or duplicate restricted exit port; repair exit settings")
		}
		seenExit[exit.Port] = true
		for _, port := range p.Ports {
			if port.Port == exit.Port {
				return fmt.Errorf("exit port overlaps a user service port")
			}
		}
		for _, hop := range p.Hops {
			if exit.Port >= hop.Start && exit.Port <= hop.End {
				return fmt.Errorf("exit port overlaps a UDP hopping range")
			}
		}
	}
	return nil
}

func (p Plan) servicePorts() []Port {
	ports := slices.Clone(p.Ports)
	for _, exit := range p.ExitPorts {
		ports = append(ports, Port{exit.Port, "tcp"}, Port{exit.Port, "udp"})
	}
	return ports
}

func (m *Manager) Sync(ctx context.Context, previous, desired Plan) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	// Previous may be a union of interrupted plans, so validate individual ports
	// but not overlapping historical hopping ranges.
	if err := (Plan{Ports: previous.Ports, TrafficUsers: previous.TrafficUsers}).Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.noop {
		return nil
	}
	exitChanged := len(previous.ExitPorts) > 0 || len(desired.ExitPorts) > 0
	if len(desired.ExitPorts) > 0 {
		if err := m.syncExit(ctx, MergeExitPorts(previous.ExitPorts, desired.ExitPorts), true); err != nil {
			return err
		}
	}
	if err := m.syncHops(ctx, previous, desired); err != nil {
		return fmt.Errorf("synchronize UDP hopping (check nftables/iptables privileges, then retry): %w", err)
	}
	if len(previous.TrafficUsers) > 0 || len(desired.TrafficUsers) > 0 {
		if err := m.syncTraffic(ctx, desired.TrafficUsers); err != nil {
			return fmt.Errorf("synchronize traffic counters (install nftables and check NET_ADMIN privileges, then retry): %w", err)
		}
	}
	services := desired.servicePorts()
	for _, port := range services {
		if err := m.changeLocked(ctx, port.Port, port.Proto, true); err != nil {
			return err
		}
	}
	for _, port := range previous.servicePorts() {
		if !slices.Contains(services, port) {
			if err := m.changeLocked(ctx, port.Port, port.Proto, false); err != nil {
				return err
			}
		}
	}
	if exitChanged && !slices.Equal(MergeExitPorts(previous.ExitPorts, desired.ExitPorts), desired.ExitPorts) {
		return m.syncExit(ctx, desired.ExitPorts, true)
	}
	return nil
}

const hoppingTable = "boxpanel_hopping"
const hoppingChain = "BOXPANEL_HY2"

func (m *Manager) syncHops(ctx context.Context, previous, desired Plan) error {
	if _, err := m.runner.LookPath("nft"); err == nil {
		output, err := m.runner.Run(ctx, "nft", "-j", "list", "ruleset")
		if err != nil {
			return err
		} // A broken/unauthorized nft must not silently fall back.
		var rules struct {
			NFTables []struct {
				Table *struct{ Family, Name string } `json:"table"`
			} `json:"nftables"`
		}
		if json.Unmarshal(output, &rules) != nil || rules.NFTables == nil {
			return fmt.Errorf("invalid nftables ruleset")
		}
		exists := false
		for _, entry := range rules.NFTables {
			if entry.Table != nil && entry.Table.Family == "inet" && entry.Table.Name == hoppingTable {
				exists = true
			}
		}
		var commands []string
		if exists {
			commands = append(commands, "delete table inet "+hoppingTable)
		}
		if len(desired.Hops) > 0 {
			commands = append(commands, "add table inet "+hoppingTable, "add chain inet "+hoppingTable+" prerouting { type nat hook prerouting priority dstnat; policy accept; }")
			for _, hop := range desired.Hops {
				commands = append(commands, fmt.Sprintf("add rule inet %s prerouting fib daddr type local udp dport %d-%d redirect to :%d comment \"boxpanel:hy2\"", hoppingTable, hop.Start, hop.End, hop.Port))
			}
		}
		if len(commands) == 0 {
			return nil
		}
		// One nft transaction, fixed identifiers and validated integers; no shell.
		_, err = m.runner.Run(ctx, "nft", strings.Join(commands, "\n"))
		return err
	}
	if len(previous.Hops) == 0 && len(desired.Hops) == 0 {
		return nil
	}
	for _, command := range []string{"iptables", "ip6tables"} {
		if _, err := m.runner.LookPath(command); err != nil {
			return fmt.Errorf("install nftables or both iptables and ip6tables")
		}
	}
	for _, command := range []string{"iptables", "ip6tables"} {
		if err := m.syncIPTablesHops(ctx, command, desired.Hops); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) syncIPTablesHops(ctx context.Context, command string, hops []Hop) error {
	run := func(args ...string) ([]byte, error) {
		return m.runner.Run(ctx, command, append([]string{"-w", "5", "-t", "nat"}, args...)...)
	}
	output, err := run("-S")
	if err != nil {
		return err
	}
	exists := false
	for _, line := range strings.Split(string(output), "\n") {
		if line == "-N "+hoppingChain {
			exists = true
		}
	}
	jump := []string{"PREROUTING", "-m", "addrtype", "--dst-type", "LOCAL", "-m", "comment", "--comment", "boxpanel:hy2", "-j", hoppingChain}
	if !exists && len(hops) == 0 {
		return nil
	}
	if !exists {
		if _, err = run("-N", hoppingChain); err != nil {
			return err
		}
	}
	// Disconnect before rebuilding. The apply coordinator restores the old plan
	// on any partial failure, including a failure in the second address family.
	_, err = run(append([]string{"-C"}, jump...)...)
	if err != nil && !host.ExitCode(err, 1) {
		return err
	}
	if err == nil {
		if _, err = run(append([]string{"-D"}, jump...)...); err != nil {
			return err
		}
	}
	if _, err = run("-F", hoppingChain); err != nil {
		return err
	}
	if len(hops) == 0 {
		_, err = run("-X", hoppingChain)
		return err
	}
	for _, hop := range hops {
		if _, err = run("-A", hoppingChain, "-p", "udp", "--dport", strconv.Itoa(hop.Start)+":"+strconv.Itoa(hop.End), "-j", "REDIRECT", "--to-ports", strconv.Itoa(hop.Port)); err != nil {
			return err
		}
	}
	_, err = run(append([]string{"-I"}, jump...)...)
	return err
}
