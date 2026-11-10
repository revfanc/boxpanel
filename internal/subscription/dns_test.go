package subscription

import (
	"encoding/json"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/web"
	"gopkg.in/yaml.v3"
)

func TestClashDNSLeakProtection(t *testing.T) {
	for _, preset := range []string{"global", "bypass_cn", "gfw_only"} {
		for _, variant := range []string{"default", "options", "custom_dns", "ipv6_dns", "shared_dns", "empty"} {
			t.Run(preset+"/"+variant, func(t *testing.T) {
				s := fixture(t)
				s.Rules.Preset = preset
				s.Rules.CustomDirect = []string{"direct.example", "same.example"}
				s.Rules.CustomProxy = []string{"proxy.example", "same.example"}
				if variant == "options" {
					s.Rules.DNS.FakeIP, s.Rules.IPv6, s.Rules.BlockAds = true, true, true
					s.Rules.Source = "sagernet"
					s.Rules.Mirror = "https://mirror.example/"
				}
				if variant == "custom_dns" {
					s.Rules.DNS.Local = "https://dns.example:8443/custom-query"
					s.Rules.DNS.Remote = "https://remote.example:8443/dns-query?test=1"
				}
				if variant == "ipv6_dns" {
					s.Rules.DNS.Remote = "https://[2606:4700:4700::1111]:8443/dns-query"
				}
				if variant == "shared_dns" {
					s.Rules.DNS.Local = s.Rules.DNS.Remote
				}
				if variant == "empty" {
					s.Nodes = nil
				}
				data, err := Render(s, Clash)
				if err != nil {
					t.Fatal(err)
				}
				var config struct {
					IPv6 bool `yaml:"ipv6"`
					DNS  struct {
						Enable       bool                `yaml:"enable"`
						RespectRules bool                `yaml:"respect-rules"`
						FollowRule   bool                `yaml:"follow-rule"`
						IPv6         bool                `yaml:"ipv6"`
						Mode         string              `yaml:"enhanced-mode"`
						Range        string              `yaml:"fake-ip-range"`
						Filter       []string            `yaml:"fake-ip-filter"`
						Nameserver   []string            `yaml:"nameserver"`
						Bootstrap    []string            `yaml:"default-nameserver"`
						ProxyServer  []string            `yaml:"proxy-server-nameserver"`
						Policy       map[string][]string `yaml:"nameserver-policy"`
						Fallback     []string            `yaml:"fallback"`
					} `yaml:"dns"`
					Providers map[string]struct {
						Format, Behavior, URL, Proxy string
					} `yaml:"rule-providers"`
					Groups []struct {
						Name, Type string
						Proxies    []string
					} `yaml:"proxy-groups"`
					Rules []string `yaml:"rules"`
				}
				if err := yaml.Unmarshal(data, &config); err != nil {
					t.Fatal(err)
				}
				d := config.DNS
				if !d.Enable || !d.RespectRules || !d.FollowRule || d.IPv6 || config.IPv6 || d.Mode != "fake-ip" || d.Range != "198.18.0.1/16" {
					t.Fatal("Clash must always use IPv4 Fake-IP")
				}
				for _, domain := range []string{"*.lan", "*.local", "localhost.ptlink", "time.*.com", "+.msftconnecttest.com", "+.msftncsi.com", "captive.apple.com"} {
					if !slices.Contains(d.Filter, domain) {
						t.Errorf("missing Fake-IP exception %s", domain)
					}
				}
				local := []string{s.Rules.DNS.Local}
				if variant != "custom_dns" && variant != "shared_dns" {
					local = append(local, "https://doh.pub/dns-query")
				}
				if !reflect.DeepEqual(d.Nameserver, local) || !reflect.DeepEqual(d.ProxyServer, local) || !reflect.DeepEqual(d.Bootstrap, []string{"https://223.5.5.5/dns-query"}) || len(d.Fallback) != 0 {
					t.Fatal("direct and bootstrap DNS must use domestic DoH defaults (or explicit local override), without a leaking fallback")
				}
				selectName := web.SubscriptionText()["select"]
				remote := []string{s.Rules.DNS.Remote}
				dnsTarget := selectName
				if variant == "empty" {
					dnsTarget = "REJECT"
				}
				if dnsEndpointTarget(t, config.Rules, remote[0]) != dnsTarget {
					t.Fatal("remote DNS must route to the proxy (or reject with no nodes), even under gfw_only")
				}
				for _, resolver := range append(slices.Clone(local), d.Bootstrap...) {
					want := "DIRECT"
					if resolver == remote[0] {
						want = dnsTarget
					}
					if dnsEndpointTarget(t, config.Rules, resolver) != want {
						t.Fatal("local DNS transport route incorrect")
					}
				}
				if !reflect.DeepEqual(d.Policy["+.proxy.example"], remote) || !reflect.DeepEqual(d.Policy["+.direct.example"], local) || !reflect.DeepEqual(d.Policy["+.same.example"], local) {
					t.Fatal("custom DNS policy must follow direct/proxy precedence")
				}
				for key, resolvers := range d.Policy {
					for _, resolver := range resolvers {
						if slices.Contains(local, resolver) {
							continue
						}
						if resolver != remote[0] || dnsEndpointTarget(t, config.Rules, resolver) != dnsTarget {
							t.Fatalf("DNS policy %s can bypass the proxy: %s", key, resolver)
						}
					}
					if tag, ok := strings.CutPrefix(key, "rule-set:"); ok {
						if _, exists := config.Providers[tag]; !exists {
							t.Fatalf("DNS refers to missing provider %s", tag)
						}
					}
				}
				selector := false
				for _, group := range config.Groups {
					selector = selector || group.Name == selectName && group.Type == "select" && len(group.Proxies) > 0
				}
				if !selector {
					t.Fatal("DNS route must reference a usable proxy selector")
				}
				for tag, provider := range config.Providers {
					if provider.Format != "yaml" || !strings.HasSuffix(provider.URL, ".yaml") || !strings.Contains(provider.URL, "MetaCubeX/meta-rules-dat/meta/geo/") || provider.Proxy != selectName {
						t.Errorf("provider %s is not YAML downloaded through the proxy: %+v", tag, provider)
					}
				}
				for _, rule := range config.Rules {
					parts := strings.Split(rule, ",")
					ipRule := parts[0] == "IP-CIDR" || parts[0] == "IP-CIDR6" || parts[0] == "RULE-SET" && config.Providers[parts[1]].Behavior == "ipcidr"
					if ipRule && parts[len(parts)-1] != "no-resolve" {
						t.Errorf("IP rule triggers local DNS: %s", rule)
					}
				}
				if preset == "bypass_cn" {
					if !reflect.DeepEqual(d.Policy["rule-set:geosite-cn"], local) || !slices.Contains(d.Filter, "rule-set:geosite-cn") || !slices.Contains(config.Rules, "RULE-SET,geoip-cn,DIRECT,no-resolve") {
						t.Fatal("bypass_cn must retain domestic DNS and no-resolve IP routing")
					}
				} else if slices.Contains(d.Filter, "rule-set:geosite-cn") {
					t.Fatal("do not exempt CN names in a preset without the CN provider")
				}
				final := "MATCH," + selectName
				if preset == "gfw_only" {
					final = "MATCH,DIRECT"
					if !reflect.DeepEqual(d.Policy["rule-set:geosite-gfw"], remote) || d.Policy["rule-set:geosite-geolocation-!cn"] != nil {
						t.Fatal("gfw_only must proxy GFW DNS and leave other domains direct")
					}
				} else {
					if !reflect.DeepEqual(d.Policy["rule-set:geosite-geolocation-!cn"], remote) || !reflect.DeepEqual(d.Policy["captive.apple.com"], remote) {
						t.Fatal("foreign real queries must use proxy DNS")
					}
				}
				if variant == "empty" {
					final = "MATCH,REJECT"
				}
				if config.Rules[len(config.Rules)-1] != final {
					t.Fatal("preset final changed")
				}
			})
		}
	}
}

