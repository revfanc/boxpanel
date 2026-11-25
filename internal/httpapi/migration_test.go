package httpapi

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/migration"
)

func migrationTLS(t *testing.T, handler http.Handler, dir string) *httptest.Server {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "certs/panel.crt"), filepath.Join(dir, "certs/panel.key"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestMasterMigrationLifecycleOverHTTPS(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	paths := app.Paths{DataDir: dir, Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "certs/panel.crt"), KeyFile: filepath.Join(dir, "certs/panel.key")}
	init, err := app.Initialize(ctx, app.InitOptions{Paths: paths, PublicIP: "127.0.0.1", Port: 2053})
	must(err)
	old, err := app.OpenExisting(ctx, paths.Database)
	must(err)
	defer old.Close()
	oldReader, err := db.OpenReader(ctx, paths.Database)
	must(err)
	defer oldReader.Close()
	certificates, err := acme.New(ctx, old, acme.Options{Dir: paths.CertificateDir(), CertFile: paths.CertFile, KeyFile: paths.KeyFile})
	must(err)
	pin, err := certificates.FallbackPin()
	must(err)
	var stopped atomic.Int32
	source := &migration.Service{DB: old, ReadDB: oldReader, DataDir: dir, Version: "v1.2.3-rc.1", Pin: pin,
		Signer: certificates.FallbackSigner(), StopCore: func(context.Context) error { stopped.Add(1); return nil }}
	oldMaster := agent.NewMaster(old, oldReader, nil)
	authSource, err := auth.New(old, nil)
	must(err)
	oldEndpoint := httptest.NewUnstartedServer(nil)
	defer oldEndpoint.Close()
	sourceHandler := New(authSource, Options{WebPath: "panel", PanelPort: oldEndpoint.Listener.Addr().(*net.TCPAddr).Port,
		DB: old, ReadDB: oldReader, Agents: oldMaster, Migration: source, Logger: logger})
	oldEndpoint.Config.Handler = http.StripPrefix("/panel", sourceHandler)
	pair, err := tls.LoadX509KeyPair(paths.CertFile, paths.KeyFile)
	must(err)
	oldEndpoint.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	oldEndpoint.StartTLS()
	oldURL := oldEndpoint.URL + "/panel/"
	oldClient, err := agent.NewClient(oldURL, pin)
	must(err)
	defer oldClient.Close()
	edge, _ := joinTestNode(t, oldMaster, oldClient, oldURL, pin, "online", "8.8.8.8")
	defer edge.Close()
	offline, _ := joinTestNode(t, oldMaster, oldClient, oldURL, pin, "offline", "8.8.4.4")
	original := edge.Master
	const insertUser = `INSERT INTO users (id,name,uuid,password,sub_token,created_at,all_servers,traffic_epoch) VALUES
		(1,'alice','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','node-password','test-subscription-token',0,0,1)`
	_, err = old.Exec(insertUser)
	must(err)
	_, err = old.Exec("INSERT INTO user_servers(user_id,server_id) VALUES (1,?)", original.ServerID)
	must(err)
	_, err = edge.Writer.Exec(insertUser)
	must(err)
	beat := agent.Heartbeat{Version: source.Version, Protocol: agent.ProtocolVersion, SingboxActive: true}
	baseline, err := oldClient.SyncHeartbeat(ctx, original.ServerID, edge.Key, beat)
	must(err)
	prior := agent.TrafficReport{Counters: []agent.TrafficCounter{{UserID: 1, Epoch: 1, Up: 8, Down: 12}}}
	must(oldClient.ReportTraffic(ctx, original.ServerID, edge.Key, prior))
	oldSamples := agent.InterfaceSamples{Samples: []agent.InterfaceSample{{SampledAt: time.Now().Add(-time.Minute).UnixNano(), Interface: "eth0", RX: 8}}}
	must(oldClient.ReportInterfaceSamples(ctx, original.ServerID, edge.Key, oldSamples))
	login, err := authSource.Login(ctx, "127.0.0.1", init.Username, init.Password, "")
	must(err)
	cookie := &http.Cookie{Name: CookieName, Value: login.Token}
	requireStatus(t, request(t, oldEndpoint.Config.Handler, "POST", oldURL+"api/v1/migration/token", `{"confirm":true}`, nil, nil), 401)
	requireStatus(t, request(t, oldEndpoint.Config.Handler, "POST", oldURL+"api/v1/migration/token", `{"confirm":true}`, cookie, map[string]string{"Origin": "https://other.example"}), 403)
	grantResponse := request(t, oldEndpoint.Config.Handler, "POST", oldURL+"api/v1/migration/token", `{"confirm":true}`, cookie, nil)
	requireStatus(t, grantResponse, 201)
	var grant migration.Grant
	must(json.Unmarshal(grantResponse.Body.Bytes(), &grant))
	if grant.SourceURL != oldURL || grant.MasterPin != pin || grant.Version != source.Version {
		t.Fatal("migration grant did not use the effective panel address, version and fixed pin")
	}
	secret, err := migration.NewSecret()
	must(err)
	transfer, err := migration.NewClient(oldURL, pin)
	must(err)
	defer transfer.Close()
	archive, newIPURL, expires, err := transfer.Download(ctx, migration.Claim{Token: grant.Token, Secret: secret, PublicIP: "9.9.9.9", Version: grant.Version})
	must(err)
	if !strings.HasPrefix(newIPURL, "https://9.9.9.9:") || expires != grant.ExpiresAt {
		t.Fatal("invalid new address or claim expiry")
	}
	if _, _, _, err := transfer.Download(ctx, migration.Claim{Token: grant.Token, Secret: secret, PublicIP: "9.9.9.9", Version: grant.Version}); !errors.Is(err, migration.ErrToken) {
		t.Fatal("successful claim was reusable", err)
	}
	requireStatus(t, request(t, oldEndpoint.Config.Handler, "GET", oldURL+"api/v1/auth/me", "", cookie, nil), 200)
	requireStatus(t, request(t, oldEndpoint.Config.Handler, "POST", oldURL+"api/v1/users", `{"name":"new"}`, cookie, nil), 409)
	if reply, err := oldClient.SyncHeartbeat(ctx, original.ServerID, edge.Key, beat); err != nil || !reply.ReadOnly || len(reply.MasterURLs) > 0 {
		t.Fatal("frozen heartbeat is not read-only", err)
	}
	during := agent.TrafficReport{Counters: []agent.TrafficCounter{{UserID: 1, Epoch: 1, Up: 30, Down: 45}}}
	if err := oldClient.ReportTraffic(ctx, original.ServerID, edge.Key, during); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("frozen source acknowledged traffic", err)
	}
	newSamples := agent.InterfaceSamples{Samples: []agent.InterfaceSample{{SampledAt: time.Now().UnixNano(), Interface: "eth0", RX: 20}}}
	if err := oldClient.ReportInterfaceSamples(ctx, original.ServerID, edge.Key, newSamples); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("frozen source acknowledged interface samples", err)
	}
	// Accumulate on the node while the source will not acknowledge anything.
	_, err = edge.Writer.Exec("UPDATE users SET traffic_up=30,traffic_down=45 WHERE id=1")
	must(err)
	_, err = edge.Writer.Exec("INSERT INTO server_traffic_samples(server_id,sampled_at,interface,rx,tx) VALUES (1,?,'eth0',20,0)", newSamples.Samples[0].SampledAt)
	must(err)
	must(db.SetSetting(ctx, edge.Writer, db.AgentAppliedGeneration, baseline.DesiredGeneration))
	must(edge.Sync(ctx, beat, func() bool { return true }))
	stage, err := backup.Stage(ctx, archive, t.TempDir())
	must(err)
	incoming, err := migration.PrepareTarget(ctx, stage, oldURL, newIPURL, secret, pin, expires)
	must(err)
	newDB := filepath.Join(stage, "boxpanel.db")
	next, err := db.Open(ctx, newDB)
	must(err)
	defer next.Close()
	newReader, err := db.OpenReader(ctx, newDB)
	must(err)
	defer newReader.Close()
	cfg, err := app.LoadConfig(ctx, next)
	must(err)
	if cfg.URL() != newIPURL {
		t.Fatal("the target did not adopt the public IP and effective panel route")
	}
	if _, err := db.GetSetting(ctx, next, db.MigrationOutgoing); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("grant and freeze should have been excluded from the snapshot", err)
	}
	var loadedID int64
	var key, token string
	must(next.QueryRow(`SELECT s.id,s.agent_public_key,u.sub_token FROM user_servers a JOIN servers s ON s.id=a.server_id
		JOIN users u ON u.id=a.user_id WHERE u.name='alice'`).Scan(&loadedID, &key, &token))
	if loadedID != original.ServerID || key != original.PublicKey || token != "test-subscription-token" {
		t.Fatal("snapshot did not retain node identity and subscription access")
	}
	receiver := &migration.Service{DB: next, ReadDB: newReader, DataDir: stage, Pin: pin, Version: grant.Version, Signer: certificates.FallbackSigner()}
	authTarget, err := auth.New(next, nil)
	must(err)
	newMaster := agent.NewMaster(next, newReader, nil)
	var unreachable atomic.Bool
	nextHandler := http.StripPrefix("/panel", New(authTarget, Options{WebPath: "panel", DB: next, ReadDB: newReader, Migration: receiver, Agents: newMaster, Logger: logger}))
	newEndpoint := migrationTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unreachable.Load() {
			w.WriteHeader(503)
			return
		}
		nextHandler.ServeHTTP(w, r)
	}), stage)
	requireStatus(t, request(t, nextHandler, "GET", newEndpoint.URL+"/panel/api/v1/auth/me", "", cookie, nil), 401)
	// Use reachable TLS endpoints for the cutover after verifying production
	// address selection from the explicit public IP above.
	newURL := newEndpoint.URL + "/panel/"
	incoming.TargetURL = newURL
	must(db.SetSetting(ctx, next, db.MigrationReceived, incoming))
	claimed, err := db.LoadMasterMigration(ctx, old, db.MigrationOutgoing)
	must(err)
	claimed.TargetURL = newURL
	must(db.SetSetting(ctx, old, db.MigrationOutgoing, claimed))
	if err := transfer.ReportReady(ctx, migration.Ready{Secret: secret, TargetURL: oldURL}); !errors.Is(err, migration.ErrToken) {
		t.Fatal("the claim should not authorize a different target URL", err)
	}
	must(transfer.ReportReady(ctx, migration.Ready{Secret: secret, TargetURL: newURL}))
	must(transfer.ReportReady(ctx, migration.Ready{Secret: secret, TargetURL: newURL}))
	if err := transfer.Abort(ctx, secret); !errors.Is(err, migration.ErrCommitted) || stopped.Load() != 1 {
		t.Fatal("retirement was reversible or the core was not stopped once", err)
	}
	must(migration.Activate(ctx, next, incoming.ID))
	if status, err := receiver.PanelStatus(ctx); err != nil || status.Total != 2 || status.Migrated != 0 || len(status.Waiting) != 2 {
		t.Fatal("old contact should not count as target progress", status, err)
	}
	must(edge.Sync(ctx, beat, func() bool { return true }))
	switched, err := db.GetSetting(ctx, edge.Reader, db.AgentMaster)
	must(err)
	if switched.MasterURL != newURL || !switched.SameIdentity(original) || len(switched.AlternateURLs) != 1 || switched.AlternateURLs[0] != oldURL {
		t.Fatal("automatic migration did not persist priority and the previous master")
	}
	assertTotals := func(conn *sql.DB, up, down, samples int64) {
		t.Helper()
		var actualUp, actualDown, sampleCount int64
		must(conn.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=1").Scan(&actualUp, &actualDown))
		must(conn.QueryRow("SELECT count(*) FROM server_traffic_samples WHERE server_id=?", original.ServerID).Scan(&sampleCount))
		if actualUp != up || actualDown != down || sampleCount != samples {
			t.Fatalf("traffic or samples mismatch: %d/%d and %d samples", actualUp, actualDown, sampleCount)
		}
	}
	assertTotals(old, 8, 12, 1)
	assertTotals(next, 30, 45, 2)
	newClient, err := agent.NewClient(newURL, pin)
	must(err)
	defer newClient.Close()
	must(newClient.ReportTraffic(ctx, original.ServerID, edge.Key, during))
	must(newClient.ReportInterfaceSamples(ctx, original.ServerID, edge.Key, newSamples))
	assertTotals(next, 30, 45, 2)
	if status, err := receiver.PanelStatus(ctx); err != nil || status.Migrated != 1 || len(status.Waiting) != 1 || status.Waiting[0].ID != offline.Master.ServerID {
		t.Fatal("wrong number of nodes to switch manually", status, err)
	}
	unreachable.Store(true)
	must(edge.Sync(ctx, beat, func() bool { return true }))
	switched, err = db.GetSetting(ctx, edge.Reader, db.AgentMaster)
	must(err)
	if switched.MasterURL != newURL || edge.Client.MasterURL() != oldURL {
		t.Fatal("fallback failed or replaced the preferred address")
	}
	unreachable.Store(false)
	must(edge.Sync(ctx, beat, func() bool { return true }))
	if edge.Client.MasterURL() != newURL {
		t.Fatal("node did not retry its preferred address")
	}
	if _, err := agent.SetMaster(ctx, offline.Writer, offline.Master, offline.Key, oldURL, beat); !errors.Is(err, db.ErrMigrationReadOnly) {
		t.Fatal("set-master accepted a retired endpoint", err)
	}
	if _, err := agent.SetMaster(ctx, offline.Writer, offline.Master, offline.Key, newURL, beat); err != nil {
		t.Fatal(err)
	}
	must(db.SetSetting(ctx, offline.Writer, db.AgentAppliedGeneration, int64(1)))
	// A normal signed heartbeat, following the persisted manual switch, counts.
	must(newClient.Heartbeat(ctx, offline.Master.ServerID, offline.Key, beat))
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); source.Run(workerCtx) }()
	defer func() { cancel(); <-done }()
	limit := time.Now().Add(2 * time.Second)
	for {
		status, err := source.PanelStatus(ctx)
		must(err)
		if status.Migrated == 2 {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("old master did not receive target progress", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	resumed, err := db.OpenReader(ctx, paths.Database)
	must(err)
	defer resumed.Close()
	status, err := (&migration.Service{ReadDB: resumed}).PanelStatus(ctx)
	must(err)
	if status.Phase != db.MigrationRetired || !status.CoreStopped || status.Migrated != 2 || status.Total != 2 {
		t.Fatal("retired state and progress were not persisted", status)
	}
}
