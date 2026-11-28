package exitlink

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

//go:embed seed/streaming/*.list seed/streaming/*.json
var streamingSeed embed.FS

// BundledStreamingRules supplies the complete offline seed. Its slices are read-only.
var BundledStreamingRules = sync.OnceValues(func() (DestinationRules, error) {
	return LoadStreamingRules(func(name, _ string) ([]byte, error) {
		return streamingSeed.ReadFile("seed/streaming/" + name)
	})
})

// LoadStreamingRules uses the same validation for the bundled seed and every
// refresh. Publish only a complete set from both domain lists and all origins.
func LoadStreamingRules(read func(name, url string) ([]byte, error)) (DestinationRules, error) {
	netflix, err := read("netflix.list", "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/netflix.list")
	if err != nil {
		return DestinationRules{}, fmt.Errorf("read Netflix domains: %w", err)
	}
	rules, err := parseDomainList(netflix)
	if err != nil {
		return DestinationRules{}, fmt.Errorf("decode Netflix domains: %w", err)
	}
	if !slices.Contains(rules.Suffixes, "netflix.com") {
		return DestinationRules{}, fmt.Errorf("Netflix domain list is missing netflix.com")
	}
	disney, err := read("disney.list", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/Ruleset/DisneyPlus.list")
	if err != nil {
		return DestinationRules{}, fmt.Errorf("read Disney+ domains: %w", err)
	}
	var disneyAnchor bool
	for line := range strings.Lines(string(disney)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, host, _ := strings.Cut(line, ",")
		host = strings.ToLower(strings.TrimSpace(host))
		if (kind != "DOMAIN" && kind != "DOMAIN-SUFFIX") || !validDomain(host) {
			return DestinationRules{}, fmt.Errorf("invalid Disney+ domain rule")
		}
		// The upstream list includes shared analytics and regional cloud
		// gateways. Keep Disney-branded hosts and the identifiable BAM/DSS names.
		if !strings.Contains(host, "disney") && !underDomain(host, "bamgrid.com") &&
			!underDomain(host, "dssott.com") && !underDomain(host, "bam.nr-data.net") {
			continue
		}
		if kind == "DOMAIN" {
			rules.Domains = append(rules.Domains, host)
		} else {
			rules.Suffixes = append(rules.Suffixes, host)
			disneyAnchor = disneyAnchor || host == "disneyplus.com"
		}
	}
	if !disneyAnchor {
		return DestinationRules{}, fmt.Errorf("Disney+ domain list is missing disneyplus.com")
	}
	// Netflix Open Connect (peering and embedded appliances), Disney Streaming Edge.
	for _, asn := range []string{"2906", "40027", "23344"} {
		url := "https://stat.ripe.net/data/ris-prefixes/data.json?resource=AS" + asn + "&list_prefixes=true&types=o&noise=filter"
		raw, err := read("as"+asn+".json", url)
		if err != nil {
			return DestinationRules{}, fmt.Errorf("read AS%s routes: %w", asn, err)
		}
		prefixes, err := originPrefixes(raw, asn)
		if err != nil {
			return DestinationRules{}, fmt.Errorf("decode AS%s routes: %w", asn, err)
		}
		rules.Prefixes = append(rules.Prefixes, prefixes...)
	}
	rules.Domains = sortedUnique(rules.Domains)
	rules.Suffixes = sortedUnique(rules.Suffixes)
	rules.Prefixes = sortedUnique(rules.Prefixes)
	return rules, nil
}

func underDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func originPrefixes(raw []byte, asn string) ([]string, error) {
	var feed struct {
		Status string
		Data   struct {
			Resource string
			Counts   map[string]struct{ Originating *int }
			Prefixes map[string]struct{ Originating []string }
		}
	}
	if err := json.Unmarshal(raw, &feed); err != nil {
		return nil, err
	}
	if feed.Status != "ok" || feed.Data.Resource != asn {
		return nil, fmt.Errorf("unexpected RIS status or origin")
	}
	var prefixes []string
	for _, family := range []string{"v4", "v6"} {
		count := feed.Data.Counts[family].Originating
		values := feed.Data.Prefixes[family].Originating
		if count == nil || values == nil || *count != len(values) {
			return nil, fmt.Errorf("incomplete %s origin list", family)
		}
		for _, value := range values {
			p, err := netip.ParsePrefix(value)
			if err != nil || p.Addr().Is4() != (family == "v4") || !validStreamingPrefix(p) {
				return nil, fmt.Errorf("invalid public %s prefix", family)
			}
			prefixes = append(prefixes, p.String())
		}
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("empty origin list")
	}
	return prefixes, nil
}

var excludedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
}

func validStreamingPrefix(p netip.Prefix) bool {
	if p != p.Masked() || p.Bits() < 8 || !p.Addr().IsGlobalUnicast() {
		return false
	}
	if p.Addr().Is6() && (p.Bits() < 16 || !netip.MustParsePrefix("2000::/3").Contains(p.Addr())) {
		return false
	}
	for _, special := range excludedNetworks {
		if p.Overlaps(special) {
			return false
		}
	}
	return true
}
