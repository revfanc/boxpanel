package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type systemOpener struct {
	port int
	err  error
}

func (o *systemOpener) Open(_ context.Context, port int, proto string) error {
	o.port = port
	if proto != "tcp" {
		return errors.New("wrong protocol")
	}
	return o.err
}

func TestSystemCommands(t *testing.T) {
	clearEnvironment(t)
	ctx := context.Background()
	dir := t.TempDir()
	if code := run([]string{"init", "--dev", "--data-dir", dir}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	opener := &systemOpener{}
	bbrCalls := 0
	deps := systemDependencies{os: "linux", opener: opener, bbr: func(context.Context) (sysinfo.BBR, error) { bbrCalls++; return sysinfo.BBR{Enabled: true}, nil }}
	for _, command := range []string{"firewall-open", "bbr"} {
		if code := systemCommand(ctx, []string{command, "--data-dir", dir}, deps, io.Discard, io.Discard); code != 0 {
			t.Fatal(command, code)
		}
	}
	if opener.port != 2053 || bbrCalls != 1 {
		t.Fatal(opener, bbrCalls)
	}
	opener.err = errors.New("private-password-do-not-print")
	var out bytes.Buffer
	if code := systemCommand(ctx, []string{"firewall-open", "--data-dir", dir}, deps, io.Discard, &out); code != 1 || strings.Contains(out.String(), "private-password") {
		t.Fatal(code, out.String())
	}
	deps.os = "darwin"
	if code := systemCommand(ctx, []string{"bbr"}, deps, io.Discard, io.Discard); code != 1 || bbrCalls != 1 {
		t.Fatal("non-Linux called host")
	}
	for _, args := range [][]string{{"--help"}, {"bbr", "--help"}, {"firewall-cleanup", "--help"}, {"ready", "--help"}} {
		if systemCommand(ctx, args, deps, io.Discard, io.Discard) != 0 {
			t.Fatal(args)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"bbr", "extra"}, {"bbr", "--dev"}} {
		if systemCommand(ctx, args, deps, io.Discard, io.Discard) != 2 {
			t.Fatal(args)
		}
	}
}

func TestPanelReadinessUsesLocalTrustedHTTPS(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	status := http.StatusUnauthorized
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/random/api/v1/auth/me" || r.Method != "GET" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected readiness request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	_, portString, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	certFile := filepath.Join(t.TempDir(), "panel.crt")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := app.Config{WebPort: port, WebPath: "random", PublicHost: "never-resolve.invalid"}
	if err := panelReady(context.Background(), cfg, certFile); err != nil {
		t.Fatal(err)
	}
	status = http.StatusInternalServerError
	if panelReady(context.Background(), cfg, certFile) == nil {
		t.Fatal("accepted unhealthy endpoint")
	}
	if err := os.WriteFile(certFile, []byte("invalid cert"), 0600); err != nil {
		t.Fatal(err)
	}
	if panelReady(context.Background(), cfg, certFile) == nil {
		t.Fatal("ignored invalid trust anchor")
	}
}
