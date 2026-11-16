package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/fwmark"
	"github.com/revfanc/boxpanel/internal/host"
)

const TrafficTable = "boxpanel_traffic"

type Counter struct {
	UserID           int64
	Upload, Download int64
}

type nftCounter struct {
	Family string  `json:"family"`
	Table  string  `json:"table"`
	Name   string  `json:"name"`
	Bytes  *uint64 `json:"bytes"`
}
type trafficRuleset struct {
	NFTables []struct {
		Table   *struct{ Family, Name string } `json:"table"`
		Chain   *nftObject                     `json:"chain"`
		Counter *nftCounter                    `json:"counter"`
	} `json:"nftables"`
}

func counterName(id int64, up bool) string {
	prefix := "down_u"
	if up {
		prefix = "up_u"
	}
	return prefix + strconv.FormatInt(id, 10)
}
func counterUser(name string) (int64, bool, error) {
	up := strings.HasPrefix(name, "up_u")
	prefix := "down_u"
	if up {
		prefix = "up_u"
	}
	if !strings.HasPrefix(name, prefix) {
		return 0, false, fmt.Errorf("unknown traffic counter; restore managed traffic table and retry apply")
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(name, prefix), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid traffic counter name; retry apply")
	}
	if _, err := fwmark.User(id); err != nil {
		return 0, false, err
	}
	if name != counterName(id, up) {
		return 0, false, fmt.Errorf("noncanonical traffic counter name; retry apply")
	}
	return id, up, nil
}

// trafficBatch rebuilds only rule references, retaining named counter objects.
// nft applies the complete batch atomically. Fixed identifiers and validated
// integers are passed directly to nft, never through a shell.
func trafficBatch(raw []byte, desired []int64) (string, error) {
	var state trafficRuleset
	if json.Unmarshal(raw, &state) != nil || state.NFTables == nil {
		return "", fmt.Errorf("invalid nftables traffic inventory; check nft permissions and retry")
	}
	exists := false
	chains := map[string]bool{}
	counters := map[string]bool{}
	for _, obj := range state.NFTables {
		if t := obj.Table; t != nil && t.Family == "inet" && t.Name == TrafficTable {
			exists = true
		}
		if c := obj.Chain; c != nil && c.Family == "inet" && c.Table == TrafficTable {
			chains[c.Name] = true
		}
		if c := obj.Counter; c != nil && c.Family == "inet" && c.Table == TrafficTable {
			if _, _, err := counterUser(c.Name); err != nil {
				return "", err
			}
			counters[c.Name] = true
		}
	}
	ids := slices.Clone(desired)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		if _, err := fwmark.User(id); err != nil {
			return "", err
		}
	}
	if len(ids) == 0 {
		if exists {
			return "delete table inet " + TrafficTable, nil
		}
		return "", nil
	}
	var commands []string
	if !exists {
		commands = append(commands, "add table inet "+TrafficTable)
	}
	for _, chain := range []string{"output", "input"} {
		if chains[chain] {
			commands = append(commands, "flush chain inet "+TrafficTable+" "+chain)
		} else {
			// mangle (-150) runs after conntrack (-200). Filter, not NAT:
			// every TCP/UDP packet is counted, including the first UDP packet.
			commands = append(commands, fmt.Sprintf("add chain inet %s %s { type filter hook %s priority mangle; policy accept; }", TrafficTable, chain, chain))
		}
	}
	keep := map[string]bool{}
	for _, id := range ids {
		mark, _ := fwmark.User(id)
		for _, up := range []bool{true, false} {
			name := counterName(id, up)
			keep[name] = true
			if !counters[name] {
				commands = append(commands, fmt.Sprintf("add counter inet %s %s", TrafficTable, name))
			}
		}
		commands = append(commands,
			fmt.Sprintf("add rule inet %s output meta l4proto { tcp, udp } meta mark 0x%08x ct mark set meta mark counter name %s", TrafficTable, mark, counterName(id, true)),
			fmt.Sprintf("add rule inet %s input meta l4proto { tcp, udp } ct direction reply ct mark 0x%08x counter name %s", TrafficTable, mark, counterName(id, false)))
	}
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !keep[name] {
			commands = append(commands, "delete counter inet "+TrafficTable+" "+name)
		}
	}
	return strings.Join(commands, "\n"), nil
}

func (m *Manager) syncTraffic(ctx context.Context, desired []int64) error {
	if _, err := m.runner.LookPath("nft"); err != nil {
		return fmt.Errorf("nftables is required; install nftables and retry")
	}
	raw, err := m.runner.Run(ctx, "nft", "-j", "list", "ruleset")
	if err != nil {
		return fmt.Errorf("list traffic inventory: %w", err)
	}
	batch, err := trafficBatch(raw, desired)
	if err != nil || batch == "" {
		return err
	}
	if _, err := m.runner.Run(ctx, "nft", batch); err != nil {
		return fmt.Errorf("apply traffic counters: %w", err)
	}
	return nil
}

// ParseCounters accepts unsigned kernel byte counters, saturating at the DB's
// int64 limit. Incomplete pairs/duplicate names must not advance baselines.
func ParseCounters(raw []byte) ([]Counter, error) {
	var state trafficRuleset
	if json.Unmarshal(raw, &state) != nil || state.NFTables == nil {
		return nil, fmt.Errorf("invalid nft counter response; check nftables and retry apply")
	}
	values := map[int64]Counter{}
	seen := map[string]bool{}
	for _, obj := range state.NFTables {
		c := obj.Counter
		if c == nil || c.Family != "inet" || c.Table != TrafficTable {
			continue
		}
		id, up, err := counterUser(c.Name)
		if err != nil {
			return nil, err
		}
		if seen[c.Name] || c.Bytes == nil {
			return nil, fmt.Errorf("incomplete or duplicate traffic counters; retry apply")
		}
		seen[c.Name] = true
		n := int64(min(*c.Bytes, uint64(math.MaxInt64)))
		v := values[id]
		v.UserID = id
		if up {
			v.Upload = n
		} else {
			v.Download = n
		}
		values[id] = v
	}
	result := make([]Counter, 0, len(values))
	for id, value := range values {
		if !seen[counterName(id, true)] || !seen[counterName(id, false)] {
			return nil, fmt.Errorf("missing traffic counter pair; retry apply")
		}
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b Counter) int { return int(a.UserID - b.UserID) })
	return result, nil
}

func ReadCounters(ctx context.Context, runner host.Runner) ([]Counter, error) {
	if runner == nil {
		runner = host.Native{}
	}
	raw, err := runner.Run(ctx, "nft", "-j", "list", "counters", "table", "inet", TrafficTable)
	if err != nil {
		return nil, fmt.Errorf("read traffic counters: check nftables/NET_ADMIN permissions and retry apply: %w", err)
	}
	return ParseCounters(raw)
}
