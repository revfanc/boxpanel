package e2e_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "entry")
	}))
	defer direct.Close()
	forwarded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "exit")
	}))
	defer forwarded.Close()
	f := newFixture(t, handshakeServer(t))
	internal, udp := reserveDualPort(t)
	exitControl := reserveTCP(t)
	password, err := exitlink.NewPassword()
	if err != nil {
		t.Fatal(err)
	}
	secrets := append(f.secrets, password)
	key := exitlink.Revision{ID: 1, Version: 1}
	f.snapshot.Exit.Outgoing = &exitlink.Outbound{Revision: key, Address: "127.0.0.1", Port: internal.port(), Password: password,
		Policy: exitlink.Policy{Mode: exitlink.Split, Failure: exitlink.Block, Rules: exitlink.Rules{DomainSuffixes: []string{"via-exit.test"}}}}
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
	entry, target := loopbackServer(t, entryJSON), loopbackServer(t, exitJSON)
	// Give only the exit a distinct local destination. The production security
	// and forwarding rules otherwise keep their order. Both synthetic DNS names
	// resolve to loopback and the fixture never contacts an external network.
	port, err := strconv.Atoi(strings.Split(forwarded.URL, ":")[2])
	if err != nil {
		t.Fatal(err)
	}
	route := target["route"].(object)
	rules := route["rules"].([]any)
	override := object{"inbound": []string{exitlink.InboundPrefix + "1"}, "action": "route-options", "override_port": port}
	route["rules"] = append(append(rules[:len(rules)-1:len(rules)-1], override), rules[len(rules)-1])
	mixed := reserveTCP(t)
	clientConfig := loopbackClient(t, f.client, "hysteria2", mixed.port())
	for _, cfg := range []object{entry, target, clientConfig} {
		cfg["dns"] = object{"servers": []object{
			{"type": "hosts", "tag": "dns-local", "predefined": object{
				"ai.via-exit.test": "127.0.0.1", "direct.test": "127.0.0.1", "xyvia-exit.test": "127.0.0.1",
			}},
		}, "final": "dns-local"}
	}
	exitCore := startCore(t, binary, "exit", target, func() { internal.release(); _ = udp.Close(); exitControl.release() }, secrets)
	exitCore.waitTCP(t, internal.address())
	exitCore.waitUDP(t, internal.address())
	entryCore := startCore(t, binary, "entry", entry, f.releaseServerPorts, secrets)
	entryCore.waitTCP(t, f.controlAddress)
	entryCore.waitUDP(t, f.hysteriaAddress)
	client := startCore(t, binary, "client", clientConfig, mixed.release, secrets)
	client.waitTCP(t, mixed.address())
	for host, want := range map[string]string{"ai.via-exit.test": "exit", "direct.test": "entry", "xyvia-exit.test": "entry"} {
		t.Run(host, func(t *testing.T) {
			u := fmt.Sprintf("http://%s:%s/", host, strings.Split(direct.URL, ":")[2])
			status, body, err := proxyGET(mixed.address(), u)
			if err != nil || status != http.StatusOK || body != want {
				t.Fatalf("want traffic through %s, got status %d, body %q, err %v", want, status, body, err)
			}
		})
	}
}
