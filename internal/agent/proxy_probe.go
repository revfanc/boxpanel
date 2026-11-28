package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/exitlink"
)

var ErrProxyTest = errors.New("external proxy test failed")

// Only fixed HTTPS echo services are queried. Do not propagate transport
// errors: they can include the proxy URL and its credentials.
func TestProxy(ctx context.Context, p exitlink.Proxy) (netip.Addr, error) {
	return probeProxy(ctx, p, &http.Transport{DialContext: dialPublicProxy}, []string{"https://api.ipify.org", "https://icanhazip.com"})
}

func probeProxy(ctx context.Context, p exitlink.Proxy, tr *http.Transport, targets []string) (netip.Addr, error) {
	if err := p.Validate(); err != nil {
		return netip.Addr{}, ErrProxyTest
	}
	u := url.URL{Scheme: p.Type, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	tr.Proxy = http.ProxyURL(&u)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, target := range targets {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return netip.Addr{}, ErrProxyTest
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 65))
		resp.Body.Close()
		ip, parseErr := netip.ParseAddr(strings.TrimSpace(string(body)))
		if err == nil && parseErr == nil && len(body) <= 64 && resp.StatusCode == http.StatusOK && ValidPublicIP(ip) {
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, ErrProxyTest
}

// Dial the checked address directly so a second DNS lookup cannot redirect
// the connection (and its proxy credentials) to a local service.
func dialPublicProxy(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrProxyTest
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, ErrProxyTest
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	for _, ip := range ips {
		if !ValidPublicIP(ip) {
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, ErrProxyTest
}
