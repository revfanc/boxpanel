package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"strings"

	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/firewall"
)

// PrepareExitGuard is used by systemd before the core starts independently of
// the panel, including boot and crash recovery. It only reads the checked
// config. It does not take the DB/apply lock held by a caller restarting core.
func PrepareExitGuard(ctx context.Context, path string, manager *firewall.Manager) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // sing-box reports a missing configuration itself
	}
	if err != nil {
		return fmt.Errorf("read configuration for exit guard: %w", err)
	}
	plan, err := exitGuardPlan(raw)
	if err != nil {
		return err
	}
	return manager.PrepareExit(ctx, firewall.Plan{}, plan)
}

func exitGuardPlan(raw []byte) (firewall.Plan, error) {
	type rule struct {
		Type    string   `json:"type"`
		Mode    string   `json:"mode"`
		Action  string   `json:"action"`
		Inbound []string `json:"inbound"`
		Source  []string `json:"source_ip_cidr"`
		Invert  bool     `json:"invert"`
		Rules   []rule   `json:"rules"`
	}
	var cfg struct {
		Inbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
			Port int    `json:"listen_port"`
		} `json:"inbounds"`
		Route struct {
			Rules []rule `json:"rules"`
		} `json:"route"`
	}
	var plan firewall.Plan
	if json.Unmarshal(raw, &cfg) != nil {
		return plan, fmt.Errorf("read exit guard: regenerate the invalid sing-box configuration with boxpanel apply")
	}
	sources := map[string]string{}
	for _, r := range cfg.Route.Rules {
		if r.Type != "logical" || r.Mode != "and" || r.Action != "reject" || len(r.Rules) != 2 {
			continue
		}
		inbound, source := r.Rules[0], r.Rules[1]
		if len(inbound.Inbound) == 1 && source.Invert && len(source.Source) == 1 {
			if prefix, err := netip.ParsePrefix(source.Source[0]); err == nil && prefix.Bits() == prefix.Addr().BitLen() {
				sources[inbound.Inbound[0]] = prefix.Addr().String()
			}
		}
	}
	for _, in := range cfg.Inbounds {
		if in.Type == "shadowsocks" && strings.HasPrefix(in.Tag, exitlink.InboundPrefix) {
			plan.ExitPorts = append(plan.ExitPorts, firewall.ExitPort{Port: in.Port, Source: sources[in.Tag]})
		}
	}
	return plan, plan.Validate()
}
