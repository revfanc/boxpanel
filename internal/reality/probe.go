package reality

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CandidateDomains is immutable. These are candidates, never presumed healthy:
// every recommendation requires a fresh verified TLS handshake from the server.
const CandidateDomains = "www.microsoft.com,www.apple.com,www.cloudflare.com,www.bing.com,www.amazon.com"
const ProbeTimeout = 3 * time.Second

type Target struct {
	Server    string `json:"server"`
	TLS13     bool   `json:"tls13"`
	H2        bool   `json:"h2"`
	LatencyMS int64  `json:"latency_ms"`
	OK        bool   `json:"ok"`
	Hint      string `json:"hint,omitempty"`
}

type Probe func(context.Context, string, int) Target

// ProbeOptions isolates DNS and the TLS exchange so tests need no network.
// Handshake must verify the certificate chain and ServerName before returning.
type ProbeOptions struct {
	Lookup    func(context.Context, string) ([]netip.Addr, error)
	Handshake func(context.Context, string, string) (tls.ConnectionState, error)
	Now       func() time.Time
}

func NewProbe(opts ProbeOptions) Probe {
	if opts.Lookup == nil {
		opts.Lookup = func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		}
	}
	if opts.Handshake == nil {
		opts.Handshake = func(ctx context.Context, address, server string) (tls.ConnectionState, error) {
			dialer := tls.Dialer{Config: &tls.Config{ServerName: server, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}}
			conn, err := dialer.DialContext(ctx, "tcp", address)
			if err != nil {
				return tls.ConnectionState{}, fmt.Errorf("probe TLS: %w", err)
			}
			defer conn.Close()
			return conn.(*tls.Conn).ConnectionState(), nil
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return func(ctx context.Context, server string, port int) Target {
		result := Target{Server: server, Hint: "请检查服务器网络和目标证书，确认支持 TLS 1.3 与 HTTP/2 后重试或更换目标。"}
		if !ValidServer(server) || port < 1 || port > 65535 {
			return result
		}
		ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
		defer cancel()
		ips, err := opts.Lookup(ctx, server)
		if err != nil || len(ips) == 0 {
			return result
		}
		// Reject the entire answer if it contains internal addresses. Dial the
		// validated IP directly, preserving SNI, to prevent DNS rebinding.
		for _, ip := range ips {
			if !PublicIP(ip) {
				return result
			}
		}
		start := opts.Now()
		for _, ip := range ips {
			state, err := opts.Handshake(ctx, net.JoinHostPort(ip.String(), strconv.Itoa(port)), server)
			if err != nil || ctx.Err() != nil {
				continue
			}
			result.LatencyMS = max(0, opts.Now().Sub(start).Milliseconds())
			result.TLS13 = state.Version == tls.VersionTLS13
			result.H2 = state.NegotiatedProtocol == "h2"
			result.OK = result.TLS13 && result.H2 && len(state.VerifiedChains) > 0
			if result.OK {
				result.Hint = ""
			}
			return result
		}
		return result
	}
}

func Recommend(ctx context.Context, probe Probe) []Target {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	names := strings.Split(CandidateDomains, ",")
	results := make([]Target, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) { defer wg.Done(); results[i] = probe(ctx, name, 443) }(i, name)
	}
	wg.Wait()
	good := []Target{}
	for _, result := range results {
		if result.OK && result.TLS13 && result.H2 {
			good = append(good, result)
		}
	}
	sort.SliceStable(good, func(i, j int) bool { return good[i].LatencyMS < good[j].LatencyMS })
	return good[:min(3, len(good))]
}

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

// ValidServer accepts an ASCII DNS name or a public IP, never URLs/host:port.
func ValidServer(server string) bool {
	if ip, err := netip.ParseAddr(server); err == nil {
		return PublicIP(ip)
	}
	if len(server) == 0 || len(server) > 253 || !strings.Contains(server, ".") {
		return false
	}
	for _, label := range strings.Split(server, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
