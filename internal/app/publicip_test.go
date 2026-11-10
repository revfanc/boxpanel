package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicIPProbe(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bodies []string
		want   string
	}{
		{"majority", []string{"203.0.113.1", "203.0.113.2", "203.0.113.1\n"}, "203.0.113.1"},
		{"disagreement", []string{"203.0.113.1", "203.0.113.2"}, ""},
		{"invalid sources", []string{"not an IP", "127.0.0.1", "192.168.1.1", "::", "fe80::1", "224.0.0.1", strings.Repeat("x", 65)}, ""},
		{"partial success", []string{"203.0.113.1", "invalid"}, "203.0.113.1"},
		{"ipv6 only", []string{"2001:db8::1", "2001:db8::1"}, "2001:db8::1"},
		{"ipv4 preferred", []string{"2001:db8::1", "203.0.113.1", "2001:db8::1"}, "203.0.113.1"},
		{"ipv4 conflict fallback", []string{"203.0.113.1", "203.0.113.2", "2001:db8::1"}, "2001:db8::1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sources := []string{}
			bodies := map[string]string{}
			for i, body := range tt.bodies {
				source := "https://source/" + string(rune('a'+i))
				sources = append(sources, source)
				bodies[source] = body
			}
			client := doerFunc(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("missing request deadline")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(bodies[r.URL.String()]))}, nil
			})
			got, err := ProbePublicIP(context.Background(), client, sources, time.Second)
			if tt.want == "" {
				if err == nil {
					t.Fatal("expected failure")
				}
			} else if err != nil || got.String() != tt.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestProbeTimeoutAndStatus(t *testing.T) {
	client := doerFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	start := time.Now()
	if _, err := ProbePublicIP(context.Background(), client, []string{"https://one", "https://two"}, 20*time.Millisecond); err == nil {
		t.Fatal("timeout accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("probes are not deadline bounded")
	}
	client = doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("203.0.113.1"))}, nil
	})
	if _, err := ProbePublicIP(context.Background(), client, []string{"https://one"}, time.Second); err == nil {
		t.Fatal("accepted failed HTTP response")
	}
}
