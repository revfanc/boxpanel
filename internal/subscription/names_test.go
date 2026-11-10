package subscription

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/share"
	"github.com/revfanc/boxpanel/web"
	"gopkg.in/yaml.v3"
)

func TestUniqueDisplayNames(t *testing.T) {
	text := web.SubscriptionText()
	for _, tc := range []struct {
		name        string
		input, want []string
	}{
		{"trim", []string{" \tHong Kong\u3000", " 日本 "}, []string{"Hong Kong", "日本"}},
		{"duplicate", []string{"HK", " HK ", "HK"}, []string{"HK", "HK (2)", "HK (3)"}},
		{"existing_suffix", []string{"HK (2)", "HK", "HK", "HK"}, []string{"HK (2)", "HK", "HK (3)", "HK (4)"}},
		{"suffix_collision", []string{"HK", "HK", "HK (2)"}, []string{"HK", "HK (2)", "HK (2) (2)"}},
		{"reserved", []string{"proxy", "auto", "manual", "direct", "DIRECT", "REJECT", text["select"]},
			[]string{"proxy (2)", "auto (2)", "manual (2)", "direct (2)", "DIRECT (2)", "REJECT (2)", text["select"] + " (2)"}},
		{"unsafe", []string{" a,b\r\nc\t\x00\x7f\u0085\u2028\u2029d ", "a,b", "a，b", `"quoted": [node] #/?%&`},
			[]string{"a，b c d", "a，b", "a，b (2)", `"quoted": [node] #/?%&`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var nodes []share.Node
			for i, name := range tc.input {
				n := fixture(t).Nodes[0]
				n.ID, n.Name = int64(i+1), name
				nodes = append(nodes, n)
			}
			slices.Reverse(nodes)
			before := slices.Clone(nodes)
			got := UniqueNames(nodes)
			if !reflect.DeepEqual(nodes, before) {
				t.Fatal("name normalization mutated input")
			}
			if !reflect.DeepEqual(got, UniqueNames(got)) {
				t.Fatal("name normalization is not idempotent")
			}
			for i, n := range got {
				if n.ID != int64(i+1) || n.Name != tc.want[i] {
					t.Fatalf("node %d: got %q, want %q", n.ID, n.Name, tc.want[i])
				}
				if strings.ContainsFunc(n.Name, func(r rune) bool { return r == ',' || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
					t.Fatalf("unsafe name: %q", n.Name)
				}
			}
			s := fixture(t)
			s.Nodes = got
			assertRenderedNames(t, s, tc.want)
		})
	}
}

// Exercise the production DB path: blank remarks must not fall back to tags,
// and every export (including administrator node links) must use the same name.
func TestStoreDisplayNames(t *testing.T) {
	_, store := publicFixture(t)
	ctx := context.Background()
	material, err := certs.Hysteria2("www.bing.com", store.now())
	if err != nil {
		t.Fatal(err)
	}
	store.Certificate = func(_ int64, _ string) ([]byte, error) { return material.Certificate, nil }
	if err := db.SetSetting(ctx, store.DB, db.Hysteria2Fingerprints, map[string]string{"2": material.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(confgen.Hysteria2Options{SNI: "www.bing.com", CertMode: "self_signed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO inbounds (id,tag,protocol,port,options,created_at,updated_at) VALUES (2,'old-hysteria2-tag','hysteria2',8443,?,0,0)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO inbounds (id,tag,protocol,port,enabled,remark,options,created_at,updated_at) SELECT 3,'disabled',protocol,9443,0,'HK',options,0,0 FROM inbounds WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ reality, hysteria2, wantReality, wantHysteria2 string }{
		{"", "", "Reality · 443", "Hysteria2 · 8443"},
		{" \t\n\u3000", "\r\x00\u2028", "Reality · 443", "Hysteria2 · 8443"},
		{" HK ", "HK", "HK", "HK (2)"},
		{" Hysteria2 · 8443 ", "", "Hysteria2 · 8443", "Hysteria2 · 8443 (2)"},
		{"manual", "手动选择", "manual (2)", "手动选择"}, // no longer a group name
		{" a,b\nname ", `"node": [x] #/?%&`, "a，b name", `"node": [x] #/?%&`},
	} {
		t.Run(fmt.Sprintf("%q/%q", tc.reality, tc.hysteria2), func(t *testing.T) {
			if _, err := store.DB.Exec("UPDATE inbounds SET remark=CASE id WHEN 1 THEN ? ELSE ? END WHERE id IN (1,2)", tc.reality, tc.hysteria2); err != nil {
				t.Fatal(err)
			}
			want := []string{tc.wantReality, tc.wantHysteria2}
			for _, public := range []bool{false, true} {
				var token *string
				if public {
					value := "token-that-must-not-leak"
					token = &value
				}
				d, err := store.Load(ctx, 0, token)
				if err != nil {
					t.Fatal(err)
				}
				assertRenderedNames(t, d.Snapshot, want)
				links, err := MakeLinks(d)
				if err != nil || len(links.Nodes) != len(want) {
					t.Fatalf("node links: %v", err)
				}
				for i, link := range links.Nodes {
					u, err := url.Parse(link.URI)
					if err != nil || link.InboundID != int64(i+1) || link.Remark != want[i] || u.Fragment != want[i] {
						t.Fatal("node link name differs from subscription")
					}
				}
			}
		})
	}
	var saved string
	if err := store.DB.QueryRow("SELECT remark FROM inbounds WHERE id=1").Scan(&saved); err != nil || saved != " a,b\nname " {
		t.Fatal("export rewrote the stored remark")
	}
}

func assertRenderedNames(t *testing.T, s Snapshot, want []string) {
	t.Helper()
	for _, format := range []Format{Singbox, Clash, Base64} {
		raw, err := Render(s, format)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		switch format {
		case Singbox:
			var config struct{ Outbounds []struct{ Type, Tag string } }
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			for _, out := range config.Outbounds {
				if out.Type == "vless" || out.Type == "hysteria2" {
					got = append(got, out.Tag)
				}
			}
		case Clash:
			var config struct{ Proxies []struct{ Name string } }
			if err := yaml.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			for _, proxy := range config.Proxies {
				got = append(got, proxy.Name)
			}
		case Base64:
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSuffix(string(decoded), "\n"), "\n") {
				u, err := url.Parse(line)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, u.Fragment)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s names = %q, want %q", format, got, want)
		}
	}
}
