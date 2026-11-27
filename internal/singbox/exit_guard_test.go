package singbox

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/revfanc/boxpanel/internal/firewall"
)

func TestBootExitGuardReadsCheckedConfiguration(t *testing.T) {
	checked, err := os.ReadFile("../confgen/testdata/exit_split.golden")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := exitGuardPlan(checked)
	if err != nil || !slices.Equal(plan.ExitPorts, []firewall.ExitPort{{Port: 20001, Source: "2001:db8::2"}}) {
		t.Fatal("failed to restore configuration's restricted source", plan, err)
	}
	noGuard := bytes.ReplaceAll(checked, []byte("2001:db8::2/128"), []byte("2001:db8::/64"))
	if _, err := exitGuardPlan(noGuard); err == nil {
		t.Fatal("core would start with a missing single-address source guard")
	}
	old, err := os.ReadFile("../confgen/testdata/dual.golden")
	if err != nil {
		t.Fatal(err)
	}
	if plan, err = exitGuardPlan(old); err != nil || len(plan.ExitPorts) != 0 {
		t.Fatal("unrelated kernel listeners affected by boot guard", plan, err)
	}
}
