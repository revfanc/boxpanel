package sysinfo

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// IPProbe is injectable; init can skip it altogether via --public-ip.
type IPProbe func(context.Context) (netip.Addr, error)

func DefaultIPProbe(ctx context.Context) (netip.Addr, error) {
	ips, err := defaultPublicIPs(ctx, false)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect public IP: %w; retry or supply --public-ip with the server IP", err)
	}
	if ips.IPv4 != "" {
		return netip.ParseAddr(ips.IPv4)
	}
	return netip.ParseAddr(ips.IPv6)
}

// collectIPs probes the supplied sources concurrently under one deadline.
func collectIPs(ctx context.Context, client HTTPDoer, sources []string, timeout time.Duration) map[netip.Addr]int {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := make(chan netip.Addr, len(sources))
	for _, source := range sources {
		go func(source string) {
			var ip netip.Addr
			defer func() { results <- ip }()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 65))
			if err != nil || len(body) > 64 {
				return
			}
			candidate, err := netip.ParseAddr(strings.TrimSpace(string(body)))
			if err == nil && candidate.Zone() == "" && candidate.IsGlobalUnicast() && !candidate.IsPrivate() {
				ip = candidate.Unmap()
			}
		}(source)
	}
	votes := make(map[netip.Addr]int)
collect:
	for range sources {
		select {
		case ip := <-results:
			if ip.IsValid() {
				votes[ip]++
			}
		case <-ctx.Done():
			break collect
		}
	}
	return votes
}

func majority(votes map[netip.Addr]int, ipv4 bool) (netip.Addr, int) {
	var winner netip.Addr
	count, total := 0, 0
	for ip, n := range votes {
		if ip.Is4() == ipv4 {
			total += n
			if n > count {
				winner, count = ip, n
			}
		}
	}
	if count > total/2 {
		return winner, total
	}
	return netip.Addr{}, total
}

func ProbePublicIP(ctx context.Context, client HTTPDoer, sources []string, timeout time.Duration) (netip.Addr, error) {
	ips, err := ProbePublicIPs(ctx, client, sources, timeout)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect public IP: %w; retry or supply --public-ip with the server IP", err)
	}
	if ips.IPv4 != "" {
		return netip.ParseAddr(ips.IPv4)
	}
	return netip.ParseAddr(ips.IPv6)
}

type PublicIPs struct {
	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
}

type PublicIPProbe func(context.Context) (PublicIPs, error)

// ProbePublicIPs votes independently for each family; a failed family does not
// hide a usable address in the other family.
func ProbePublicIPs(ctx context.Context, client HTTPDoer, sources []string, timeout time.Duration) (PublicIPs, error) {
	votes := collectIPs(ctx, client, sources, timeout)
	var result PublicIPs
	if ip, _ := majority(votes, true); ip.IsValid() {
		result.IPv4 = ip.String()
	}
	if ip, _ := majority(votes, false); ip.IsValid() {
		result.IPv6 = ip.String()
	}
	if result.IPv4 == "" && result.IPv6 == "" {
		return result, fmt.Errorf("detect public IPs: sources failed or disagreed; check outbound HTTPS and retry")
	}
	return result, nil
}

func DefaultPublicIPs(ctx context.Context) (PublicIPs, error) {
	return defaultPublicIPs(ctx, true)
}

func defaultPublicIPs(ctx context.Context, both bool) (PublicIPs, error) {
	dial := (&net.Dialer{}).DialContext
	ipv4, ipv6 := ipClient("tcp4", dial), ipClient("tcp6", dial)
	defer ipv4.CloseIdleConnections()
	defer ipv6.CloseIdleConnections()
	return probeIPFamilies(ctx, ipv4, ipv6, 3*time.Second, both)
}

// No environment proxy or redirect: the result must describe this server's
// egress on the requested family. Dial injection keeps transport tests offline.
func ipClient(network string, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		return dial(ctx, network, address)
	}}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// IPv4 always runs first. Init stops on an agreed IPv4 address; diagnostics also
// collect IPv6 for display, DNS validation and port probes. Reserve at least
// half the remaining deadline for IPv6 so IPv4 timeouts cannot starve fallback.
func probeIPFamilies(ctx context.Context, ipv4, ipv6 HTTPDoer, timeout time.Duration, both bool) (PublicIPs, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result PublicIPs
	deadline, _ := ctx.Deadline()
	if ctx.Err() == nil {
		ips, _ := ProbePublicIPs(ctx, ipv4, []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://ifconfig.me/ip"}, time.Until(deadline)/2)
		result.IPv4 = ips.IPv4
	}
	if (both || result.IPv4 == "") && ctx.Err() == nil {
		ips, _ := ProbePublicIPs(ctx, ipv6, []string{"https://api6.ipify.org", "https://ipv6.icanhazip.com", "https://ifconfig.me/ip"}, time.Until(deadline))
		result.IPv6 = ips.IPv6
	}
	if result.IPv4 == "" && result.IPv6 == "" {
		return result, fmt.Errorf("detect public IPs: check outbound HTTPS and retry")
	}
	return result, nil
}
