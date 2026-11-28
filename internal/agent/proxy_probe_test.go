package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/exitlink"
)

// Run a real authenticated proxy of each kind, and a TLS echo service behind it.
func TestProxyObservesIPThroughEachProtocol(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("make tools installs the test proxy")
	}
	echo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "9.9.9.9") }))
	defer echo.Close()
	roots := x509.NewCertPool()
	roots.AddCert(echo.Certificate())
	for _, protocol := range []string{"socks5", "http", "https"} {
		t.Run(protocol, func(t *testing.T) {
			dir := t.TempDir()
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			p := exitlink.Proxy{Type: protocol, Host: "example.com", Port: port, Username: "test-user", Password: "fixture-password"}
			in := map[string]any{"type": "http", "listen": "127.0.0.1", "listen_port": port, "users": []map[string]string{{"username": p.Username, "password": p.Password}}}
			if protocol == "socks5" {
				in["type"] = "socks"
			}
			if protocol == "https" {
				key, err := x509.MarshalPKCS8PrivateKey(echo.TLS.Certificates[0].PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				in["tls"] = map[string]any{"enabled": true, "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: echo.Certificate().Raw})), "key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
			}
			cfg, err := json.Marshal(map[string]any{"inbounds": []any{in}, "outbounds": []any{map[string]string{"type": "direct"}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "proxy.json")
			if err := os.WriteFile(path, cfg, 0600); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command(bin, "check", "-c", path).Run(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "run", "-c", path)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			for deadline := time.Now().Add(5 * time.Second); ; {
				c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					c.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("proxy did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			through := func(p exitlink.Proxy) (netip.Addr, error) {
				tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != net.JoinHostPort(p.Host, strconv.Itoa(port)) {
						return nil, errors.New("unexpected direct dial")
					}
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				}}
				return probeProxy(t.Context(), p, tr, []string{echo.URL})
			}
			if ip, err := through(p); err != nil || ip.String() != "9.9.9.9" {
				t.Fatalf("unexpected egress %v, %v", ip, err)
			}
			p.Password = "incorrect"
			if ip, err := through(p); err == nil || ip.IsValid() {
				t.Fatal("bad credentials returned an egress IP")
			}
		})
	}
}

func TestProxyCommandIsScopedAndExpiresWithRequest(t *testing.T) {
	m := NewMaster(nil, nil, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	p := exitlink.Proxy{Type: "http", Host: "proxy.example.org", Port: 8080}
	received := make(chan netip.Addr, 1)
	go func() { ip, _ := m.TestNodeProxy(ctx, 3, p); received <- ip }()
	var cmd *ProxyTestCommand
	for cmd == nil && ctx.Err() == nil {
		cmd = m.proxyCommand(3)
		if cmd == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if cmd == nil || cmd.Proxy != p || m.proxyCommand(4) != nil {
		t.Fatal("proxy sent to wrong entry")
	}
	if _, err := m.TestNodeProxy(ctx, 3, p); !errors.Is(err, ErrProxyBusy) {
		t.Fatal("concurrent request was not rejected")
	}
	m.FinishProxyTest(authenticated{id: 4}, ProxyTestResult{ID: cmd.ID, IP: "8.8.8.8"})
	m.FinishProxyTest(authenticated{id: 3}, ProxyTestResult{ID: "obsolete", IP: "8.8.8.8"})
	m.FinishProxyTest(authenticated{id: 3}, ProxyTestResult{ID: cmd.ID, IP: "9.9.9.9"})
	if ip := <-received; ip.String() != "9.9.9.9" || m.proxyCommand(3) != nil {
		t.Fatal("test result or cleanup was incorrect")
	}
	cancel()
	if _, err := m.TestNodeProxy(ctx, 3, p); !errors.Is(err, context.Canceled) || m.proxyCommand(3) != nil {
		t.Fatal("canceled request retained proxy credentials")
	}
}
