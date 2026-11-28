package e2e_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/exitlink"
)

func TestExitLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback integration test disabled by -short")
	}
	binary := singboxBinary(t)
	targetTCP, targetUDP := reserveDualPort(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, targetBody)
	})
	target := &http.Server{Handler: handler}
	go target.Serve(targetTCP)
	t.Cleanup(func() { _ = target.Close() })
	port := targetTCP.port()
	private := httptest.NewServer(handler)
	t.Cleanup(private.Close)
	privatePort := private.Listener.Addr().(*net.TCPAddr).Port

	// Both target listeners use IPv4. Give each resolver a different
	// answer for the same name so only the expected resolver can reach it.
	entryHosts := object{
		"video.netflix.com": "::1", "disneyplus.com": "::1", "chatgpt.com": "::1", "claude.ai": "::1", "ai.via-exit.test": "::1", "all.test": "::1",
		"direct.test": "127.0.0.1", "xyvia-exit.test": "127.0.0.1", "fallback.via-exit.test": "127.0.0.1",
	}
	exitHosts := object{
		"video.netflix.com": "127.0.0.1", "disneyplus.com": "127.0.0.1", "chatgpt.com": "127.0.0.1", "claude.ai": "127.0.0.1", "ai.via-exit.test": "127.0.0.1", "all.test": "127.0.0.1",
		"direct.test": "::1", "xyvia-exit.test": "::1", "fallback.via-exit.test": "::1",
	}
	for _, tc := range []struct {
		name, mode, failure string
		down                bool
		reachable, rejected []string
	}{
		{name: "split", mode: exitlink.Split, failure: exitlink.Block,
			reachable: []string{"chatgpt.com", "claude.ai", "ai.via-exit.test", "direct.test", "xyvia-exit.test", "203.0.113.42", "video.netflix.com", "disneyplus.com", "203.0.113.43"}},
		{name: "all", mode: exitlink.All, failure: exitlink.Block, reachable: []string{"all.test"}, rejected: []string{"127.0.0.1"}},
		{name: "fallback", mode: exitlink.All, failure: exitlink.Fallback, down: true, reachable: []string{"fallback.via-exit.test"}},
		{name: "block", mode: exitlink.Split, failure: exitlink.Block, down: true, reachable: []string{"direct.test"},
			rejected: []string{"chatgpt.com", "fallback.via-exit.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, handshakeServer(t))
			internal, udp := reserveDualPort(t)
			exitControl, mixed := reserveTCP(t), reserveTCP(t)
			password, err := exitlink.NewPassword()
			if err != nil {
				t.Fatal(err)
			}
			secrets := append(f.secrets, password)
			key := exitlink.Revision{ID: 1, Version: 1}
			f.snapshot.Exit.Outgoing = &exitlink.Outbound{Revision: key, Address: "127.0.0.1", Port: internal.port(), Password: password,
				Policy: exitlink.Policy{Mode: tc.mode, Failure: tc.failure, Rules: exitlink.Rules{
					Presets: []string{"ai", "streaming"}, DomainSuffixes: []string{"via-exit.test"},
				}}}
			if tc.down {
				f.snapshot.ExitProbe = exitlink.Probe{Revision: key, Available: new(false), PolicyDown: true}
			}
			// A documentation-only IP stands in for the provider's published range.
			// Only the exit maps it to the isolated listener.
			ai, err := exitlink.BundledAIRules()
			if err != nil {
				t.Fatal(err)
			}
			streaming, err := exitlink.BundledStreamingRules()
			if err != nil {
				t.Fatal(err)
			}
			ai.Prefixes = []string{"203.0.113.42/32"}
			streaming.Prefixes = []string{"203.0.113.43/32"}
			f.snapshot.PresetRules = map[string]exitlink.DestinationRules{"ai": ai, "streaming": streaming}
			entryJSON, err := confgen.Build(f.snapshot, f.options)
			if err != nil {
				t.Fatal(err)
			}
			exitJSON, err := confgen.Build(confgen.Snapshot{BlockBittorrent: true, Exit: exitlink.Configuration{
				Incoming: []exitlink.Inbound{{ID: key.ID, Source: "127.0.0.1", Port: internal.port(), Password: password}},
			}}, confgen.Options{Now: time.Now(), ClashAPIPort: exitControl.port(), ClashAPISecret: f.options.ClashAPISecret})
			if err != nil {
				t.Fatal(err)
			}
			entry := exitLoopbackServer(t, entryJSON, entryHosts, port)
			exit := exitLoopbackServer(t, exitJSON, exitHosts, port)
			exitRoute := exit["route"].(object)
			exitRoute["rules"] = append([]any{object{"ip_cidr": "203.0.113.0/24", "port": port, "action": "route", "outbound": "direct", "override_address": "127.0.0.1"},
				object{"ip_cidr": "203.0.113.0/24", "action": "reject"}}, exitRoute["rules"].([]any)...)
			// A missed IP match on the entry must fail locally, never dial the WAN.
			entryRoute := entry["route"].(object)
			var guarded []any
			for _, raw := range entryRoute["rules"].([]any) {
				if raw.(object)["action"] == "resolve" {
					guarded = append(guarded, object{"ip_cidr": "203.0.113.0/24", "action": "reject"})
				}
				guarded = append(guarded, raw)
			}
			entryRoute["rules"] = guarded
			client := loopbackClient(t, f.client, "hysteria2", mixed.port())
			client["dns"] = hostsDNS(object{})
			relay, relayUDP := reserveDualPort(t)
			client["inbounds"] = append(client["inbounds"].([]any), object{
				"type": "direct", "tag": "test-udp", "network": "udp", "listen": "127.0.0.1", "listen_port": relay.port(),
				"override_address": tc.reachable[0], "override_port": port,
			})

			exitCore := startCore(t, binary, "exit", exit, func() { internal.release(); _ = udp.Close(); exitControl.release() }, secrets)
			exitCore.waitTCP(t, internal.address())
			exitCore.waitUDP(t, internal.address())
			entryCore := startCore(t, binary, "entry", entry, f.releaseServerPorts, secrets)
			entryCore.waitTCP(t, f.controlAddress)
			entryCore.waitUDP(t, f.hysteriaAddress)
			clientCore := startCore(t, binary, "client", client, func() { mixed.release(); relay.release(); _ = relayUDP.Close() }, secrets)
			clientCore.waitTCP(t, mixed.address())
			clientCore.waitUDP(t, relay.address())
			t.Run("UDP", func(t *testing.T) { checkExitDatagram(t, relay.address(), targetUDP) })
			for _, host := range tc.reachable {
				t.Run(host, func(t *testing.T) {
					status, body, err := proxyGET(mixed.address(), fmt.Sprintf("http://%s:%d/", host, port))
					if err != nil || status != http.StatusOK || body != targetBody {
						t.Fatalf("request failed: status=%d body=%q err=%v", status, body, err)
					}
					requireRejectedGET(t, mixed.address(), host, privatePort)
				})
			}
			for _, host := range tc.rejected {
				t.Run(host, func(t *testing.T) {
					requireRejectedGET(t, mixed.address(), host, port)
				})
			}
			exitCore.assertAlive(t)
			entryCore.assertAlive(t)
			clientCore.assertAlive(t)
		})
	}
}

