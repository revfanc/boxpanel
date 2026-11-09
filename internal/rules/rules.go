package rules

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
)

var domainLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func ValidHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func HTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !ValidHost(u.Hostname()) || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\r\n\\") {
		return nil, fmt.Errorf("use an HTTPS URL without credentials or fragment")
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("use a port between 1 and 65535")
		}
	}
	return u, nil
}

func Interval(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || n < 1 || n > 30 {
			return 0, fmt.Errorf("use an interval between 1h and 30d")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Hour || d > 30*24*time.Hour || d%time.Second != 0 {
		return 0, fmt.Errorf("use an interval between 1h and 30d")
	}
	return d, nil
}

func Validate(s db.RulesSettings) error {
	if s.Preset != "bypass_cn" && s.Preset != "gfw_only" && s.Preset != "global" {
		return fmt.Errorf("preset: select bypass_cn, gfw_only or global")
	}
	if s.Source != "metacubex" && s.Source != "sagernet" {
		return fmt.Errorf("source: select metacubex or sagernet")
	}
	if _, err := Interval(s.UpdateInterval); err != nil {
		return fmt.Errorf("update_interval: %w", err)
	}
	if s.Mirror != "" {
		if _, err := HTTPS(s.Mirror); err != nil || !strings.HasSuffix(s.Mirror, "/") || strings.Contains(s.Mirror, "?") {
			return fmt.Errorf("mirror: use an HTTPS prefix ending in / without a query")
		}
	}
	for _, raw := range []string{s.DNS.Local, s.DNS.Remote} {
		if _, err := HTTPS(raw); err != nil {
			return fmt.Errorf("dns: %w", err)
		}
	}
	for _, list := range [][]string{s.CustomDirect, s.CustomProxy} {
		if len(list) > 128 {
			return fmt.Errorf("custom domains: use at most 128 suffixes per list")
		}
		for _, domain := range list {
			if _, err := netip.ParseAddr(domain); err == nil || !ValidHost(domain) {
				return fmt.Errorf("custom domains: use DNS suffixes without wildcards, ports or URLs")
			}
		}
	}
	return nil
}

type Rule struct{ Kind, Value, Target string }
type Plan struct {
	Rules []Rule
	Tags  []string
	Final string
}

// Build shares rule precedence across formats. Custom direct wins ties;
// custom proxy precedes the preset's country rules.
func Build(s db.RulesSettings) (Plan, error) {
	if err := Validate(s); err != nil {
		return Plan{}, err
	}
	p := Plan{Final: "proxy"}
	addSet := func(tag, target string) {
		p.Rules = append(p.Rules, Rule{"set", tag, target})
		p.Tags = append(p.Tags, tag)
	}
	if s.BlockAds {
		addSet("geosite-ads", "reject")
	}
	p.Rules = append(p.Rules, Rule{"private", "", "direct"})
	for _, d := range s.CustomDirect {
		p.Rules = append(p.Rules, Rule{"domain", d, "direct"})
	}
	for _, d := range s.CustomProxy {
		p.Rules = append(p.Rules, Rule{"domain", d, "proxy"})
	}
	switch s.Preset {
	case "bypass_cn":
		addSet("geosite-cn", "direct")
		addSet("geoip-cn", "direct")
	case "gfw_only":
		addSet("geosite-gfw", "proxy")
		p.Final = "direct"
	}
	return p, nil
}
