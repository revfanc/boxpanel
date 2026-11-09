package app

import (
	"context"
	"fmt"
	"io"
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
func ProbePublicIP(ctx context.Context, client HTTPDoer, sources []string, timeout time.Duration) (netip.Addr, error) {
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
	for _, ipv4 := range []bool{true, false} {
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
		if total > 0 {
			if count > total/2 {
				return winner, nil
			}
			break
		}
	}
	return netip.Addr{}, fmt.Errorf("detect public IP: sources failed or disagreed; retry or supply --public-ip with the server IP")
}
