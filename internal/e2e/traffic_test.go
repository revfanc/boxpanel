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
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/traffic"
)

// This extends the offline loopback coverage without changing TestLoopback's
// startup, assertions or skip conditions. Kernel counters survive closed transfers.
func TestTrafficLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback integration test disabled by -short")
	}
	counting := runtime.GOOS == "linux" && os.Geteuid() == 0
	for _, name := range []string{"nft", "ip", "unshare"} {
		if _, err := exec.LookPath(name); err != nil {
			counting = false
		}
	}
	if counting && os.Getenv("BOXPANEL_TRAFFIC_NETNS") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := exec.CommandContext(ctx, "unshare", "-n", "true").CombinedOutput(); err != nil {
			counting = false
		} else {
			cmd := exec.CommandContext(ctx, "unshare", "-n", os.Args[0], "-test.run=^TestTrafficLoopback$", "-test.v")
			cmd.Env = append(os.Environ(), "BOXPANEL_TRAFFIC_NETNS=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated traffic e2e: %v\n%s", err, out)
			}
			return
		}
	}
	if counting {
		if _, err := (host.Native{}).Run(context.Background(), "ip", "link", "set", "lo", "up"); err != nil {
			t.Fatal(err)
		}
	}
	binary := singboxBinary(t)
	f := newFixture(t, handshakeServer(t), counting)
	if counting {
		m := firewall.New(firewall.Options{OS: "linux"})
		if err := m.Sync(context.Background(), firewall.Plan{}, firewall.Plan{TrafficUsers: []int64{1}}); err != nil {
			t.Fatal(err)
		}
	}
	server := startCore(t, binary, "traffic-server", f.server, f.releaseServerPorts, f.secrets)
	server.waitTCP(t, f.controlAddress)
	server.waitTCP(t, f.realityAddress)
	server.waitUDP(t, f.hysteriaAddress)
	collector := traffic.NewCollector(f.writer, f.reader, nil)
	if counting {
		if _, err := f.writer.Exec(`INSERT INTO settings(key,value) VALUES('applied_users','[1]')`); err != nil {
			t.Fatal(err)
		}
		if err := collector.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []string{"reality", "hysteria2"} {
		t.Run(node, func(t *testing.T) {
			const upload = 32 << 10
			const download = 64 << 10
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil || n != upload {
					t.Error("target upload size", n, err)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(bytes.Repeat([]byte("x"), download))
				w.(http.Flusher).Flush()
			}))
			defer target.Close()
			mixed := reserveTCP(t)
			client := startCore(t, binary, "traffic-client", loopbackClient(t, f.client, node, mixed.port()), mixed.release, f.secrets)
			client.waitTCP(t, mixed.address())
			proxy := &url.URL{Scheme: "http", Host: mixed.address()}
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
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
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || !bytes.Equal(body, bytes.Repeat([]byte("x"), download)) {
				t.Fatal("transfer body mismatch")
			}
			_ = resp.Body.Close()
			t.Run("kernel-accounting", func(t *testing.T) {
				if !counting {
					t.Skip("traffic assertion requires Linux root, nft and a private network namespace; connectivity was checked")
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
			})
			server.assertAlive(t)
			client.assertAlive(t)
		})
	}
}
