package reality

import (
	"context"
	"net/netip"
)

// CloudflareHint explains why a Cloudflare-hosted target is refused. Reality
// forwards every unauthenticated connection to its target unchanged, and any
// Cloudflare edge serves every Cloudflare customer by SNI. A Cloudflare target
// therefore lets strangers use this server as a "preferred IP" relay into
// Cloudflare at the owner's bandwidth.
const CloudflareHint = "该目标托管在 Cloudflare，会让本机被他人当作优选 IP 中转流量。请改用不在 Cloudflare 上、与服务器同地区且证书有效的网站。"

// Published at https://www.cloudflare.com/ips-v4/ and /ips-v6/ (checked 2026-11-26).
var cloudflarePrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, prefix := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(prefix))
	}
	return out
}()

// Cloudflare reports whether ip belongs to Cloudflare's anycast network.
func Cloudflare(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range cloudflarePrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// HostedOnCloudflare resolves server (a DNS name or IP literal) and reports
// whether any of its addresses is a Cloudflare edge.
func HostedOnCloudflare(ctx context.Context, lookup func(context.Context, string) ([]netip.Addr, error), server string) (bool, error) {
	ips := []netip.Addr{}
	if ip, err := netip.ParseAddr(server); err == nil {
		ips = append(ips, ip)
	} else {
		resolved, err := lookup(ctx, server)
		if err != nil {
			return false, err
		}
		ips = resolved
	}
	for _, ip := range ips {
		if Cloudflare(ip) {
			return true, nil
		}
	}
	return false, nil
}
