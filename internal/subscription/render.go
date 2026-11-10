package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/share"
	"github.com/revfanc/boxpanel/web"
	"gopkg.in/yaml.v3"
)

type Snapshot struct {
	Nodes []share.Node
	Rules db.RulesSettings
}
type object = map[string]any

func Render(s Snapshot, format Format) ([]byte, error) {
	p, err := rules.Build(s.Rules)
	if err != nil {
		return nil, fmt.Errorf("render subscription: %w", err)
	}
	switch format {
	case Singbox:
		value, err := singboxConfig(s, p)
		if err != nil {
			return nil, err
		}
		b, err := json.MarshalIndent(value, "", "  ")
		return append(b, '\n'), err
	case Clash:
		var b bytes.Buffer
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		if err := enc.Encode(clashConfig(s, p)); err != nil {
			return nil, fmt.Errorf("encode mihomo subscription: %w", err)
		}
		return b.Bytes(), nil
	case Base64:
		lines := []string{}
		for _, n := range s.Nodes {
			u, err := share.URI(n)
			if err != nil {
				return nil, err
			}
			lines = append(lines, u)
		}
		raw := strings.Join(lines, "\n")
		if len(lines) > 0 {
			raw += "\n"
		}
		return []byte(base64.StdEncoding.EncodeToString([]byte(raw)) + "\n"), nil
	default:
		return nil, fmt.Errorf("render subscription: unsupported format")
	}
}