func checkExitDatagram(t *testing.T, relay string, target net.PacketConn) {
	t.Helper()
	conn, err := net.Dial("udp4", relay)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := t.Name()
	deadline := time.Now().Add(5 * time.Second)
	if err := target.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, peer, err := target.ReadFrom(buf)
	if err != nil || string(buf[:n]) != payload {
		t.Fatalf("target did not receive the UDP request: %v", err)
	}
	if _, err := target.WriteTo(buf[:n], peer); err != nil {
		t.Fatal(err)
	}
	n, err = conn.Read(buf)
	if err != nil || string(buf[:n]) != payload {
		t.Fatalf("client did not receive the UDP reply: %v", err)
	}
}

func requireRejectedGET(t *testing.T, proxy, host string, port int) {
	t.Helper()
	status, _, err := proxyGET(proxy, fmt.Sprintf("http://%s:%d/", host, port))
	if err == nil && status < 500 {
		t.Fatalf("blocked request to %s:%d returned HTTP %d", host, port, status)
	}
}

func exitLoopbackServer(t *testing.T, raw []byte, hosts object, targetPort int) object {
	t.Helper()
	cfg := bindLoopbackServer(t, raw)
	cfg["dns"] = hostsDNS(hosts)
	// Exempt only the TCP/UDP target port from the post-resolution private-IP
	// guard. The second listener tests that other private destinations fail.
	rules := cfg["route"].(object)["rules"].([]any)
	for i, raw := range rules {
		rule := raw.(object)
		if rule["ip_is_private"] == true && rule["inbound"] == nil && rule["action"] == "reject" {
			rules[i] = object{"type": "logical", "mode": "and", "action": "reject", "rules": []object{
				{"ip_is_private": true}, {"port": targetPort, "invert": true},
			}}
		}
	}
	return cfg
}

func hostsDNS(hosts object) object {
	return object{"servers": []object{{"type": "hosts", "tag": "dns-local", "predefined": hosts}}, "final": "dns-local"}
}
