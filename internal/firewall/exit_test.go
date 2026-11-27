package firewall

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/exitlink"
)

const activeExitTable = `{"nftables":[
  {"table":{"family":"inet","name":"boxpanel_exit"}},
  {"chain":{"family":"inet","table":"boxpanel_exit","name":"guard","type":"filter","hook":"input","policy":"accept"}},
  {"rule":{"family":"inet","table":"boxpanel_exit","chain":"guard","expr":[{"drop":null}]}},
  {"chain":{"family":"inet","table":"boxpanel_exit","name":"flow_out"}},
  {"chain":{"family":"inet","table":"boxpanel_exit","name":"flow_in"}},
  {"counter":{"family":"inet","table":"boxpanel_exit","name":"forward_up","bytes":21}},
  {"counter":{"family":"inet","table":"boxpanel_exit","name":"forward_down","bytes":50}}
]}`

func TestExitFirewallStagingAndCleanup(t *testing.T) {
	old := Plan{ExitPorts: []ExitPort{{20000, "8.8.8.8"}, {20001, "1.0.0.1"}}}
	next := Plan{ExitPorts: []ExitPort{{20000, "9.9.9.9"}, {20002, "2001:4860:4860::8888"}}}
	staged := MergeExitPorts(old.ExitPorts, next.ExitPorts)
	if !slices.Equal(staged, []ExitPort{next.ExitPorts[0], next.ExitPorts[1], old.ExitPorts[1]}) {
		t.Fatal("staging missed old ports or used a stale source")
	}
	batch, err := exitBatch([]byte(activeExitTable), staged, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"meta l4proto { tcp, udp } th dport 20000 ip saddr != 9.9.9.9 drop",
		"meta l4proto { tcp, udp } th dport 20000 meta nfproto ipv6 drop",
		"meta l4proto { tcp, udp } th dport 20002 ip6 saddr != 2001:4860:4860::8888 drop",
		"meta l4proto { tcp, udp } th dport 20002 meta nfproto ipv4 drop",
		"meta l4proto { tcp, udp } th dport 20001 ip saddr != 1.0.0.1 drop",
	} {
		if !strings.Contains(batch, line) {
			t.Fatal("missing restriction", line)
		}
	}
	if strings.Contains(batch, "8.8.8.8") || strings.Contains(batch, "forward_up") {
		t.Fatal("staging opened the old source or changed counters")
	}
	m, _ := fake(t, []string{"nft"}, cmd(activeExitTable, nil, "nft", "-j", "list", "ruleset"), cmd("", nil, "nft", batch))
	if err := m.PrepareExit(t.Context(), old, next); err != nil {
		t.Fatal(err)
	}
	final, err := exitBatch([]byte(activeExitTable), next.ExitPorts, true)
	if err != nil || strings.Contains(final, "20001") || strings.Contains(final, "add counter") || strings.Contains(final, "reset") ||
		!strings.Contains(final, "meta mark 0xb7000000 ct mark set meta mark counter name forward_up") {
		t.Fatal("finalize missed filters or reset accounting", final, err)
	}
	deleted, err := exitBatch([]byte(activeExitTable), nil, true)
	if err != nil || len(strings.Split(deleted, "\n")) != 3 || strings.Contains(deleted, "counter") || strings.Contains(deleted, "table") {
		t.Fatal("disable should remove all hooks and keep only passive counters", deleted, err)
	}
	var rules nftRuleset
	if err := json.Unmarshal([]byte(activeExitTable), &rules); err != nil || len(rules.inputs()) != 0 {
		t.Fatal("exit guard was exposed as a host firewall for port openings", err)
	}
}

func TestExitFirewallReadsOnlyExitCountersAndRejectsPortOverlap(t *testing.T) {
	m, _ := fake(t, []string{"nft"}, cmd(activeExitTable, nil, "nft", "-j", "list", "counters", "inet"))
	totals, found, err := ReadExitCounters(t.Context(), m.runner)
	if err != nil || !found || totals != (exitlink.Totals{Up: 21, Down: 50}) {
		t.Fatal(totals, found, err)
	}
	plans := []Plan{
		{ExitPorts: []ExitPort{{Port: 20000, Source: "0.0.0.0/0"}}},
		{ExitPorts: []ExitPort{{20000, "8.8.8.8"}, {20000, "9.9.9.9"}}},
		{ExitPorts: []ExitPort{{20000, "8.8.8.8"}}, Ports: []Port{{20000, "udp"}}},
		{ExitPorts: []ExitPort{{20000, "8.8.8.8"}}, Ports: []Port{{19999, "udp"}}, Hops: []Hop{{19990, 20004, 19999}}},
	}
	for _, plan := range plans {
		if err := plan.Validate(); err == nil {
			t.Fatalf("accepted unsafe exit plan %+v", plan)
		}
	}
}