func dnsServer(raw, tag string, remote bool) object {
	u, _ := rules.HTTPS(raw) // validated by rules.Build
	o := object{"type": "https", "tag": tag, "server": u.Hostname()}
	if u.Port() != "" {
		p, _ := strconv.Atoi(u.Port())
		o["server_port"] = p
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/dns-query"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	o["path"] = path
	if _, err := netip.ParseAddr(u.Hostname()); err != nil {
		o["domain_resolver"] = "dns-bootstrap"
	}
	if remote {
		o["detour"] = "proxy"
	}
	return o
}

func singboxConfig(s Snapshot, p rules.Plan) (object, error) {
	names := []string{}
	for _, n := range s.Nodes {
		names = append(names, n.Name)
	}
	// Empty subscriptions stay valid without silently giving proxy traffic a
	// direct route. The private-IP exception still works; other traffic rejects.
	// One manual selector listing the nodes, first node by default. No auto
	// group (clients cannot hide it) and no direct choice (clients have a
	// direct mode of their own).
	choices := []string{"direct"}
	if len(names) > 0 {
		choices = names
	}
	out := []object{{"type": "selector", "tag": "proxy", "outbounds": choices, "default": choices[0]}}
	for _, n := range s.Nodes {
		o := object{"tag": n.Name, "server": n.Host, "server_port": n.Port}
		if v := n.Reality; v != nil {
			o["type"] = "vless"
			o["uuid"] = n.UUID
			o["flow"] = "xtls-rprx-vision"
			o["tls"] = object{"enabled": true, "server_name": v.ServerName, "utls": object{"enabled": true, "fingerprint": v.Fingerprint}, "reality": object{"enabled": true, "public_key": v.PublicKey, "short_id": v.ShortID}}
		} else if h := n.Hysteria2; h != nil {
			if n.Certificate == "" {
				return nil, fmt.Errorf("render Hysteria2: missing certificate; apply the inbound first")
			}
			o["type"] = "hysteria2"
			o["password"] = n.Password
			o["tls"] = object{"enabled": true, "server_name": h.SNI, "alpn": []string{"h3"}, "certificate": n.Certificate}
			if h.UpMbps > 0 {
				o["up_mbps"] = h.UpMbps
			}
			if h.DownMbps > 0 {
				o["down_mbps"] = h.DownMbps
			}
			if h.ObfsPassword != "" {
				o["obfs"] = object{"type": "salamander", "password": h.ObfsPassword}
			}
			if h.PortHopping.Enabled {
				delete(o, "server_port")
				o["server_ports"] = []string{fmt.Sprintf("%d:%d", h.PortHopping.Start, h.PortHopping.End)}
			}
		} else {
			return nil, fmt.Errorf("render node: unsupported protocol")
		}
		out = append(out, o)
	}
	out = append(out, object{"type": "direct", "tag": "direct"})
	routeRules := []object{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}
	dnsRules := []object{}
	if !s.Rules.IPv6 {
		dnsRules = append(dnsRules, object{"query_type": []string{"AAAA"}, "action": "predefined", "rcode": "NOERROR"})
	}
	for _, r := range p.Rules {
		if r.Value == "geoip-cn" && len(s.Nodes) > 0 {
			// Domain destinations (including restored FakeIP names) do not
			// acquire destination IPs just by matching an IP rule-set. Resolve
			// only after all explicit domain decisions, with a real resolver.
			routeRules = append(routeRules, object{"action": "resolve", "server": "dns-remote"}, object{"ip_is_private": true, "action": "route", "outbound": "direct"})
		}
		o := object{"action": "route", "outbound": r.Target}
		if r.Target == "reject" || r.Target == "proxy" && len(s.Nodes) == 0 {
			o = object{"action": "reject"}
		}
		switch r.Kind {
		case "private":
			o["ip_is_private"] = true
		case "domain":
			o["domain_suffix"] = []string{r.Value}
		case "set":
			o["rule_set"] = []string{r.Value}
		}
		routeRules = append(routeRules, o)
		if r.Kind == "private" || r.Value == "geoip-cn" {
			continue
		}
		d := object{"action": "route", "server": "dns-local"}
		if r.Target == "proxy" {
			d["server"] = "dns-remote"
		}
		if r.Target == "reject" {
			d = object{"action": "reject"}
		}
		if r.Kind == "domain" {
			d["domain_suffix"] = []string{r.Value}
		} else {
			d["rule_set"] = []string{r.Value}
		}
		// A/AAAA use FakeIP; other query types must still use the proxy's
		// real resolver, including in gfw_only where the final DNS is local.
		if s.Rules.DNS.FakeIP && r.Target == "proxy" {
			fake := object{}
			for k, v := range d {
				fake[k] = v
			}
			fake["server"] = "dns-fakeip"
			fake["query_type"] = []string{"A", "AAAA"}
			dnsRules = append(dnsRules, fake)
		}
		dnsRules = append(dnsRules, d)
	}
	if len(s.Nodes) == 0 {
		routeRules = append(routeRules, object{"action": "reject"})
	}
	sets := []object{}
	for _, tag := range p.Tags {
		u, _ := rules.URL(tag, s.Rules.Source, "singbox", s.Rules.Mirror, nil)
		sets = append(sets, object{"type": "remote", "tag": tag, "format": "binary", "url": u, "http_client": object{"detour": "proxy"}, "update_interval": s.Rules.UpdateInterval})
	}
	servers := []object{dnsServer(s.Rules.DNS.Remote, "dns-remote", true), dnsServer(s.Rules.DNS.Local, "dns-local", false), {"type": "local", "tag": "dns-bootstrap"}}
	if s.Rules.DNS.FakeIP {
		f := object{"type": "fakeip", "tag": "dns-fakeip", "inet4_range": "198.18.0.0/15"}
		if s.Rules.IPv6 {
			f["inet6_range"] = "2001:2::/48"
		}
		servers = append(servers, f)
		if p.Final == "proxy" {
			dnsRules = append(dnsRules, object{"query_type": []string{"A", "AAAA"}, "action": "route", "server": "dns-fakeip"})
		}
	}
	dnsFinal := "dns-remote"
	if p.Final == "direct" {
		dnsFinal = "dns-local"
	}
	addresses := []string{"172.19.0.1/30"}
	if s.Rules.IPv6 {
		addresses = append(addresses, "fdfe:dead:beef::1/126")
	}
	return object{"log": object{"level": "warn"}, "dns": object{"servers": servers, "rules": dnsRules, "final": dnsFinal}, "inbounds": []object{{"type": "tun", "tag": "tun-in", "address": addresses, "auto_route": true, "strict_route": true}, {"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080}}, "outbounds": out, "route": object{"rules": routeRules, "rule_set": sets, "final": p.Final, "auto_detect_interface": true, "default_domain_resolver": "dns-local"}, "experimental": object{"cache_file": object{"enabled": true, "store_fakeip": s.Rules.DNS.FakeIP}}}, nil
}

func clashConfig(s Snapshot, p rules.Plan) object {
	text := web.SubscriptionText()
	selectName := text["select"]
	names := []string{}
	proxies := []object{}
	for _, n := range s.Nodes {
		names = append(names, n.Name)
		o := object{"name": n.Name, "server": n.Host, "port": n.Port}
		if v := n.Reality; v != nil {
			o["type"] = "vless"
			o["uuid"] = n.UUID
			o["flow"] = "xtls-rprx-vision"
			o["network"] = "tcp"
			o["tls"] = true
			o["udp"] = true
			o["packet-encoding"] = "xudp"
			o["servername"] = v.ServerName
			o["client-fingerprint"] = v.Fingerprint
			o["reality-opts"] = object{"public-key": v.PublicKey, "short-id": v.ShortID}
		}
		if h := n.Hysteria2; h != nil {
			o["type"] = "hysteria2"
			o["password"] = n.Password
			o["sni"] = h.SNI
			o["skip-cert-verify"] = false
			o["fingerprint"] = n.Fingerprint
			o["alpn"] = []string{"h3"}
			if h.UpMbps > 0 {
				o["up"] = h.UpMbps
			}
			if h.DownMbps > 0 {
				o["down"] = h.DownMbps
			}
			if h.ObfsPassword != "" {
				o["obfs"] = "salamander"
				o["obfs-password"] = h.ObfsPassword
			}
			if h.PortHopping.Enabled {
				o["ports"] = fmt.Sprintf("%d-%d", h.PortHopping.Start, h.PortHopping.End)
			}
		}
		proxies = append(proxies, o)
	}
	target := func(t string) string {
		switch t {
		case "direct":
			return "DIRECT"
		case "reject":
			return "REJECT"
		}
		return selectName
	}
	lines := []string{}
	policy := object{}
	localDNS := []string{s.Rules.DNS.Local}
	if s.Rules.DNS.Local == "https://223.5.5.5/dns-query" {
		localDNS = append(localDNS, "https://doh.pub/dns-query")
	}
	remoteDNS := []string{s.Rules.DNS.Remote}
	// DNS transports follow these rules (respect-rules in mihomo, follow-rule
	// in Stash). The remote endpoint comes first even in gfw_only or when a
	// custom direct suffix also covers it. Node DNS has its own resolver chain.
	dnsTarget := selectName
	if len(s.Nodes) == 0 {
		dnsTarget = "REJECT"
	}
	lines = append(lines, clashDNSRule(s.Rules.DNS.Remote, dnsTarget))
	seenDNSHosts := map[string]bool{}
	remoteURL, _ := rules.HTTPS(s.Rules.DNS.Remote)
	seenDNSHosts[strings.ToLower(remoteURL.Hostname())] = true
	for _, resolver := range append([]string{"https://223.5.5.5/dns-query"}, localDNS...) {
		u, _ := rules.HTTPS(resolver)
		host := strings.ToLower(u.Hostname())
		if !seenDNSHosts[host] {
			lines = append(lines, clashDNSRule(resolver, "DIRECT"))
			seenDNSHosts[host] = true
		}
	}
	realIPExceptions := []string{"time.*.com", "ntp.*.com", "+.pool.ntp.org", "+.msftconnecttest.com", "+.msftncsi.com", "captive.apple.com"}
	fakeIPFilter := append([]string{"*.lan", "*.local", "localhost.ptlink", "localhost.ptlogin2.qq.com"}, realIPExceptions...)
	tags := append([]string{}, p.Tags...)
	if p.Final == "proxy" {
		tags = append(tags, "geosite-geolocation-!cn")
		policy["rule-set:geosite-geolocation-!cn"] = remoteDNS
	}
	for _, r := range p.Rules {
		t := target(r.Target)
		if r.Target == "proxy" && len(s.Nodes) == 0 {
			t = "REJECT"
		}
		switch r.Kind {
		case "private":
			for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
				lines = append(lines, "IP-CIDR,"+cidr+",DIRECT,no-resolve")
			}
			for _, cidr := range []string{"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"} {
				lines = append(lines, "IP-CIDR6,"+cidr+",DIRECT,no-resolve")
			}
		case "domain":
			lines = append(lines, "DOMAIN-SUFFIX,"+r.Value+","+t)
			key := "+." + r.Value
			if _, exists := policy[key]; !exists {
				resolvers := localDNS
				if r.Target == "proxy" {
					resolvers = remoteDNS
				}
				policy[key] = resolvers
			}
		case "set":
			line := "RULE-SET," + r.Value + "," + t
			if r.Value == "geoip-cn" {
				line += ",no-resolve"
			}
			lines = append(lines, line)
			if r.Value == "geosite-gfw" {
				policy["rule-set:"+r.Value] = remoteDNS
			}
			if r.Value == "geosite-cn" {
				policy["rule-set:"+r.Value] = localDNS
				// Supported by mihomo and current Stash; only reference sets
				// present in this preset, so global keeps proxying CN domains.
				fakeIPFilter = append(fakeIPFilter, "rule-set:geosite-cn")
			}
		}
	}
	if p.Final == "proxy" {
		// Protect real-IP exceptions, while keeping explicit custom policies.
		for _, domain := range realIPExceptions {
			if _, exists := policy[domain]; !exists {
				policy[domain] = remoteDNS
			}
		}
	}
	if len(s.Nodes) == 0 {
		lines = append(lines, "MATCH,REJECT")
	} else {
		lines = append(lines, "MATCH,"+target(p.Final))
	}
	providers := object{}
	interval, _ := rules.Interval(s.Rules.UpdateInterval)
	for _, tag := range tags {
		u, _ := rules.URL(tag, s.Rules.Source, "clash", s.Rules.Mirror, nil)
		behavior := "domain"
		if tag == "geoip-cn" {
			behavior = "ipcidr"
		}
		providers[tag] = object{"type": "http", "behavior": behavior, "format": "yaml", "url": u, "interval": int(interval.Seconds()), "proxy": selectName}
	}
	choices := names
	if len(choices) == 0 {
		choices = []string{"REJECT"}
	}
	// A single manual group: the user taps a node to use it.
	groups := []object{{"name": selectName, "type": "select", "proxies": choices}}
	return object{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning", "ipv6": false,
		"dns": object{
			"enable": true, "ipv6": false, "enhanced-mode": "fake-ip",
			"respect-rules": true, "follow-rule": true,
			"fake-ip-range": "198.18.0.1/16", "fake-ip-filter": fakeIPFilter,
			"default-nameserver": []string{"https://223.5.5.5/dns-query"},
			"nameserver":         localDNS, "nameserver-policy": policy, "proxy-server-nameserver": localDNS,
		},
		"proxies": proxies, "proxy-groups": groups,
		"rule-providers": providers, "rules": lines,
	}
}

func clashDNSRule(raw, target string) string {
	u, _ := rules.HTTPS(raw) // validated by rules.Build
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		kind, bits := "IP-CIDR", "/32"
		if ip.Is6() {
			kind, bits = "IP-CIDR6", "/128"
		}
		return kind + "," + host + bits + "," + target + ",no-resolve"
	}
	return "DOMAIN," + host + "," + target
}
