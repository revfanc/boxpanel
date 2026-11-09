package sysinfo

import (
	"context"
	"io"
	"net/http"
	"strings"
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