// Evaluate the DNS endpoint's first matching host/IP route, so a later proxy
// rule cannot hide an earlier direct rule for the same resolver.
func dnsEndpointTarget(t *testing.T, rules []string, resolver string) string {
	t.Helper()
	u, err := url.Parse(resolver)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	ip, _ := netip.ParseAddr(host)
	for _, rule := range rules {
		p := strings.Split(rule, ",")
		switch p[0] {
		case "DOMAIN":
			if p[1] == host {
				return p[2]
			}
		case "DOMAIN-SUFFIX":
			if host == p[1] || strings.HasSuffix(host, "."+p[1]) {
				return p[2]
			}
		case "IP-CIDR", "IP-CIDR6":
			prefix, err := netip.ParsePrefix(p[1])
			if err != nil {
				t.Fatal(err)
			}
			if prefix.Contains(ip) {
				return p[2]
			}
		case "MATCH":
			return p[1]
		}
	}
	return ""
}

func TestSingboxDNSLeakProtection(t *testing.T) {
	for _, preset := range []string{"global", "bypass_cn", "gfw_only"} {
		for _, fakeIP := range []bool{false, true} {
			s := fixture(t)
			s.Rules.Preset, s.Rules.DNS.FakeIP = preset, fakeIP
			data, err := Render(s, Singbox)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				DNS struct {
					Servers []struct{ Tag, Detour, Server string }
					Final   string
					Rules   []struct {
						Action, Server, Rcode string
						QueryType             []string `json:"query_type"`
						RuleSet               []string `json:"rule_set"`
					}
				}
				Outbounds []struct{ Tag, Type string }
				Route     struct {
					Rules []struct{ Action, Protocol string }
				}
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			remote, hijack, aaaa, cn := false, false, false, false
			for _, server := range config.DNS.Servers {
				if server.Tag == "dns-remote" {
					if server.Detour != "proxy" {
						t.Fatal("remote DNS lacks proxy detour")
					}
					for _, out := range config.Outbounds {
						remote = remote || out.Tag == server.Detour && out.Type == "selector"
					}
				}
			}
			for _, rule := range config.Route.Rules {
				hijack = hijack || rule.Action == "hijack-dns" && rule.Protocol == "dns"
			}
			for _, rule := range config.DNS.Rules {
				aaaa = aaaa || slices.Contains(rule.QueryType, "AAAA") && rule.Action == "predefined" && rule.Rcode == "NOERROR"
				cn = cn || slices.Contains(rule.RuleSet, "geosite-cn") && rule.Server == "dns-local"
			}
			final := "dns-remote"
			if preset == "gfw_only" {
				final = "dns-local"
			}
			if !remote || !hijack || !aaaa || config.DNS.Final != final || preset == "bypass_cn" && !cn {
				t.Fatalf("DNS protection missing for %s (FakeIP=%v)", preset, fakeIP)
			}
		}
	}
}
