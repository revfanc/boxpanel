package subscription

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/revfanc/boxpanel/web"
	"gopkg.in/yaml.v3"
)

func TestSubscriptionGroups(t *testing.T) {
	for _, preset := range []string{"bypass_cn", "gfw_only", "global"} {
		for _, variant := range []string{"multiple", "two_auto", "three", "three_auto", "reality_only", "hysteria2_only", "empty"} {
			t.Run(preset+"/"+variant, func(t *testing.T) {
				s := fixture(t)
				s.Rules.Preset = preset
				switch variant {
				case "three", "three_auto":
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
				s.AutoGroup = variant == "two_auto" || variant == "three_auto"
				autoEnabled := variant == "three_auto"
				names := []string{}
				for _, n := range s.Nodes {
					names = append(names, n.Name)
				}
				text := web.SubscriptionText()
				groupCount := 1
				if autoEnabled {
					groupCount++
				}
				if s.AutoGroup {
					disabled := s
					disabled.AutoGroup = false
					for _, format := range []Format{Singbox, Clash, Base64} {
						before, err := Render(disabled, format)
						if err != nil {
							t.Fatal(err)
						}
						after, err := Render(s, format)
						wantSame := !autoEnabled || format == Base64
						if err != nil || bytes.Equal(before, after) != wantSame {
							t.Fatalf("%s output comparison: %v", format, err)
						}
					}
				}

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
					want := []string{"direct"}
					if len(names) > 0 {
						want = slices.Clone(names)
					}
					if autoEnabled {
						want = append(want, text["autoSelect"])
						auto := config.Outbounds[1+len(names)]
						if auto.Type != "urltest" || auto.Tag != text["autoSelect"] || !slices.Equal(auto.Outbounds, names) ||
							auto.URL != "https://www.gstatic.com/generate_204" || auto.Interval != "5m" || auto.Tolerance != 50 {
							t.Fatalf("wrong auto group: %+v", auto)
						}
					}
					main := config.Outbounds[0]
					if main.Type != "selector" || main.Tag != "proxy" || !slices.Equal(main.Outbounds, want) || main.Default != want[0] {
						t.Fatalf("wrong first layer: %+v", main)
					}
					for _, out := range config.Outbounds[1 : 1+len(names)] {
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
					want := slices.Clone(names)
					if len(names) == 0 {
						want = []string{"REJECT"}
					}
					if autoEnabled {
						want = append(want, text["autoSelect"])
						auto := config.Groups[1]
						if auto.Type != "url-test" || auto.Name != text["autoSelect"] || !slices.Equal(auto.Proxies, names) ||
							auto.URL != "https://www.gstatic.com/generate_204" || auto.Interval != 300 || auto.Tolerance != 50 || !auto.Lazy || auto.Hidden {
							t.Fatalf("wrong auto group: %+v", auto)
						}
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
