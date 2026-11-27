package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"

	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/fwmark"
	"github.com/revfanc/boxpanel/internal/host"
)

const (
	exitTable = "boxpanel_exit"
	exitGuard = "guard"
	exitUp    = "forward_up"
	exitDown  = "forward_down"
)

type ExitPort struct {
	Port   int    `json:"port"`
	Source string `json:"source"`
}

func (p ExitPort) valid() bool {
	ip, err := netip.ParseAddr(p.Source)
	return err == nil && ip.Zone() == "" && !ip.Is4In6() && !ip.IsUnspecified() && !ip.IsMulticast() &&
		p.Port > 0 && p.Port <= 65535
}

// MergeExitPorts keeps both sets reserved through a restart or recovery. The
// new source takes precedence when the same port changes owners.
func MergeExitPorts(previous, desired []ExitPort) []ExitPort {
	out := slices.Clone(desired)
	for _, old := range previous {
		if !slices.ContainsFunc(out, func(p ExitPort) bool { return p.Port == old.Port }) {
			out = append(out, old)
		}
	}
	return out
}

// PrepareExit installs the source guard before a new sing-box listener starts.
// Both transport protocols are protected, even on a host with default ACCEPT.
func (m *Manager) PrepareExit(ctx context.Context, previous, desired Plan) error {
	ports := MergeExitPorts(previous.ExitPorts, desired.ExitPorts)
	if m.noop || len(ports) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncExit(ctx, ports, false)
}

func (m *Manager) syncExit(ctx context.Context, ports []ExitPort, finalize bool) error {
	for _, port := range ports {
		if !port.valid() {
			return fmt.Errorf("invalid restricted exit port; repair its port and source address")
		}
	}
	if _, err := m.runner.LookPath("nft"); err != nil {
		return fmt.Errorf("install nftables to protect the exit port and retry")
	}
	raw, err := m.runner.Run(ctx, "nft", "-j", "list", "ruleset")
	if err != nil {
		return fmt.Errorf("read exit firewall state: %w", err)
	}
	batch, err := exitBatch(raw, ports, finalize)
	if err != nil || batch == "" {
		return err
	}
	if _, err := m.runner.Run(ctx, "nft", batch); err != nil {
		return fmt.Errorf("apply exit firewall rules: check nftables permissions and retry: %w", err)
	}
	return nil
}

func exitBatch(raw []byte, ports []ExitPort, finalize bool) (string, error) {
	var state trafficRuleset
	if json.Unmarshal(raw, &state) != nil || state.NFTables == nil {
		return "", fmt.Errorf("invalid nftables exit rule inventory")
	}
	exists := false
	chains := map[string]bool{}
	counters := map[string]bool{}
	for _, obj := range state.NFTables {
		if t := obj.Table; t != nil && t.Family == "inet" && t.Name == exitTable {
			exists = true
		}
		if c := obj.Chain; c != nil && c.Family == "inet" && c.Table == exitTable {
			chains[c.Name] = true
		}
		if c := obj.Counter; c != nil && c.Family == "inet" && c.Table == exitTable {
			counters[c.Name] = true
		}
	}
	var commands []string
	if len(ports) == 0 {
		for _, chain := range []string{exitGuard, "flow_out", "flow_in"} {
			if chains[chain] {
				commands = append(commands, "delete chain inet "+exitTable+" "+chain)
			}
		}
		// Keep the counters available for the next accounting sample. There
		// are no filtering or accounting hooks once the last port is removed.
		return strings.Join(commands, "\n"), nil
	}
	if !exists {
		commands = append(commands, "add table inet "+exitTable)
	}
	replaceChain := func(name, hook string, priority int) {
		if chains[name] {
			commands = append(commands, fmt.Sprintf("flush chain inet %s %s", exitTable, name))
		} else {
			commands = append(commands, fmt.Sprintf("add chain inet %s %s { type filter hook %s priority %d; policy accept; }", exitTable, name, hook, priority))
		}
	}
	replaceChain(exitGuard, "input", -10)
	for _, p := range ports {
		family, other := "ip", "ipv6"
		if netip.MustParseAddr(p.Source).Is6() {
			family, other = "ip6", "ipv4"
		}
		match := fmt.Sprintf("add rule inet %s %s meta l4proto { tcp, udp } th dport %d ", exitTable, exitGuard, p.Port)
		commands = append(commands, match+family+" saddr != "+p.Source+" drop", match+"meta nfproto "+other+" drop")
	}
	if finalize {
		for _, name := range []string{exitUp, exitDown} {
			if !counters[name] {
				commands = append(commands, "add counter inet "+exitTable+" "+name)
			}
		}
		replaceChain("flow_out", "output", -150)
		replaceChain("flow_in", "input", -150)
		commands = append(commands,
			fmt.Sprintf("add rule inet %s flow_out meta l4proto { tcp, udp } meta mark 0x%08x ct mark set meta mark counter name %s", exitTable, fwmark.ExitForward, exitUp),
			fmt.Sprintf("add rule inet %s flow_in meta l4proto { tcp, udp } ct direction reply ct mark 0x%08x counter name %s", exitTable, fwmark.ExitForward, exitDown))
	}
	return strings.Join(commands, "\n"), nil
}

func ReadExitCounters(ctx context.Context, runner host.Runner) (exitlink.Totals, bool, error) {
	if runner == nil {
		runner = host.Native{}
	}
	raw, err := runner.Run(ctx, "nft", "-j", "list", "counters", "inet")
	if err != nil {
		return exitlink.Totals{}, false, fmt.Errorf("read exit counters: %w", err)
	}
	var state trafficRuleset
	if json.Unmarshal(raw, &state) != nil || state.NFTables == nil {
		return exitlink.Totals{}, false, fmt.Errorf("invalid nftables exit counters")
	}
	values := map[string]int64{}
	for _, obj := range state.NFTables {
		c := obj.Counter
		if c == nil || c.Family != "inet" || c.Table != exitTable || (c.Name != exitUp && c.Name != exitDown) {
			continue
		}
		if _, found := values[c.Name]; found || c.Bytes == nil {
			return exitlink.Totals{}, false, fmt.Errorf("invalid exit counter pair")
		}
		values[c.Name] = int64(min(*c.Bytes, math.MaxInt64))
	}
	if len(values) == 1 {
		return exitlink.Totals{}, false, fmt.Errorf("incomplete exit counter pair")
	}
	return exitlink.Totals{Up: values[exitUp], Down: values[exitDown]}, len(values) == 2, nil
}

// PurgeExit removes any remaining passive counters after uninstall has stopped
// the services and closed the port rules.
func (m *Manager) PurgeExit(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.noop {
		return nil
	}
	if _, err := m.runner.LookPath("nft"); err != nil {
		return nil
	}
	raw, err := m.runner.Run(ctx, "nft", "-j", "list", "ruleset")
	if err != nil {
		return err
	}
	var rules trafficRuleset
	if json.Unmarshal(raw, &rules) != nil || rules.NFTables == nil {
		return fmt.Errorf("invalid nftables exit rule inventory")
	}
	for _, obj := range rules.NFTables {
		if t := obj.Table; t != nil && t.Family == "inet" && t.Name == exitTable {
			_, err := m.runner.Run(ctx, "nft", "delete", "table", "inet", exitTable)
			return err
		}
	}
	return nil
}
