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
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	return ProbePublicIP(ctx, client, []string{"https://api64.ipify.org", "https://ifconfig.me/ip", "https://icanhazip.com"}, 3*time.Second)
}

// ProbePublicIP probes all sources concurrently under one deadline. Among valid
// replies it prefers IPv4, then requires a strict majority within that family.
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
	votes := collectIPs(ctx, client, sources, timeout)
	for _, ipv4 := range []bool{true, false} {
		winner, total := majority(votes, ipv4)
		if total > 0 {
			if winner.IsValid() {
				return winner, nil
			}
			break
		}
	}
	return netip.Addr{}, fmt.Errorf("detect public IP: sources failed or disagreed; retry or supply --public-ip with the server IP")
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
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	results := make(chan PublicIPs, 2)
	for _, network := range []string{"tcp4", "tcp6"} {
		go func(network string) {
			transport := &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			ips, _ := ProbePublicIPs(ctx, client, []string{"https://api64.ipify.org", "https://ifconfig.me/ip", "https://icanhazip.com"}, 3*time.Second)
			results <- ips
		}(network)
	}
	var result PublicIPs
	for range 2 {
		r := <-results
		if r.IPv4 != "" {
			result.IPv4 = r.IPv4
		}
		if r.IPv6 != "" {
			result.IPv6 = r.IPv6
		}
	}
	if result.IPv4 == "" && result.IPv6 == "" {
		return result, fmt.Errorf("detect public IPs: check outbound HTTPS and retry")
	}
	return result, nil
}
