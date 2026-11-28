package exitlink

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestStreamingSources(t *testing.T) {
	seed, err := BundledStreamingRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"netflix.com", "nflxvideo.net", "disneyplus.com", "bamgrid.com", "dssott.com"} {
		if !slices.Contains(seed.Suffixes, domain) {
			t.Fatal("missing seed suffix", domain)
		}
	}
	for _, shared := range []string{"adobedtm.com", "braze.com", "execute-api.us-east-1.amazonaws.com", "cdn.optimizely.com"} {
		if slices.Contains(seed.Suffixes, shared) {
			t.Fatal("shared service included", shared)
		}
	}
	const ris = `{"status":"ok","data":{"resource":"%s","counts":{"v4":{"originating":1},"v6":{"originating":1}},"prefixes":{"v4":{"originating":["45.57.0.0/24"],"transiting":["8.8.8.0/24"]},"v6":{"originating":["2606:4700::/48"]}}}}`
	for _, tc := range []struct{ name, source, old, replacement string }{
		{name: "valid"},
		{"missing Disney anchor", "disney.list", "disneyplus.com", "braze.com"},
		{"missing Netflix anchor", "netflix.list", "netflix.com", "video.test"},
		{"bad rule", "disney.list", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD"},
		{"wrong ASN", "as23344.json", "23344", "16509"},
		{"wrong status", "as40027.json", "ok", "error"},
		{"partial family", "as2906.json", "2606:4700::/48", ""},
		{"bad count", "as2906.json", "originating\":1", "originating\":2"},
		{"missing AF", "as23344.json", "v6", "unknown"},
		{"private", "as40027.json", "45.57.0.0/24", "10.0.0.0/8"},
		{"catch all", "as2906.json", "45.57.0.0/24", "0.0.0.0/0"},
		{"wrong AF", "as2906.json", "45.57.0.0/24", "2606:4700::/48"},
		{"host bits", "as2906.json", "45.57.0.0/24", "45.57.0.2/24"},
		{"offline", "as23344.json", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadStreamingRules(func(name, address string) ([]byte, error) {
				u, err := url.Parse(address)
				if err != nil || u.Scheme != "https" {
					t.Fatal("invalid rule source", address)
				}
				var raw string
				switch name {
				case "netflix.list":
					raw = "+.netflix.com\nplay.netflix.com"
				case "disney.list":
					raw = "DOMAIN-SUFFIX,disneyplus.com\nDOMAIN,login.disneyplus.com\nDOMAIN-SUFFIX,braze.com"
				default:
					asn := strings.TrimSuffix(strings.TrimPrefix(name, "as"), ".json")
					if u.Host != "stat.ripe.net" || u.Query().Get("types") != "o" || u.Query().Get("resource") != "AS"+asn {
						t.Fatal(address)
					}
					raw = fmt.Sprintf(ris, asn)
				}
				if name == tc.source {
					if tc.name == "offline" {
						return nil, errors.New("offline")
					}
					raw = strings.ReplaceAll(raw, tc.old, tc.replacement)
				}
				return []byte(raw), nil
			})
			if tc.name != "valid" {
				if err == nil || len(got.Domains)+len(got.Suffixes)+len(got.Prefixes) != 0 {
					t.Fatal("accepted partial update", got, err)
				}
				return
			}
			if err != nil || !slices.Equal(got.Domains, []string{"login.disneyplus.com", "play.netflix.com"}) ||
				!slices.Equal(got.Suffixes, []string{"disneyplus.com", "netflix.com"}) ||
				!slices.Equal(got.Prefixes, []string{"2606:4700::/48", "45.57.0.0/24"}) {
				t.Fatal(got, err)
			}
		})
	}
}
