package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/traffic"
)

// This extends the offline loopback coverage without changing TestLoopback's
// startup, assertions or skip conditions. Sample while a known transfer is open:
// /connections intentionally omits connections that have already closed.
func TestTrafficLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback integration test disabled by -short")
	}
	binary := singboxBinary(t)
	f := newFixture(t, handshakeServer(t))
	server := startCore(t, binary, "traffic-server", f.server, f.releaseServerPorts, f.secrets)
	server.waitTCP(t, f.controlAddress)
	server.waitTCP(t, f.realityAddress)
	server.waitUDP(t, f.hysteriaAddress)
	collector := traffic.NewCollector(f.writer, f.reader, nil)
	for _, node := range []string{"reality", "hysteria2"} {
		t.Run(node, func(t *testing.T) {
			const upload = 32 << 10
			const download = 64 << 10
			release := make(chan struct{})
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil || n != upload {
					t.Error("target upload size", n, err)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(bytes.Repeat([]byte("x"), download))
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer target.Close()
			defer close(release)
			mixed := reserveTCP(t)
			client := startCore(t, binary, "traffic-client", loopbackClient(t, f.client, node, mixed.port()), mixed.release, f.secrets)
			client.waitTCP(t, mixed.address())
			proxy := &url.URL{Scheme: "http", Host: mixed.address()}
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != mixed.address() {
					return nil, fmt.Errorf("unexpected direct dial")
				}
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", address)
			}}
			defer transport.CloseIdleConnections()
			httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			var beforeUp, beforeDown int64
			if err := f.reader.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=1").Scan(&beforeUp, &beforeDown); err != nil {
				t.Fatal(err)
			}
			resp, err := httpClient.Post(target.URL, "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte("y"), upload)))
			if err != nil {
				t.Fatal("transfer through proxy", err)
			}
			defer resp.Body.Close()
			body := make([]byte, download)
			if _, err := io.ReadFull(resp.Body, body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || !bytes.Equal(body, bytes.Repeat([]byte("x"), download)) {
				t.Fatal("transfer body mismatch")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := collector.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			if err := collector.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			var up, down int64
			if err := f.reader.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=1").Scan(&up, &down); err != nil {
				t.Fatal(err)
			}
			up -= beforeUp
			down -= beforeDown
			if up < upload/2 || up > upload*3 || down < download/2 || down > download*3 {
				t.Fatalf("sampled usage out of range: upload=%d download=%d", up, down)
			}
			server.assertAlive(t)
			client.assertAlive(t)
		})
	}
}
