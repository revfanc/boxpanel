// Package firewall manages host input ports without enabling a firewall or
// replacing its policy. Rule synchronization at startup belongs to T2.3.
package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/revfanc/boxpanel/internal/host"
)

type Status struct {
	Type   string `json:"type"`
	Active bool   `json:"active"`
}

type Options struct {
	OS     string
	Dev    bool
	Runner host.Runner
}

type Manager struct {
	runner host.Runner
	noop   bool
	mu     sync.Mutex
}

func New(opts Options) *Manager {
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Runner == nil {
		opts.Runner = host.Native{}
	}
	return &Manager{runner: opts.Runner, noop: opts.Dev || opts.OS != "linux"}
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.detect(ctx)
}

func (m *Manager) detect(ctx context.Context) (Status, error) {
	if m.noop {
		return Status{Type: "none"}, nil
	}
	fallback := Status{Type: "none"}
	for _, candidate := range []struct{ name, kind string }{{"ufw", "ufw"}, {"firewall-cmd", "firewalld"}, {"nft", "nftables"}, {"iptables", "iptables"}} {
		if _, err := m.runner.LookPath(candidate.name); err != nil {
			continue
		}
		var active bool
		var err error
		switch candidate.kind {
		case "ufw":
			var output []byte
			output, err = m.runner.Run(ctx, "ufw", "status")
			if err == nil {
				active, err = parseUFW(output)
			}
		case "firewalld":
			var output []byte
			output, err = m.runner.Run(ctx, "firewall-cmd", "--state")
			state := strings.TrimSpace(string(output))
			if state == "not running" && (host.ExitCode(err, 252) || host.ExitCode(err, 1)) {
				err = nil
			} else if err == nil && state != "running" {
				err = fmt.Errorf("unexpected firewalld state")
			}
			active = state == "running"
		case "nftables":
			var rules nftRuleset
			rules, err = m.nftRules(ctx)
			active = len(rules.inputs()) > 0
		case "iptables":
			for _, command := range []string{"iptables", "ip6tables"} {
				var output []byte
				output, err = m.runner.Run(ctx, command, "-w", "5", "-S", "INPUT")
				if err != nil {
					break
				}
				var enabled bool
				enabled, err = parseIPTables(output)
				active = active || enabled
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			return Status{Type: candidate.kind}, fmt.Errorf("detect %s (check privileges and firewall configuration): %w", candidate.kind, err)
		}
		status := Status{Type: candidate.kind, Active: active}
		if active {
			return status, nil
		}
		if fallback.Type == "none" {
			fallback = status
		}
	}
	return fallback, nil
}

func parseUFW(output []byte) (bool, error) {
	line := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0]
	switch strings.TrimSpace(line) {
	case "Status: active":
		return true, nil
	case "Status: inactive":
		return false, nil
	default:
		return false, fmt.Errorf("invalid ufw status")
	}
}

func parseIPTables(output []byte) (bool, error) {
	active, policy := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		switch {
		case line == "-P INPUT ACCEPT":
			policy = true
		case line == "-P INPUT DROP":
			active, policy = true, true
		case strings.HasPrefix(line, "-A INPUT "):
			active = true
		default:
			return false, fmt.Errorf("invalid iptables INPUT output")
		}
	}
	if !policy {
		return false, fmt.Errorf("missing iptables INPUT policy")
	}
	return active, nil
}

func (m *Manager) Open(ctx context.Context, port int, proto string) error {
	return m.change(ctx, port, proto, true)
}

func (m *Manager) Close(ctx context.Context, port int, proto string) error {
	return m.change(ctx, port, proto, false)
}

