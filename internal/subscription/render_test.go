package subscription

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/share"
	"github.com/revfanc/boxpanel/internal/singbox"
	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "update client subscription goldens")

func fixture(t *testing.T) Snapshot {
	t.Helper()
	cert, err := os.ReadFile("testdata/cert.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(cert)
	sum := sha256.Sum256(block.Bytes)
	return Snapshot{Rules: db.RulesSettings{Preset: "bypass_cn", Source: "metacubex", UpdateInterval: "1d", CustomDirect: []string{}, CustomProxy: []string{}, DNS: db.DNSSettings{Remote: "https://1.1.1.1/dns-query", Local: "https://223.5.5.5/dns-query"}}, Nodes: []share.Node{
		{ID: 1, Name: "香港 Reality", Host: "203.0.113.8", Port: 443, UUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Reality: &share.Reality{ServerName: "www.example.com", PublicKey: "eNXgbb5tlL6g2fud8-bam8Tz5tLHPhwxkOsxcjDnqFs", ShortID: "0123456789abcdef", Fingerprint: "chrome"}},
		{ID: 2, Name: "香港 Hysteria2", Host: "203.0.113.8", Port: 8443, Password: "test-only-p@ss:/?#密", Hysteria2: &confgen.Hysteria2Options{SNI: "www.bing.com", CertMode: "self_signed"}, Certificate: string(cert), Fingerprint: hex.EncodeToString(sum[:])},
	}}
}

func TestGolden(t *testing.T) {
	for _, preset := range []string{"bypass_cn", "gfw_only", "global"} {
		for _, variant := range []string{"default", "options", "empty", "single", "names"} {
			for _, format := range []Format{Singbox, Clash, Base64} {
				name := string(format) + "_" + preset + "_" + variant
				t.Run(name, func(t *testing.T) {
					s := fixture(t)
					s.Rules.Preset = preset
					if variant == "options" {
						s.Rules.BlockAds = true
						s.Rules.Source = "sagernet"
						s.Rules.Mirror = "https://mirror.example/"
						s.Rules.UpdateInterval = "12h"
						s.Rules.CustomDirect = []string{"local.example"}
						s.Rules.CustomProxy = []string{"proxy.example"}
						s.Rules.IPv6 = true
						s.Rules.DNS.FakeIP = true
						s.Rules.DNS.Local = "https://dns.example:8443/custom-query"
						s.Nodes[1].Hysteria2.ObfsPassword = "test-obfs:#特殊"
						s.Nodes[1].Hysteria2.PortHopping = confgen.PortHopping{Enabled: true, Start: 20000, End: 30000}
						s.Nodes[1].Hysteria2.UpMbps = 50
						s.Nodes[1].Hysteria2.DownMbps = 100
						for i := range s.Nodes {
							s.Nodes[i].Host = "2001:db8::8"
						}
					}
					if variant == "empty" {
						s.Nodes = nil
					}
					if variant == "single" {
						s.Nodes = s.Nodes[:1]
					}
					if variant == "names" {
						extra := s.Nodes[0]
						extra.ID, extra.Port, extra.Name = 3, 9443, " Reality · 443 "
						s.Nodes[0].Name, s.Nodes[1].Name = "", " \t\n "
						s.Nodes = UniqueNames(append(s.Nodes, extra))
					}
					before, _ := json.Marshal(s)
					got, err := Render(s, format)
					if err != nil {
						t.Fatal(err)
					}
					again, err := Render(s, format)
					if err != nil || !bytes.Equal(got, again) {
						t.Fatal("unstable rendering")
					}
					after, _ := json.Marshal(s)
					if !bytes.Equal(before, after) {
						t.Fatal("mutated snapshot")
					}
					path := filepath.Join("testdata", name+".golden")
					if *update {
						if err := os.WriteFile(path, got, 0600); err != nil {
							t.Fatal(err)
						}
					}
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(want, got) {
						t.Fatalf("golden mismatch: %s; go test ./internal/subscription -update", path)
					}
					if format == Clash {
						var v map[string]any
						if err := yaml.Unmarshal(got, &v); err != nil {
							t.Fatal(err)
						}
						if v["rule-providers"] == nil {
							t.Fatal("missing providers")
						}
					}
					if format == Base64 {
						raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(got)))
						if err != nil {
							t.Fatal(err)
						}
						if len(s.Nodes) > 0 && !strings.HasSuffix(string(raw), "\n") {
							t.Fatal("missing URI newline")
						}
					}
				})
			}
		}
	}
}

