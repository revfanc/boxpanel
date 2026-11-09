package firewall

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

type exitError int

func (e exitError) Error() string { return "command failed" }
func (e exitError) ExitCode() int { return int(e) }

type command struct {
	args   []string
	output string
	err    error
}
type runner struct {
	t        *testing.T
	paths    map[string]bool
	commands []command
	calls    [][]string
	lookups  int
}

func (r *runner) LookPath(name string) (string, error) {
	r.lookups++
	if r.paths[name] {
		return name, nil
	}
	return "", fs.ErrNotExist
}
func (r *runner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if len(r.commands) == 0 {
		r.t.Fatalf("unexpected command %v", call)
	}
	want := r.commands[0]
	r.commands = r.commands[1:]
	if !reflect.DeepEqual(want.args, call) {
		r.t.Fatalf("command=%v want=%v", call, want.args)
	}
	return []byte(want.output), want.err
}
func fake(t *testing.T, paths []string, commands ...command) (*Manager, *runner) {
	t.Helper()
	r := &runner{t: t, paths: map[string]bool{}, commands: commands}
	for _, path := range paths {
		r.paths[path] = true
	}
	t.Cleanup(func() {
		if len(r.commands) != 0 {
			t.Errorf("unexecuted commands: %v", r.commands)
		}
	})
	return New(Options{OS: "linux", Runner: r}), r
}
func cmd(output string, err error, args ...string) command { return command{args, output, err} }

const nftInput = `{"nftables":[{"metainfo":{"json_schema_version":1}},{"chain":{"family":"inet","table":"custom","name":"incoming","type":"filter","hook":"input","policy":"drop"}},{"chain":{"family":"ip","table":"nat","name":"prerouting","type":"nat","hook":"prerouting"}}]}`

func TestDetection(t *testing.T) {
	for _, tt := range []struct {
		name     string
		paths    []string
		commands []command
		want     Status
		failure  bool
	}{
		{"none", nil, nil, Status{Type: "none"}, false},
		{"ufw active", []string{"ufw", "nft"}, []command{cmd("Status: active\n", nil, "ufw", "status")}, Status{Type: "ufw", Active: true}, false},
		{"ufw inactive", []string{"ufw"}, []command{cmd("Status: inactive\n", nil, "ufw", "status")}, Status{Type: "ufw"}, false},
		{"firewalld after inactive ufw", []string{"ufw", "firewall-cmd"}, []command{cmd("Status: inactive\n", nil, "ufw", "status"), cmd("running\n", nil, "firewall-cmd", "--state")}, Status{Type: "firewalld", Active: true}, false},
		{"firewalld inactive", []string{"firewall-cmd"}, []command{cmd("not running\n", exitError(252), "firewall-cmd", "--state")}, Status{Type: "firewalld"}, false},
		{"nftables", []string{"nft"}, []command{cmd(nftInput, nil, "nft", "-j", "-a", "list", "ruleset")}, Status{Type: "nftables", Active: true}, false},
		{"empty nftables", []string{"nft"}, []command{cmd(`{"nftables":[]}`, nil, "nft", "-j", "-a", "list", "ruleset")}, Status{Type: "nftables"}, false},
		{"iptables ipv6 policy", []string{"iptables"}, []command{cmd("-P INPUT ACCEPT\n", nil, "iptables", "-w", "5", "-S", "INPUT"), cmd("-P INPUT DROP\n", nil, "ip6tables", "-w", "5", "-S", "INPUT")}, Status{Type: "iptables", Active: true}, false},
		{"iptables empty", []string{"iptables"}, []command{cmd("-P INPUT ACCEPT\n", nil, "iptables", "-w", "5", "-S", "INPUT"), cmd("-P INPUT ACCEPT\n", nil, "ip6tables", "-w", "5", "-S", "INPUT")}, Status{Type: "iptables"}, false},
		{"permission does not fall through", []string{"ufw", "nft"}, []command{cmd("", fs.ErrPermission, "ufw", "status")}, Status{Type: "ufw"}, true},
		{"invalid ufw", []string{"ufw"}, []command{cmd("unexpected", nil, "ufw", "status")}, Status{Type: "ufw"}, true},
		{"invalid firewalld", []string{"firewall-cmd"}, []command{cmd("unexpected", nil, "firewall-cmd", "--state")}, Status{Type: "firewalld"}, true},
		{"invalid nft", []string{"nft"}, []command{cmd(`{}`, nil, "nft", "-j", "-a", "list", "ruleset")}, Status{Type: "nftables"}, true},
		{"malformed nft", []string{"nft"}, []command{cmd(`broken`, nil, "nft", "-j", "-a", "list", "ruleset")}, Status{Type: "nftables"}, true},
		{"invalid iptables", []string{"iptables"}, []command{cmd("not a policy", nil, "iptables", "-w", "5", "-S", "INPUT")}, Status{Type: "iptables"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := fake(t, tt.paths, tt.commands...)
			got, err := m.Status(context.Background())
			if got != tt.want || (err != nil) != tt.failure {
				t.Fatalf("status=%+v %v", got, err)
			}
		})
	}
}

