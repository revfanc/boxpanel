package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/httpapi"
	"github.com/revfanc/boxpanel/internal/migration"
	"github.com/revfanc/boxpanel/internal/singbox"
)

func TestReceiveMigrationAbortsOnTransferOrRestoreFailure(t *testing.T) {
	ctx := t.Context()
	p, _ := backupPaths(t)
	conn, err := app.OpenExisting(ctx, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	certificates, err := acme.New(ctx, conn, acme.Options{Dir: p.CertificateDir(), CertFile: p.CertFile, KeyFile: p.KeyFile})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := certificates.FallbackPin()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	service := &migration.Service{DB: conn, ReadDB: conn, DataDir: p.DataDir, Version: version, Pin: pin, Signer: certificates.FallbackSigner()}
	authService, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(authService, httpapi.Options{DB: conn, Migration: service})
	for _, mode := range []string{"interrupted", "source-write", "checksum", "local-readiness"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "local-readiness" || r.URL.Path != migration.Prefix+"/claim" {
					api.ServeHTTP(w, r)
					return
				}
				if mode == "source-write" {
					api.ServeHTTP(interruptedMigrationWriter{w}, r)
					return
				}
				captured := httptest.NewRecorder()
				api.ServeHTTP(captured, r)
				for k, v := range captured.Header() {
					w.Header()[k] = v
				}
				body := captured.Body.Bytes()
				if captured.Code == 200 && mode == "checksum" {
					body = corruptSnapshotChecksum(t, body)
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				} else if captured.Code == 200 {
					body = body[:len(body)/2]
				}
				w.WriteHeader(captured.Code)
				_, _ = w.Write(body)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
			server.StartTLS()
			defer server.Close()
			grant, err := service.Issue(ctx, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "new-master")
			p := app.Paths{DataDir: dir, Database: filepath.Join(dir, "boxpanel.db"), CertFile: filepath.Join(dir, "certs/panel.crt"), KeyFile: filepath.Join(dir, "certs/panel.key")}
			core := singboxPaths{binary: filepath.Join(filepath.Dir(dir), "sing-box"), config: filepath.Join(dir, "sing-box.json")}
			control := &restoreControl{active: map[string]bool{}}
			runner := &restoreRunner{}
			deps := restoreDependencies{controller: control, fw: &restoreTestFirewall{}, apply: singbox.ApplyOptions{OS: "linux", Runner: runner},
				install: func(context.Context) error { return nil }, ready: func(context.Context, app.Paths) error { return errors.New("test local HTTPS failure") }}
			err = receiveMigration(ctx, p, core, grant.SourceURL, pin, grant.Token, "9.9.9.9", "", io.Discard, deps)
			if err == nil {
				t.Fatal("an incomplete migration succeeded")
			}
			if mode == "local-readiness" {
				if !strings.Contains(err.Error(), "test local HTTPS failure") || runner.checks != 1 || control.active[singbox.PanelUnit] || control.active[singbox.SingboxUnit] {
					t.Fatal("failed restore did not validate config and roll back services", err, control.events)
				}
			} else if runner.checks != 0 {
				t.Fatal("an invalid archive reached restore/apply")
			}
			if _, err := os.Stat(p.DataDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed migration left a live data directory", err)
			}
			if status, err := service.PanelStatus(ctx); err != nil || status.Phase != "idle" {
				t.Fatal("old master failed to unlock after target failure", status, err)
			}
			if release, err := service.BeginWrite(ctx); err != nil {
				t.Fatal("old master is still read-only", err)
			} else {
				release()
			}
		})
	}
}

type interruptedMigrationWriter struct{ http.ResponseWriter }

func (w interruptedMigrationWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (w interruptedMigrationWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func corruptSnapshotChecksum(t *testing.T, data []byte) []byte {
	t.Helper()
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Error(err)
		return nil
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var out bytes.Buffer
	zip := gzip.NewWriter(&out)
	writer := tar.NewWriter(zip)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Error(err)
			return nil
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Error(err)
			return nil
		}
		if header.Name == "boxpanel.db" {
			body[0] ^= 0xff
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Error(err)
			return nil
		}
		if _, err := writer.Write(body); err != nil {
			t.Error(err)
			return nil
		}
	}
	if err := errors.Join(writer.Close(), zip.Close()); err != nil {
		t.Error(err)
		return nil
	}
	return out.Bytes()
}