func (m *Manager) change(ctx context.Context, port int, proto string, open bool) error {
	if port < 1 || port > 65535 || (proto != "tcp" && proto != "udp") {
		return fmt.Errorf("firewall port: use a port from 1 to 65535 and protocol tcp or udp")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status, err := m.detect(ctx)
	if err != nil {
		return err
	}
	if !status.Active {
		return nil
	}
	value := strconv.Itoa(port)
	comment := "boxpanel:" + proto + ":" + value
	switch status.Type {
	case "ufw":
		args := []string{"allow", value + "/" + proto, "comment", comment}
		if !open {
			args = append([]string{"--force", "delete"}, args...)
		}
		_, err = m.runner.Run(ctx, "ufw", args...)
	case "firewalld":
		// Update runtime and permanent configuration separately; never reload
		// (which would discard unrelated runtime rules). Use the default zone.
		for _, prefix := range [][]string{nil, {"--permanent"}} {
			_, queryErr := m.runner.Run(ctx, "firewall-cmd", append(prefix, "--query-port="+value+"/"+proto)...)
			if queryErr != nil && !host.ExitCode(queryErr, 1) {
				err = queryErr
				break
			}
			if (queryErr == nil) == open {
				continue
			}
			action := "--add-port="
			if !open {
				action = "--remove-port="
			}
			_, err = m.runner.Run(ctx, "firewall-cmd", append(prefix, action+value+"/"+proto)...)
			if err != nil {
				break
			}
		}
	case "nftables":
		err = m.changeNFT(ctx, value, proto, comment, open)
	case "iptables":
		for _, command := range []string{"iptables", "ip6tables"} {
			rule := []string{"INPUT", "-p", proto, "--dport", value, "-m", "comment", "--comment", comment, "-j", "ACCEPT"}
			_, queryErr := m.runner.Run(ctx, command, append([]string{"-w", "5", "-C"}, rule...)...)
			if queryErr != nil && !host.ExitCode(queryErr, 1) {
				err = queryErr
				break
			}
			if (queryErr == nil) == open {
				continue
			}
			action := "-I"
			if !open {
				action = "-D"
			}
			_, err = m.runner.Run(ctx, command, append([]string{"-w", "5", action}, rule...)...)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("update %s port %s/%s (check privileges, then retry to reconcile partial changes): %w", status.Type, value, proto, err)
	}
	return nil
}

type nftObject struct {
	Family  string `json:"family"`
	Table   string `json:"table"`
	Name    string `json:"name"`
	Chain   string `json:"chain"`
	Type    string `json:"type"`
	Hook    string `json:"hook"`
	Comment string `json:"comment"`
	Handle  uint64 `json:"handle"`
}

type nftRuleset struct {
	NFTables []struct {
		Chain *nftObject `json:"chain"`
		Rule  *nftObject `json:"rule"`
	} `json:"nftables"`
}

func (r nftRuleset) inputs() []nftObject {
	chains := []nftObject{}
	for _, obj := range r.NFTables {
		c := obj.Chain
		if c != nil && c.Hook == "input" && c.Type == "filter" && (c.Family == "inet" || c.Family == "ip" || c.Family == "ip6") {
			chains = append(chains, *c)
		}
	}
	return chains
}

func (m *Manager) nftRules(ctx context.Context) (nftRuleset, error) {
	var rules nftRuleset
	output, err := m.runner.Run(ctx, "nft", "-j", "-a", "list", "ruleset")
	if err != nil {
		return rules, err
	}
	if err := json.Unmarshal(output, &rules); err != nil {
		return rules, fmt.Errorf("parse nftables ruleset: %w", err)
	}
	if rules.NFTables == nil {
		return rules, fmt.Errorf("missing nftables ruleset")
	}
	return rules, nil
}

func (m *Manager) changeNFT(ctx context.Context, port, proto, comment string, open bool) error {
	rules, err := m.nftRules(ctx)
	if err != nil {
		return err
	}
	// An accept in a separate base chain cannot override a later chain's drop.
	// Insert at the head of each existing IP input filter chain instead.
	for _, chain := range rules.inputs() {
		found := false
		for _, obj := range rules.NFTables {
			r := obj.Rule
			if r == nil || r.Family != chain.Family || r.Table != chain.Table || r.Chain != chain.Name || r.Comment != comment {
				continue
			}
			found = true
			if !open {
				if r.Handle == 0 {
					return fmt.Errorf("missing nftables rule handle")
				}
				if _, err := m.runner.Run(ctx, "nft", "delete", "rule", r.Family, strconv.Quote(r.Table), strconv.Quote(r.Chain), "handle", strconv.FormatUint(r.Handle, 10)); err != nil {
					return err
				}
			}
		}
		if open && !found {
			if _, err := m.runner.Run(ctx, "nft", "insert", "rule", chain.Family, strconv.Quote(chain.Table), strconv.Quote(chain.Name), proto, "dport", port, "accept", "comment", strconv.Quote(comment)); err != nil {
				return err
			}
		}
	}
	return nil
}