func TestNoopAndValidation(t *testing.T) {
	for _, opts := range []Options{{OS: "darwin"}, {OS: "linux", Dev: true}} {
		r := &runner{t: t}
		opts.Runner = r
		m := New(opts)
		if status, err := m.Status(context.Background()); err != nil || status != (Status{Type: "none"}) {
			t.Fatal("not noop")
		}
		if err := m.Open(context.Background(), 443, "tcp"); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(context.Background(), 8443, "udp"); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 0 || r.lookups != 0 {
			t.Fatal("noop touched host")
		}
	}
	m, r := fake(t, nil)
	for _, tt := range []struct {
		port  int
		proto string
	}{{0, "tcp"}, {65536, "tcp"}, {443, "TCP"}, {443, "tcp; reboot"}} {
		if err := m.Open(context.Background(), tt.port, tt.proto); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if r.lookups != 0 {
		t.Fatal("validation reached host")
	}
}

func TestUFWPorts(t *testing.T) {
	m, _ := fake(t, []string{"ufw"},
		cmd("Status: active", nil, "ufw", "status"),
		cmd("", nil, "ufw", "allow", "443/tcp", "comment", "boxpanel:tcp:443"),
		cmd("Status: active", nil, "ufw", "status"),
		cmd("", nil, "ufw", "--force", "delete", "allow", "443/tcp", "comment", "boxpanel:tcp:443"),
	)
	if err := m.Open(context.Background(), 443, "tcp"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background(), 443, "tcp"); err != nil {
		t.Fatal(err)
	}
}

func TestFirewalldRuntimePermanentAndIdempotency(t *testing.T) {
	m, _ := fake(t, []string{"firewall-cmd"},
		cmd("running", nil, "firewall-cmd", "--state"),
		cmd("no", exitError(1), "firewall-cmd", "--query-port=8443/udp"),
		cmd("success", nil, "firewall-cmd", "--add-port=8443/udp"),
		cmd("yes", nil, "firewall-cmd", "--permanent", "--query-port=8443/udp"),
		cmd("running", nil, "firewall-cmd", "--state"),
		cmd("yes", nil, "firewall-cmd", "--query-port=8443/udp"),
		cmd("success", nil, "firewall-cmd", "--remove-port=8443/udp"),
		cmd("yes", nil, "firewall-cmd", "--permanent", "--query-port=8443/udp"),
		cmd("success", nil, "firewall-cmd", "--permanent", "--remove-port=8443/udp"),
		cmd("running", nil, "firewall-cmd", "--state"),
		cmd("no", exitError(1), "firewall-cmd", "--query-port=8443/udp"),
		cmd("no", exitError(1), "firewall-cmd", "--permanent", "--query-port=8443/udp"),
	)
	if err := m.Open(context.Background(), 8443, "udp"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.Close(context.Background(), 8443, "udp"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNFTOnlyManagedRules(t *testing.T) {
	withRules := strings.TrimSuffix(nftInput, "]}") + `,{"rule":{"family":"inet","table":"custom","chain":"incoming","comment":"boxpanel:tcp:443","handle":42}},{"rule":{"family":"inet","table":"custom","chain":"incoming","comment":"administrator","handle":43}}]}`
	m, _ := fake(t, []string{"nft"},
		cmd(nftInput, nil, "nft", "-j", "-a", "list", "ruleset"), cmd(nftInput, nil, "nft", "-j", "-a", "list", "ruleset"),
		cmd("", nil, "nft", "insert", "rule", "inet", `"custom"`, `"incoming"`, "tcp", "dport", "443", "accept", "comment", `"boxpanel:tcp:443"`),
		cmd(withRules, nil, "nft", "-j", "-a", "list", "ruleset"), cmd(withRules, nil, "nft", "-j", "-a", "list", "ruleset"),
		cmd(withRules, nil, "nft", "-j", "-a", "list", "ruleset"), cmd(withRules, nil, "nft", "-j", "-a", "list", "ruleset"),
		cmd("", nil, "nft", "delete", "rule", "inet", `"custom"`, `"incoming"`, "handle", "42"),
	)
	for range 2 {
		if err := m.Open(context.Background(), 443, "tcp"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(context.Background(), 443, "tcp"); err != nil {
		t.Fatal(err)
	}
}

func TestIPTablesDualStack(t *testing.T) {
	commands := []command{}
	for phase, open := range []bool{true, true, false, false} {
		for _, name := range []string{"iptables", "ip6tables"} {
			commands = append(commands, cmd("-P INPUT DROP\n", nil, name, "-w", "5", "-S", "INPUT"))
		}
		for _, name := range []string{"iptables", "ip6tables"} {
			rule := []string{"INPUT", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", "boxpanel:tcp:443", "-j", "ACCEPT"}
			var err error
			exists := phase == 1 || phase == 2
			if !exists {
				err = exitError(1)
			}
			commands = append(commands, cmd("", err, append([]string{name, "-w", "5", "-C"}, rule...)...))
			if exists == open {
				continue
			}
			action := "-D"
			if open {
				action = "-I"
			}
			commands = append(commands, cmd("", nil, append([]string{name, "-w", "5", action}, rule...)...))
		}
	}
	m, _ := fake(t, []string{"iptables"}, commands...)
	for range 2 {
		if err := m.Open(context.Background(), 443, "tcp"); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := m.Close(context.Background(), 443, "tcp"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMutationErrorsAreNotMissingRules(t *testing.T) {
	m, _ := fake(t, []string{"firewall-cmd"}, cmd("running", nil, "firewall-cmd", "--state"), cmd("denied", exitError(29), "firewall-cmd", "--query-port=443/tcp"))
	if err := m.Open(context.Background(), 443, "tcp"); err == nil {
		t.Fatal("permission denied treated as missing port")
	}
	m, _ = fake(t, []string{"ufw"}, cmd("Status: active", nil, "ufw", "status"), cmd("denied", fs.ErrPermission, "ufw", "allow", "443/tcp", "comment", "boxpanel:tcp:443"))
	if err := m.Open(context.Background(), 443, "tcp"); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("lost mutation error")
	}
}