func TestSetMasterCommandVerifiesIdentityBeforeSaving(t *testing.T) {
	clearEnvironment(t)
	ctx := t.Context()
	p, _ := backupPaths(t)
	conn, err := app.OpenExisting(ctx, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	master := agent.NewMaster(conn, nil, nil)
	authService, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(authService, httpapi.Options{DB: conn, Agents: master})
	endpoint := httptest.NewTLSServer(api)
	defer endpoint.Close()
	spki := endpoint.Certificate().RawSubjectPublicKeyInfo
	pin := certs.SPKIPin(spki)
	_, token, err := master.IssueToken(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	nodeDir := t.TempDir()
	node := app.Paths{DataDir: nodeDir, Database: filepath.Join(nodeDir, "boxpanel.db")}
	if _, err := app.JoinNode(ctx, node, endpoint.URL, pin, token, "node", "8.8.8.8", version); err != nil {
		t.Fatal(err)
	}
	nodeDB, err := app.OpenExisting(ctx, node.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer nodeDB.Close()
	before, err := db.GetSetting(ctx, nodeDB, db.AgentMaster)
	if err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer denied.Close()
	var output bytes.Buffer
	if code := run([]string{"set-master", denied.URL, "--data-dir", nodeDir}, io.Discard, &output); code != 1 {
		t.Fatal("set-master accepted a target that rejected the signed heartbeat", output.String())
	}
	otherCertificate, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := httptest.NewUnstartedServer(api)
	untrusted.TLS = &tls.Config{Certificates: []tls.Certificate{otherCertificate}}
	untrusted.StartTLS()
	defer untrusted.Close()
	if code := run([]string{"set-master", untrusted.URL, "--data-dir", nodeDir}, io.Discard, &output); code != 1 {
		t.Fatal("set-master trusted an unrelated self-signed certificate", output.String())
	}
	if after, err := db.GetSetting(ctx, nodeDB, db.AgentMaster); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed set-master modified local addresses or identity", err)
	}
	verified := httptest.NewTLSServer(api)
	defer verified.Close()
	if code := run([]string{"set-master", "--data-dir", nodeDir, verified.URL}, &output, &output); code != 0 {
		t.Fatal("set-master could not verify a valid endpoint", output.String())
	}
	after, err := db.GetSetting(ctx, nodeDB, db.AgentMaster)
	if err != nil || after.MasterURL != verified.URL+"/" || !after.SameIdentity(before) || !reflect.DeepEqual(after.AlternateURLs, []string{before.MasterURL}) {
		t.Fatal("successful set-master did not retain identity and the previous address", err)
	}
}

func TestDomainMigrationRetainsDomainAndServesPinnedIP(t *testing.T) {
	ctx := t.Context()
	p, _ := backupPaths(t)
	conn, err := app.OpenExisting(ctx, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const domain = "panel.example.com"
	if _, err := app.SetPublicHost(ctx, conn, domain); err != nil {
		t.Fatal(err)
	}
	certificates, err := acme.New(ctx, conn, acme.Options{Dir: p.CertificateDir(), CertFile: p.CertFile, KeyFile: p.KeyFile})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := certificates.FallbackPin()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := migration.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	const target = "https://9.9.9.9:2053/panel/"
	if _, err := migration.PrepareTarget(ctx, p.DataDir, "https://panel.example.com:2053/panel/", target, secret, pin, 1); err != nil {
		t.Fatal(err)
	}
	cfg, err := app.LoadConfig(ctx, conn)
	if err != nil || cfg.PublicHost != domain {
		t.Fatal("domain mode did not preserve the public name", err)
	}
	getCert, err := migrationCertificates(ctx, conn, certificates, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, want string }{{domain, domain}, {"", "9.9.9.9"}} {
		pair, err := getCert(&tls.ClientHelloInfo{ServerName: test.name})
		if err != nil || pair.Leaf.VerifyHostname(test.want) != nil || certs.SPKIPin(pair.Leaf.RawSubjectPublicKeyInfo) != pin {
			t.Fatal("hostname or fallback identity changed after a domain migration", test, err)
		}
	}
}