func TestEverySingboxGoldenCheck(t *testing.T) {
	paths, err := filepath.Glob("testdata/singbox_*.golden")
	if err != nil || len(paths) == 0 {
		t.Fatal("missing client goldens")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			binary, err := exec.LookPath("sing-box")
			if err != nil {
				t.Skip("run make tools and make test to validate with sing-box")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			v, err := exec.CommandContext(ctx, binary, "version").Output()
			if err != nil || !strings.HasPrefix(string(v), "sing-box version "+singbox.PinnedVersion+"\n") {
				t.Fatal("requires pinned sing-box")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			config := filepath.Join(dir, "client.json")
			if err = os.WriteFile(config, data, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, binary, "check", "-c", config)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("check failed: %v\n%s", err, out)
			}
			if len(out) > 0 {
				t.Fatalf("unexpected warnings: %s", out)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	for _, ua := range []string{"sing-box/1.14", "SFA", "sfi", "SFM/1", "sft", "Mozilla sing-box"} {
		f, e := Detect(url.Values{}, ua, "text/html")
		if e != nil || f != Singbox {
			t.Fatalf("%s: %s %v", ua, f, e)
		}
	}
	for _, ua := range []string{"Clash", "MIHOMO", "stash", "Verge", "FlClash", "nyanpasu"} {
		f, e := Detect(nil, ua, "")
		if e != nil || f != Clash {
			t.Fatalf("%s: %s %v", ua, f, e)
		}
	}
	for _, ua := range []string{"v2rayN", "Shadowrocket", "NekoBox", "Mozilla", ""} {
		f, e := Detect(nil, ua, "")
		if e != nil || f != Base64 {
			t.Fatalf("%s: %s %v", ua, f, e)
		}
	}
	f, e := Detect(nil, "Mozilla/5.0", "text/html")
	if e != nil || f != HTML {
		t.Fatal(f, e)
	}
	for _, q := range []url.Values{{"format": {""}}, {"format": {"yaml"}}, {"format": {"clash", "singbox"}}} {
		if _, err := Detect(q, "sing-box", ""); err == nil {
			t.Fatal("accepted invalid format")
		}
	}
	f, e = Detect(url.Values{"format": {"base64"}}, "Mozilla sing-box", "text/html")
	if e != nil || f != Base64 {
		t.Fatal("explicit format must win")
	}
}

func TestRedactAndNames(t *testing.T) {
	s := fixture(t)
	s.Nodes[1].Hysteria2.ObfsPassword = "quote\"pass\n:x"
	s.Nodes[0].Name = s.Nodes[1].Hysteria2.ObfsPassword
	for _, f := range []Format{Singbox, Clash} {
		raw, err := Render(s, f)
		if err != nil {
			t.Fatal(err)
		}
		clean, err := Redact(raw, f, []string{s.Nodes[0].UUID, s.Nodes[1].Password, s.Nodes[1].Hysteria2.ObfsPassword})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(clean, []byte("aaaaaaaa")) || bytes.Contains(clean, []byte("test-only")) || bytes.Contains(clean, []byte("quote")) {
			t.Fatal("preview leaked credential")
		}
	}
	s.Nodes = append(s.Nodes, s.Nodes[0])
	s.Nodes[2].ID = 3
	s.Nodes[0].Name = "proxy"
	s.Nodes[2].Name = "proxy"
	before := append([]share.Node{}, s.Nodes...)
	names := UniqueNames(s.Nodes)
	if !reflect.DeepEqual(s.Nodes, before) {
		t.Fatal("mutated names")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n.Name] || n.Name == "proxy" {
			t.Fatal("duplicate/reserved name")
		}
		seen[n.Name] = true
	}
}

func TestDNSAndIPRouting(t *testing.T) {
	s := fixture(t)
	s.Rules.DNS.FakeIP = true
	s.Rules.Preset = "gfw_only"
	b, err := Render(s, Singbox)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		DNS struct {
			Rules []struct {
				RuleSet   []string `json:"rule_set"`
				QueryType []string `json:"query_type"`
				Server    string
			}
		}
		Route struct {
			Rules []struct {
				Action, Server string
				RuleSet        []string `json:"rule_set"`
			}
		}
	}
	if err := json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	fake, real := false, false
	for _, r := range config.DNS.Rules {
		if len(r.RuleSet) > 0 && r.RuleSet[0] == "geosite-gfw" {
			fake = fake || r.Server == "dns-fakeip" && len(r.QueryType) == 2
			real = real || r.Server == "dns-remote" && len(r.QueryType) == 0
		}
	}
	if !fake || !real {
		t.Fatal("GFW queries need FakeIP A/AAAA and real remote DNS for other types")
	}
	s.Rules.Preset = "bypass_cn"
	b, err = Render(s, Singbox)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, r := range config.Route.Rules {
		if r.Action == "resolve" && r.Server == "dns-remote" {
			resolved = true
		}
		if len(r.RuleSet) > 0 && r.RuleSet[0] == "geoip-cn" && !resolved {
			t.Fatal("country IP matching requires resolved domain addresses")
		}
	}
}
