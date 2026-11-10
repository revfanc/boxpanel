package rules

import (
	"github.com/revfanc/boxpanel/internal/db"
	"reflect"
	"strings"
	"testing"
)

func testSettings() db.RulesSettings {
	return db.RulesSettings{Preset: "bypass_cn", Source: "metacubex", UpdateInterval: "1d", DNS: db.DNSSettings{Local: "https://223.5.5.5/dns-query", Remote: "https://1.1.1.1/dns-query"}}
}
func TestPlansAndSources(t *testing.T) {
	for _, preset := range []string{"bypass_cn", "gfw_only", "global"} {
		s := testSettings()
		s.Preset = preset
		s.BlockAds = true
		s.CustomDirect = []string{"same.example"}
		s.CustomProxy = []string{"same.example"}
		p, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		if p.Rules[0].Target != "reject" || p.Rules[1].Kind != "private" || p.Rules[2].Target != "direct" || p.Rules[3].Target != "proxy" {
			t.Fatal(p)
		}
		if (p.Final == "direct") != (preset == "gfw_only") {
			t.Fatal(p)
		}
	}
	for _, s := range Sources() {
		for _, source := range []string{"metacubex", "sagernet"} {
			for _, format := range []string{"singbox", "clash"} {
				got, err := URL(s.Tag, source, format, "https://mirror.example/", nil)
				if err != nil || !strings.HasPrefix(got, "https://mirror.example/https://raw.githubusercontent.com/") {
					t.Fatal(got, err)
				}
				if format == "clash" && !strings.Contains(got, "MetaCubeX") {
					t.Fatal("mihomo must use MetaCubeX")
				}
				if format == "clash" && !strings.HasSuffix(got, ".yaml") {
					t.Fatal("Clash providers must use portable YAML")
				}
				if format == "singbox" && !strings.HasSuffix(got, ".srs") {
					t.Fatal("sing-box providers must retain SRS")
				}
			}
		}
	}
	u, _ := URL("geosite-gfw", "sagernet", "singbox", "", nil)
	if u != MetaSRGFW {
		t.Fatal("unavailable SagerNet GFW must use documented fallback")
	}
	if !reflect.DeepEqual(Sources(), Sources()) {
		t.Fatal("unstable sources")
	}
}
func TestValidation(t *testing.T) {
	for _, change := range []func(*db.RulesSettings){func(s *db.RulesSettings) { s.Preset = "bad" }, func(s *db.RulesSettings) { s.Source = "bad" }, func(s *db.RulesSettings) { s.DNS.Local = "https://user:pass@host/" }, func(s *db.RulesSettings) { s.DNS.Remote = "http://1.1.1.1" }, func(s *db.RulesSettings) { s.Mirror = "https://mirror.test" }, func(s *db.RulesSettings) { s.UpdateInterval = "0s" }, func(s *db.RulesSettings) { s.UpdateInterval = "999999999999999999d" }, func(s *db.RulesSettings) { s.CustomDirect = []string{"a,b,DIRECT"} }, func(s *db.RulesSettings) { s.CustomProxy = []string{"*.example.com"} }} {
		s := testSettings()
		change(&s)
		if Validate(s) == nil {
			t.Fatal("accepted invalid rules")
		}
	}
	for _, host := range []string{"example.com", "2001:db8::1", "127.0.0.1"} {
		if !ValidHost(host) {
			t.Fatal(host)
		}
	}
	for _, host := range []string{"a/b", "host:443", "[::1]", "fe80::1%en0", "a\r\nb", "-a.com"} {
		if ValidHost(host) {
			t.Fatal(host)
		}
	}
}
