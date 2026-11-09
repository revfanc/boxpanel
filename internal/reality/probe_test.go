package reality

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTLSProbeWithoutNetwork(t *testing.T) {
	verified := [][]*x509.Certificate{{{}}}
	for _, tt := range []struct {
		name          string
		state         tls.ConnectionState
		err           error
		ok, tls13, h2 bool
	}{
		{"valid", tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: "h2", VerifiedChains: verified}, nil, true, true, true},
		{"tls12", tls.ConnectionState{Version: tls.VersionTLS12, NegotiatedProtocol: "h2", VerifiedChains: verified}, nil, false, false, true},
		{"http1", tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: "http/1.1", VerifiedChains: verified}, nil, false, true, false},
		{"unverified", tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: "h2"}, nil, false, true, true},
		{"bad certificate", tls.ConnectionState{}, errors.New("certificate failed"), false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			probe := NewProbe(ProbeOptions{
				Lookup: func(ctx context.Context, server string) ([]netip.Addr, error) {
					if server != "www.example.com" {
						t.Fatal(server)
					}
					return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
				},
				Handshake: func(ctx context.Context, address, server string) (tls.ConnectionState, error) {
					if address != "1.1.1.1:8443" || server != "www.example.com" {
						t.Fatalf("wrong TLS target %s %s", address, server)
					}
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > ProbeTimeout {
						t.Fatal("missing timeout")
					}
					now = now.Add(12 * time.Millisecond)
					return tt.state, tt.err
				}, Now: func() time.Time { return now },
			})
			got := probe(context.Background(), "www.example.com", 8443)
			if got.OK != tt.ok || got.TLS13 != tt.tls13 || got.H2 != tt.h2 {
				t.Fatalf("target=%+v", got)
			}
			if tt.err == nil && got.LatencyMS != 12 {
				t.Fatalf("latency=%d", got.LatencyMS)
			}
		})
	}
}
func TestProbeRejectsPrivateDNSAndCancellation(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "fc00::1", "::ffff:127.0.0.1"} {
		probe := NewProbe(ProbeOptions{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr(ip)}, nil
			},
			Handshake: func(context.Context, string, string) (tls.ConnectionState, error) {
				t.Fatal("private DNS reached handshake")
				return tls.ConnectionState{}, nil
			},
		})
		if probe(context.Background(), "www.example.com", 443).OK {
			t.Fatal(ip)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := NewProbe(ProbeOptions{Lookup: func(ctx context.Context, _ string) ([]netip.Addr, error) { return nil, ctx.Err() }, Handshake: func(context.Context, string, string) (tls.ConnectionState, error) {
		t.Fatal("canceled probe dialed")
		return tls.ConnectionState{}, nil
	}})
	if probe(ctx, "www.example.com", 443).OK {
		t.Fatal("canceled probe succeeded")
	}
	for _, server := range []string{"localhost", "127.0.0.1", "https://example.com", "example.com:443", "a..com", "-bad.com", "example.com/path"} {
		if ValidServer(server) {
			t.Fatal(server)
		}
	}
}
func TestRecommendationsFilterSortLimitAndEmpty(t *testing.T) {
	names := strings.Split(CandidateDomains, ",")
	probe := func(ctx context.Context, server string, port int) Target {
		if port != 443 {
			t.Error("wrong candidate port")
		}
		for i, name := range names {
			if server == name {
				return Target{Server: server, TLS13: true, H2: true, OK: i != 0, LatencyMS: int64(50 - i*10)}
			}
		}
		t.Error("unknown candidate")
		return Target{}
	}
	got := Recommend(context.Background(), probe)
	want := []string{names[4], names[3], names[2]}
	var actual []string
	for _, target := range got {
		actual = append(actual, target.Server)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("got %v", actual)
	}
	if empty := Recommend(context.Background(), func(context.Context, string, int) Target { return Target{} }); empty == nil || len(empty) != 0 {
		t.Fatal(empty)
	}
}
