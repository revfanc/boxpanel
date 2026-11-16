package firewall

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/fwmark"
)

var updateTraffic = flag.Bool("update", false, "update firewall golden files")

func TestTrafficRuleGolden(t *testing.T) {
	raw := []byte(`{"nftables":[]}`)
	got, err := trafficBatch(raw, []int64{2, 1})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "traffic.golden")
	if *updateTraffic {
		if err := os.WriteFile(path, []byte(got+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got+"\n" != string(want) {
		t.Fatalf("traffic rules mismatch:\n%s", got)
	}
}
func inventory(ids ...int64) string {
	objects := []string{`{"table":{"family":"inet","name":"boxpanel_traffic"}}`,
		`{"chain":{"family":"inet","table":"boxpanel_traffic","name":"output"}}`,
		`{"chain":{"family":"inet","table":"boxpanel_traffic","name":"input"}}`}
	for _, id := range ids {
		for _, up := range []bool{true, false} {
			objects = append(objects, fmt.Sprintf(`{"counter":{"family":"inet","table":"boxpanel_traffic","name":%q,"bytes":12345}}`, counterName(id, up)))
		}
	}
	return `{"nftables":[` + strings.Join(objects, ",") + `]}`
}
func TestTrafficSyncPreservesCountersAndRemovesOnlyDeparted(t *testing.T) {
	batch, err := trafficBatch([]byte(inventory(1, 2)), []int64{2, 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"delete table", "reset", "add counter inet boxpanel_traffic up_u2", "delete counter inet boxpanel_traffic up_u2", "flush table", "flush ruleset"} {
		if strings.Contains(batch, forbidden) {
			t.Fatal("reset surviving counters:", batch)
		}
	}
	for _, required := range []string{"flush chain inet boxpanel_traffic output", "flush chain inet boxpanel_traffic input", "delete counter inet boxpanel_traffic up_u1", "delete counter inet boxpanel_traffic down_u1", "add counter inet boxpanel_traffic up_u3"} {
		if !strings.Contains(batch, required) {
			t.Fatal("missing sync:", required)
		}
	}
	m, _ := fake(t, []string{"nft"}, cmd(inventory(1, 2), nil, "nft", "-j", "list", "ruleset"), cmd("", nil, "nft", batch))
	if err := m.syncTraffic(context.Background(), []int64{2, 3}); err != nil {
		t.Fatal(err)
	}
	// Replay, including startup, replaces rule references but not objects.
	batch, err = trafficBatch([]byte(inventory(1, 2)), []int64{1, 2})
	if err != nil || strings.Contains(batch, "counter inet") || strings.Contains(batch, "delete") {
		t.Fatal(batch, err)
	}
	// Cleanup of an empty desired plan is confined to our own table.
	batch, err = trafficBatch([]byte(inventory(1, 2)), nil)
	if err != nil || batch != "delete table inet boxpanel_traffic" {
		t.Fatal(batch, err)
	}
}
func TestTrafficCounterParsingAndBounds(t *testing.T) {
	raw := inventory(1, 2)
	values, err := ParseCounters([]byte(raw))
	if err != nil || len(values) != 2 || values[0] != (Counter{1, 12345, 12345}) {
		t.Fatal(values, err)
	}
	values, err = ParseCounters([]byte(strings.ReplaceAll(inventory(1), "12345", "18446744073709551615")))
	if err != nil || values[0].Upload != math.MaxInt64 {
		t.Fatal(values, err)
	}
	for _, raw := range []string{`{}`, `{"nftables":null}`, `broken`, strings.Replace(inventory(1), `"up_u1"`, `"up_u01"`, 1), strings.Replace(inventory(1), `"down_u1"`, `"up_u1"`, 1), strings.Replace(inventory(1), `"bytes":12345`, `"bytes":-1`, 1), strings.Replace(inventory(1), `,"bytes":12345`, ``, 1), strings.Replace(inventory(1), `"down_u1"`, `"down_u2"`, 1)} {
		if _, err := ParseCounters([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, id := range []int64{0, -1, fwmark.MaxUserID + 1, math.MaxInt64} {
		if _, err := trafficBatch([]byte(`{"nftables":[]}`), []int64{id}); err == nil {
			t.Fatal("accepted mark overflow", id)
		}
		if err := (Plan{TrafficUsers: []int64{id}}).Validate(); err == nil {
			t.Fatal("accepted invalid plan", id)
		}
	}
	mark, err := fwmark.User(fwmark.MaxUserID)
	if err != nil || mark != 0xb7ffffff {
		t.Fatal(mark, err)
	}
}
func TestTrafficFailuresNoFallbackAndNoop(t *testing.T) {
	m, _ := fake(t, nil)
	if err := m.syncTraffic(context.Background(), []int64{1}); err == nil {
		t.Fatal("missing nft accepted")
	}
	m, _ = fake(t, []string{"nft"}, cmd("denied", fs.ErrPermission, "nft", "-j", "list", "ruleset"))
	if err := m.syncTraffic(context.Background(), []int64{1}); err == nil {
		t.Fatal("permission ignored")
	}
	m, _ = fake(t, []string{"nft"}, cmd(inventory(1), nil, "nft", "-j", "list", "counters", "table", "inet", TrafficTable))
	values, err := ReadCounters(context.Background(), m.runner)
	if err != nil || len(values) != 1 {
		t.Fatal(values, err)
	}
	for _, opts := range []Options{{OS: "darwin"}, {OS: "linux", Dev: true}} {
		opts.Runner = &runner{t: t}
		if err := New(opts).Sync(context.Background(), Plan{}, Plan{TrafficUsers: []int64{1}}); err != nil {
			t.Fatal(err)
		}
	}
	// Accounting chains must not look like an administrator's input firewall.
	var rules nftRuleset
	rules.NFTables = append(rules.NFTables, struct {
		Chain *nftObject `json:"chain"`
		Rule  *nftObject `json:"rule"`
	}{Chain: &nftObject{Family: "inet", Table: TrafficTable, Type: "filter", Hook: "input"}})
	if len(rules.inputs()) != 0 {
		t.Fatal("traffic table detected as input firewall")
	}
}
