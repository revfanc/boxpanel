// Package rules defines client routing policy and versioned remote rule sources.
package rules

import "fmt"

const (
	MetaSRSBase   = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/"
	MetaMRSBase   = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/"
	SagerSiteBase = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"
	SagerIPBase   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/"
	MetaSRCN      = MetaSRSBase + "geosite/cn.srs"
	MetaSRIPCN    = MetaSRSBase + "geoip/cn.srs"
	MetaSRGFW     = MetaSRSBase + "geosite/gfw.srs"
	MetaSRNonCN   = MetaSRSBase + "geosite/geolocation-!cn.srs"
	MetaSRAds     = MetaSRSBase + "geosite/category-ads-all.srs"
	MetaMRCN      = MetaMRSBase + "geosite/cn.mrs"
	MetaMRIPCN    = MetaMRSBase + "geoip/cn.mrs"
	MetaMRGFW     = MetaMRSBase + "geosite/gfw.mrs"
	MetaMRNonCN   = MetaMRSBase + "geosite/geolocation-!cn.mrs"
	MetaMRAds     = MetaMRSBase + "geosite/category-ads-all.mrs"
	SagerCN       = SagerSiteBase + "geosite-cn.srs"
	SagerIPCN     = SagerIPBase + "geoip-cn.srs"
	// SagerNet does not publish geosite-gfw.srs (verified HTTP 404). Keep
	// gfw_only semantics using MetaCubeX instead of substituting non-CN.
	SagerGFW   = MetaSRGFW
	SagerNonCN = SagerSiteBase + "geosite-geolocation-!cn.srs"
	SagerAds   = SagerSiteBase + "geosite-category-ads-all.srs"
)

type Source struct{ Tag, Behavior, MetaSRS, MetaMRS, SagerSRS string }

// Sources returns a copy, in a stable order. Tests never fetch these URLs.
func Sources() []Source {
	return []Source{
		{"geosite-cn", "domain", MetaSRCN, MetaMRCN, SagerCN},
		{"geoip-cn", "ipcidr", MetaSRIPCN, MetaMRIPCN, SagerIPCN},
		{"geosite-gfw", "domain", MetaSRGFW, MetaMRGFW, SagerGFW},
		{"geosite-geolocation-!cn", "domain", MetaSRNonCN, MetaMRNonCN, SagerNonCN},
		{"geosite-ads", "domain", MetaSRAds, MetaMRAds, SagerAds},
	}
}

// Mirror is deliberately injectable; the built-in policy is HTTPS prefix + URL.
type Mirror func(prefix, original string) string

func PrefixMirror(prefix, original string) string { return prefix + original }
func URL(tag, source, format, mirror string, rewrite Mirror) (string, error) {
	for _, s := range Sources() {
		if s.Tag != tag {
			continue
		}
		u := s.MetaSRS
		if source == "sagernet" {
			u = s.SagerSRS
		}
		if format == "clash" {
			u = s.MetaMRS
		}
		if rewrite == nil {
			rewrite = PrefixMirror
		}
		return rewrite(mirror, u), nil
	}
	return "", fmt.Errorf("resolve rule source: unknown tag; select a supported preset")
}
