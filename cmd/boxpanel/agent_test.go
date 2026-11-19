package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/httpapi"
	"github.com/revfanc/boxpanel/internal/spa"
)

func TestJoinServeInfoAndLeaveNode(t *testing.T) {
	t.Setenv("BOXPANEL_PUBLIC_IP", "192.168.1.10")
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	master := agent.NewMaster(conn, nil, nil)
	_, token, err := master.IssueToken(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(nil, httpapi.Options{Agents: master, WebPath: "hidden", Logger: logger})
	h, err := spa.New(fstest.MapFS{"index.html": {Data: []byte("<!--BOXPANEL_INJECT-->")}}, spa.Options{WebPath: "hidden", API: api})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(h)
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	var output bytes.Buffer
	execute := func(command string, args ...string) string {
		t.Helper()
		output.Reset()
		if rc := run(append([]string{command, "--data-dir", dir}, args...), &output, &output); rc != 0 {
			t.Fatalf("%s exit=%d output=%s", command, rc, output.String())
		}
		return output.String()
	}
	args := []string{"--master", server.URL + "/hidden/", "--token", token, "--master-pin", pin}
	if rc := run(append([]string{"join", "--data-dir", dir}, args...), &output, &output); rc == 0 || !strings.Contains(output.String(), agent.ErrPublicIP.Error()) {
		t.Fatal("join did not surface the public IP error", output.String())
	}
	if got := execute("join", append(args, "--public-ip", "8.8.8.8")...); strings.Contains(got, token) || !strings.Contains(got, "server_id=2") {
		t.Fatal("unexpected join output")
	}
	var publicHost string
	if err := conn.QueryRow("SELECT public_host FROM servers WHERE id=2").Scan(&publicHost); err != nil || publicHost != "8.8.8.8" {
		t.Fatal("--public-ip did not override the environment", err)
	}
	opts, err := parseServeOptions([]string{"--dev", "--data-dir", dir, "--db-path", filepath.Join(dir, "boxpanel.db")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(app.AgentKeyPath(opts.paths))
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 32 {
		t.Fatal("agent key is missing or has an unexpected format or permissions", err)
	}
	for _, filename := range []string{opts.paths.CertFile, opts.paths.KeyFile} {
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("node created panel TLS material")
		}
	}
	node, err := db.Open(ctx, opts.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, opts, logger, io.Discard) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := db.GetSetting(ctx, node, db.AgentLastHeartbeat)
		if err == nil && result.Status == "ok" {
			break
		}
		select {
		case err := <-done:
			t.Fatal("node service exited early", err)
		case <-ctx.Done():
			t.Fatal("node service never recorded a successful heartbeat")
		case <-ticker.C:
		}
	}
	got := execute("info")
	if !strings.Contains(got, "最近心跳: 成功") || !strings.Contains(got, server.URL+"/hidden/") ||
		strings.Contains(got, "面板地址") || strings.Contains(got, pin) || strings.Contains(got, token) {
		t.Fatal("unexpected agent info")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	execute("leave")
	execute("leave")
	if _, err := os.Stat(app.AgentKeyPath(opts.paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("leave retained the signing key")
	}
	node.Close()
	output.Reset()
	if rc := run([]string{"info", "--data-dir", dir}, &output, &output); rc == 0 {
		t.Fatal("departed node still initialized")
	}
	if _, err := app.Initialize(context.Background(), app.InitOptions{Paths: opts.paths, Dev: true}); err != nil {
		t.Fatal("departed node cannot be initialized again", err)
	}
}

func TestHeartbeatBackoffAndRemovalHint(t *testing.T) {
	var last db.AgentHeartbeatResult
	streak, backoff := 0, 5*time.Second
	now := time.Unix(1000, 0)
	for i, expected := range []time.Duration{5, 10, 20, 40, 60, 60} {
		next, delay := nextHeartbeat(last, agent.ErrUnauthorized, &streak, &backoff, now)
		if delay != expected*time.Second || next.RepeatedUnauthorized != (i >= 2) || next.Status != "unauthorized" {
			t.Fatalf("attempt %d: next=%+v delay=%v", i, next, delay)
		}
		last = next
	}
	last, _ = nextHeartbeat(last, errors.New("offline"), &streak, &backoff, now)
	if !last.RepeatedUnauthorized || last.Status != "unreachable" || streak != 0 {
		t.Fatal("network failure reset removal hint or retained the consecutive counter")
	}
	last, delay := nextHeartbeat(last, nil, &streak, &backoff, now)
	if last.RepeatedUnauthorized || last.Status != "ok" || delay != 30*time.Second || backoff != 5*time.Second {
		t.Fatal("successful heartbeat did not reset the retry state")
	}
}

func TestHeartbeatClockSkewAppearsInInfoAndLogs(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	master := db.AgentConnection{MasterURL: "https://master.example/panel/", ServerID: 2}
	if err := db.SetSetting(ctx, conn, db.AgentMaster, master); err != nil {
		t.Fatal(err)
	}
	last := db.AgentHeartbeatResult{Status: "unauthorized", RepeatedUnauthorized: true}
	streak, backoff := 3, 20*time.Second
	skew := &agent.ClockSkewError{Seconds: 150}
	next, delay := nextHeartbeat(last, skew, &streak, &backoff, time.Now())
	if next.Status != "clock_skew" || next.ClockSkewSeconds != 150 || next.RepeatedUnauthorized || streak != 0 || delay != 20*time.Second {
		t.Fatalf("unexpected clock skew result: %+v, streak=%d, delay=%s", next, streak, delay)
	}
	if ok, err := recordHeartbeatResult(ctx, conn, master, next); err != nil || !ok {
		t.Fatal("clock skew was not saved", err)
	}
	var output bytes.Buffer
	if err := printAgentInfo(ctx, conn, &output); err != nil || !strings.Contains(output.String(), skew.Error()) || strings.Contains(output.String(), removalHint) {
		t.Fatal("info did not report clock skew", err, output.String())
	}
	output.Reset()
	logger := slog.New(slog.NewTextHandler(&output, nil))
	logHeartbeatResult(logger, last, next)
	logHeartbeatResult(logger, next, next)
	if strings.Count(output.String(), skew.Error()) != 1 || strings.Contains(output.String(), removalHint) {
		t.Fatal("log did not report clock skew once", output.String())
	}
}
