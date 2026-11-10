package sysinfo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type httpDoer func(*http.Request) (*http.Response, error)

func (f httpDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestDualStackProbe(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bodies []string
		want   PublicIPs
	}{
		{"both", []string{"203.0.113.1", "203.0.113.1", "203.0.113.2", "2001:db8::1", "2001:db8::1"}, PublicIPs{IPv4: "203.0.113.1", IPv6: "2001:db8::1"}},
		{"IPv4 conflict", []string{"203.0.113.1", "203.0.113.2", "2001:db8::1"}, PublicIPs{IPv6: "2001:db8::1"}},
		{"IPv6 conflict", []string{"2001:db8::1", "2001:db8::2", "203.0.113.1"}, PublicIPs{IPv4: "203.0.113.1"}},
		{"mapped address", []string{"::ffff:203.0.113.1", "127.0.0.1", "fd00::1"}, PublicIPs{IPv4: "203.0.113.1"}},
		{"invalid", []string{"192.168.1.1", "::1", "fe80::1%eth0", "not an IP", strings.Repeat("x", 65)}, PublicIPs{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sources := []string{}
			bodies := map[string]string{}
			for i, body := range tt.bodies {
				url := "https://source/" + string(rune('a'+i))
				sources = append(sources, url)
				bodies[url] = body
			}
			client := httpDoer(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("missing deadline")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(bodies[r.URL.String()]))}, nil
			})
			got, err := ProbePublicIPs(context.Background(), client, sources, time.Second)
			if got != tt.want || (err != nil) != (tt.want == (PublicIPs{})) {
				t.Fatalf("ips=%+v %v", got, err)
			}
		})
	}
}

func TestPublicIPCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := httpDoer(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	start := time.Now()
	if _, err := ProbePublicIPs(ctx, client, []string{"https://one", "https://two"}, time.Second); err == nil {
		t.Fatal("cancellation accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe ignored cancellation")
	}
}

func TestIPv4FirstDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name, v4, v6 string
		both         bool
		want         PublicIPs
		wantV6       bool
	}{
		{"dual stack init", "ok", "ok", false, PublicIPs{IPv4: "203.0.113.1"}, false},
		{"IPv6 only init", "failed", "ok", false, PublicIPs{IPv6: "2001:db8::1"}, true},
		{"IPv4 timeout fallback", "timeout", "ok", false, PublicIPs{IPv6: "2001:db8::1"}, true},
		{"IPv4 conflict fallback", "conflict", "ok", false, PublicIPs{IPv6: "2001:db8::1"}, true},
		{"wrong family cannot win", "wrong", "ok", false, PublicIPs{IPv6: "2001:db8::1"}, true},
		{"dual stack diagnostics", "ok", "ok", true, PublicIPs{IPv4: "203.0.113.1", IPv6: "2001:db8::1"}, true},
		{"IPv4 only diagnostics", "ok", "failed", true, PublicIPs{IPv4: "203.0.113.1"}, true},
		{"IPv4 timeout diagnostics", "timeout", "ok", true, PublicIPs{IPv6: "2001:db8::1"}, true},
		{"wrong IPv6 response", "failed", "wrong", true, PublicIPs{}, true},
		{"all unavailable", "failed", "failed", false, PublicIPs{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls4, calls6 atomic.Int32
			client := func(v4 bool, mode string) HTTPDoer {
				return httpDoer(func(r *http.Request) (*http.Response, error) {
					var sources []string
					if v4 {
						calls4.Add(1)
						sources = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://ifconfig.me/ip"}
					} else {
						calls6.Add(1)
						if calls4.Load() != 3 {
							t.Error("IPv6 started before IPv4 sources were attempted")
						}
						sources = []string{"https://api6.ipify.org", "https://ipv6.icanhazip.com", "https://ifconfig.me/ip"}
					}
					index := -1
					for i, source := range sources {
						if r.URL.String() == source {
							index = i
						}
					}
					if index < 0 {
						t.Errorf("unexpected source: %s", r.URL)
					}
					if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) <= 0 {
						t.Error("no time left to probe family")
					}
					switch mode {
					case "timeout":
						<-r.Context().Done()
						return nil, r.Context().Err()
					case "failed":
						return nil, errors.New("offline")
					}
					body := "203.0.113.1"
					if v4 == (mode == "wrong") {
						body = "2001:db8::1"
					}
					if mode == "conflict" {
						body = "203.0.113." + string(rune('1'+index))
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
			}
			got, err := probeIPFamilies(context.Background(), client(true, tt.v4), client(false, tt.v6), 200*time.Millisecond, tt.both)
			if got != tt.want || (err != nil) != (tt.want == (PublicIPs{})) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
			if calls4.Load() != 3 || (calls6.Load() > 0) != tt.wantV6 {
				t.Fatalf("calls: v4=%d v6=%d", calls4.Load(), calls6.Load())
			}
		})
	}
}

func TestFamilyProbeReservesShortParentDeadlineAndCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ipv4 := httpDoer(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ipv6 := httpDoer(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			t.Error("parent deadline left no IPv6 fallback budget")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("2001:db8::1"))}, nil
	})
	ips, err := probeIPFamilies(ctx, ipv4, ipv6, 3*time.Second, false)
	if err != nil || ips.IPv6 != "2001:db8::1" {
		t.Fatal(ips, err)
	}
	cancel()
	never := httpDoer(func(*http.Request) (*http.Response, error) {
		t.Error("canceled probe issued HTTP request")
		return nil, errors.New("canceled")
	})
	if _, err := probeIPFamilies(ctx, never, never, time.Second, true); err == nil {
		t.Fatal("canceled discovery succeeded")
	}
}

func TestIPClientPinsFamilyWithoutProxyOrRedirect(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			var called atomic.Bool
			client := ipClient(network, func(_ context.Context, got, address string) (net.Conn, error) {
				called.Store(true)
				if got != network || address != "source.invalid:443" {
					t.Errorf("dial = %q %q", got, address)
				}
				return nil, errors.New("injected offline dialer")
			})
			defer client.CloseIdleConnections()
			if _, err := client.Get("https://source.invalid"); err == nil || !called.Load() {
				t.Fatal("injected dialer was not used")
			}
			if client.Transport.(*http.Transport).Proxy != nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
				t.Fatal("probe allows proxy or redirect")
			}
		})
	}
}
