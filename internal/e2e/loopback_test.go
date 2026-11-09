// Package e2e_test exercises generated configurations with the pinned core.
// All listeners and destinations are loopback; no root, TUN or systemd is used.
package e2e_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/reality"
)

const targetBody = "boxpanel offline loopback: authenticated proxy reached the target\n"

func TestLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback integration test disabled by -short")
	}
	binary := singboxBinary(t)
	handshake := handshakeServer(t)
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, targetBody)
	}))
	t.Cleanup(target.Close)

	f := newFixture(t, handshake)
	server := startCore(t, binary, "server", f.server, f.releaseServerPorts, f.secrets)
	server.waitTCP(t, f.controlAddress)
	server.waitTCP(t, f.realityAddress)
	server.waitUDP(t, f.hysteriaAddress)

	for _, tc := range []struct {
		name, node string
		wrongKey   bool
	}{
		{name: "VLESSReality", node: "reality"},
		{name: "Hysteria2", node: "hysteria2"},
		{name: "RealityWrongPublicKey", node: "reality", wrongKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mixed := reserveTCP(t)
			clientConfig := loopbackClient(t, f.client, tc.node, mixed.port())
			if tc.wrongKey {
				wrong, err := reality.GenerateKeyPair()
				if err != nil {
					t.Fatal(err)
				}
				// The sole protocol change in the negative case is a different,
				// valid X25519 public key. The server and other credentials stay put.
				for _, raw := range clientConfig["outbounds"].([]any) {
					out := raw.(object)
					if out["tag"] == tc.node {
						out["tls"].(object)["reality"].(object)["public_key"] = wrong.PublicKey
					}
				}
			}
			client := startCore(t, binary, "client", clientConfig, mixed.release, f.secrets)
			client.waitTCP(t, mixed.address())
			before := hits.Load()
			status, body, err := proxyGET(mixed.address(), target.URL)
			server.assertAlive(t)
			client.assertAlive(t)
			if tc.wrongKey {
				// A timeout, connection error or proxy 5xx is a failed request;
				// neither a successful HTTP response nor reaching the target is OK.
				if err == nil && status < 500 {
					t.Fatalf("wrong Reality public key returned HTTP %d", status)
				}
				if hits.Load() != before {
					t.Fatal("wrong Reality public key reached the HTTP target")
				}
				return
			}
			if err != nil {
				t.Fatalf("request through mixed proxy: %v", err)
			}
			if status != http.StatusOK || body != targetBody || hits.Load() != before+1 {
				t.Fatalf("proxy response: status=%d body=%q target hits=%d", status, body, hits.Load()-before)
			}
		})
	}
}

func handshakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "local Reality handshake target\n")
	}))
	s.EnableHTTP2 = true
	// X25519 also avoids a TLS 1.3 HelloRetryRequest when forwarding the
	// pinned core's uTLS ClientHello to a newer Go TLS implementation.
	s.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"}, CurvePreferences: []tls.CurveID{tls.X25519}}
	s.StartTLS()
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dialer := tls.Dialer{Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}}}
	conn, err := dialer.DialContext(ctx, "tcp4", s.Listener.Addr().String())
	if err != nil {
		t.Fatalf("verify local handshake target: %v", err)
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != "h2" {
		t.Fatal("Reality handshake target must negotiate TLS 1.3 and h2")
	}
	return s
}

func proxyGET(proxyAddress, target string) (int, string, error) {
	proxy := &url.URL{Scheme: "http", Host: proxyAddress}
	transport := &http.Transport{
		Proxy:             http.ProxyURL(proxy), // explicit: even loopback must use mixed
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != proxyAddress {
				return nil, fmt.Errorf("unexpected direct dial to %s", address)
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", address)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(target)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), err
}
