package subscription

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/revfanc/boxpanel/web"
	"gopkg.in/yaml.v3"
)

func TestSubscriptionGroups(t *testing.T) {
	for _, preset := range []string{"bypass_cn", "gfw_only", "global"} {
		for _, variant := range []string{"multiple", "three", "reality_only", "hysteria2_only", "empty"} {
			t.Run(preset+"/"+variant, func(t *testing.T) {
				s := fixture(t)
				s.Rules.Preset = preset
				switch variant {
				case "three":
					extra := s.Nodes[0]
					extra.ID, extra.Name, extra.Port = 3, "third", 9443
					s.Nodes = append(s.Nodes, extra)
				case "reality_only":
					s.Nodes = s.Nodes[:1]
				case "hysteria2_only":
					s.Nodes = s.Nodes[1:]
				case "empty":
					s.Nodes = nil
				}
				names := []string{}
				for _, n := range s.Nodes {
					names = append(names, n.Name)
				}
				text := web.SubscriptionText()
				groupCount := 1 // a single manual group, whatever the node count

				t.Run("singbox", func(t *testing.T) {
					raw, err := Render(s, Singbox)
					if err != nil {
						t.Fatal(err)
					}
					var config struct {
						Outbounds []struct {
							Type, Tag, Default, URL, Interval string
							Outbounds                         []string
							Tolerance                         int
						}
						DNS struct {
							Servers []struct{ Tag, Detour string }
						}
						Route struct {
							Final   string
							Rules   []struct{ Action string }
							RuleSet []struct {
								HTTPClient struct{ Detour string } `json:"http_client"`
							} `json:"rule_set"`
						}
					}
					if err := json.Unmarshal(raw, &config); err != nil {
						t.Fatal(err)
					}
					if len(config.Outbounds) != groupCount+len(names)+1 {
						t.Fatalf("unexpected outbounds: %+v", config.Outbounds)
					}
					// One manual selector of all nodes; no auto group, no direct choice.
					want := []string{"direct"}
					if len(names) > 0 {
						want = slices.Clone(names)
					}
					main := config.Outbounds[0]
					if main.Type != "selector" || main.Tag != "proxy" || !slices.Equal(main.Outbounds, want) || main.Default != want[0] {
						t.Fatalf("wrong first layer: %+v", main)
					}
					for _, out := range config.Outbounds[groupCount:] {
						if out.Type == "selector" || out.Type == "urltest" {
							t.Fatal("unexpected extra group")
						}
					}
					final := "proxy"
					if preset == "gfw_only" {
						final = "direct"
					}
					if config.Route.Final != final || config.DNS.Servers[0].Tag != "dns-remote" || config.DNS.Servers[0].Detour != "proxy" {
						t.Fatal("route or remote DNS reference changed")
					}
					for _, set := range config.Route.RuleSet {
						if set.HTTPClient.Detour != "proxy" {
							t.Fatal("rule-set download must still use proxy")
						}
					}
					if len(names) == 0 && config.Route.Rules[len(config.Route.Rules)-1].Action != "reject" {
						t.Fatal("empty subscription must reject unmatched traffic")
					}
				})

				t.Run("clash", func(t *testing.T) {
					raw, err := Render(s, Clash)
					if err != nil {
						t.Fatal(err)
					}
					var config struct {
						Groups []struct {
							Name, Type, URL     string
							Proxies             []string
							Interval, Tolerance int
							Lazy, Hidden        bool
						} `yaml:"proxy-groups"`
						Providers map[string]struct{ Proxy string } `yaml:"rule-providers"`
						Rules     []string
					}
					if err := yaml.Unmarshal(raw, &config); err != nil {
						t.Fatal(err)
					}
					if len(config.Groups) != groupCount {
						t.Fatalf("unexpected groups: %+v", config.Groups)
					}
					// One manual group of all nodes; no auto group, no DIRECT choice.
					want := slices.Clone(names)
					if len(names) == 0 {
						want = []string{"REJECT"}
					}
					main := config.Groups[0]
					if main.Type != "select" || main.Name != text["select"] || !slices.Equal(main.Proxies, want) {
						t.Fatalf("wrong first layer: %+v", main)
					}
					for _, provider := range config.Providers {
						if provider.Proxy != text["select"] {
							t.Fatal("provider must still use the main selector")
						}
					}
					final, dns := text["select"], text["select"]
					if preset == "gfw_only" {
						final = "DIRECT"
					}
					if len(names) == 0 {
						final, dns = "REJECT", "REJECT"
					}
					if config.Rules[len(config.Rules)-1] != "MATCH,"+final || dnsEndpointTarget(t, config.Rules, s.Rules.DNS.Remote) != dns {
						t.Fatal("MATCH or remote DNS reference changed")
					}
				})
			})
		}
	}
}
