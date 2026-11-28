package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
)

func TestDailyAIRulesKeepLastWorkingSnapshot(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "entry.db")
	open := func() *ExitMonitor {
		conn, err := db.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		return &ExitMonitor{Reader: conn, Writer: conn, Manager: &exitMonitorApplier{writer: conn}}
	}
	m := open()
	defer func() { m.Writer.Close() }()
	if err := db.SetSetting(ctx, m.Writer, db.NodeRole, db.RoleAgent); err != nil {
		t.Fatal(err)
	}
	status := 200
	domains := "exact.example.org\n+.suffix.example.org\n"
	voice := `{"prefixes":[{"ipv4Prefix":"8.8.8.0/24"},{"ipv6Prefix":"2606:4700::/48"}]}`
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/domains" {
			fmt.Fprint(w, domains)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, voice)
	}))
	defer source.Close()
	requests := 0
	now := time.Unix(100000, 0)
	wire := func() {
		m.now = func() time.Time { return now }
		m.fetchAIRules = func(ctx context.Context) (exitlink.DestinationRules, error) {
			requests++
			return downloadAIRules(ctx, source.Client(), source.URL+"/domains", source.URL+"/voice")
		}
	}
	wire()
	run := func(wantFailure bool, wantRequests int) {
		t.Helper()
		err := m.refreshAIRules(ctx)
		if (err != nil) != wantFailure || requests != wantRequests {
			t.Fatalf("refresh error=%v, requests=%d; want failure=%v, requests=%d", err, requests, wantFailure, wantRequests)
		}
	}
	mirror := func(mode string, presets ...string) {
		t.Helper()
		cfg := exitlink.Configuration{Outgoing: &exitlink.Outbound{Policy: exitlink.Policy{Mode: mode, Rules: exitlink.Rules{Presets: presets}}}}
		if err := db.SetSetting(ctx, m.Writer, db.AgentExit, cfg); err != nil {
			t.Fatal(err)
		}
	}
	mirror(exitlink.Split, "streaming")
	run(false, 0)
	mirror(exitlink.All, "ai")
	run(false, 0)
	mirror(exitlink.Split, "ai")
	run(false, 1)
	original, err := db.GetSetting(ctx, m.Reader, db.ExitAIRules)
	if err != nil || len(original.Prefixes) != 2 || len(original.Suffixes) != 1 || len(original.Domains) != 1 {
		t.Fatal("missing domain or IPv4/IPv6 rules", err)
	}
	retained := func() {
		t.Helper()
		got, err := db.GetSetting(ctx, m.Reader, db.ExitAIRules)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("failed update replaced the accepted rules", err)
		}
	}
	now = now.Add(aiRefreshInterval - time.Second)
	run(false, 1)
	now = now.Add(time.Second)
	domains = "+.new.example.org\n"
	status = 503
	run(true, 2)
	retained()
	if err := m.Writer.Close(); err != nil {
		t.Fatal(err)
	}
	m = open()
	wire()
	run(false, 2)
	retained()
	now = now.Add(aiRefreshInterval)
	status = 200
	voice = `{"prefixes":[]}`
	run(true, 3)
	retained()
	now = now.Add(aiRefreshInterval)
	voice = `{"prefixes":[{"ipv4Prefix":"9.9.9.9/32"}]}`
	m.Manager.(*exitMonitorApplier).err = errors.New("apply rejected")
	run(true, 4)
	retained()
	now = now.Add(aiRefreshInterval)
	m.Manager.(*exitMonitorApplier).err = nil
	run(false, 5)
	got, err := db.GetSetting(ctx, m.Reader, db.ExitAIRules)
	if err != nil || len(got.Domains) != 0 || len(got.Suffixes) != 1 || got.Suffixes[0] != "new.example.org" || len(got.Prefixes) != 1 || got.Prefixes[0] != "9.9.9.9/32" {
		t.Fatal("fresh domains and ranges were not replaced together", err)
	}
}
