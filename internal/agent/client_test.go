package agent

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
)

func TestLegacyMasterAllowsFallbackHeartbeatWithoutApplying(t *testing.T) {
	var currentJoins, legacyJoins, legacyBeats, reportedOutdated atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode node request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case Prefix + "/join":
			if string(body["protocol"]) == "2" {
				currentJoins.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			legacyJoins.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"server_id":2}`))
		case Prefix + "/heartbeat":
			if string(body["sync_error"]) == `"master_outdated"` {
				reportedOutdated.Add(1)
			}
			if body["protocol"] != nil || body["interfaces"] != nil || body["sync_error"] != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			legacyBeats.Add(1)
			_, _ = w.Write([]byte(`{"desired_generation":12}`))
		default:
			t.Errorf("incompatible node called %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	client, err := NewClient(server.URL, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id, err := client.Join(ctx, "token", pub, "edge", "8.8.8.8", "current")
	if err != nil || id.ServerID != 2 || currentJoins.Load() != 1 || legacyJoins.Load() != 1 {
		t.Fatal("registration did not negotiate the legacy request shape", err)
	}
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := db.SetSetting(ctx, conn, db.AgentAppliedGeneration, int64(7)); err != nil {
		t.Fatal(err)
	}
	node := Node{Reader: conn, Writer: conn, Client: client, Key: key, Master: db.AgentConnection{ServerID: id.ServerID}}
	beat := Heartbeat{Version: "current", SingboxActive: true, Interfaces: []string{"eth0"}}
	for range 2 {
		if err := node.Sync(ctx, beat, func() bool { return true }); !errors.Is(err, ErrMasterOutdated) {
			t.Fatalf("legacy sync: %v", err)
		}
	}
	applied, err := db.GetSetting(ctx, conn, db.AgentAppliedGeneration)
	if err != nil || applied != 7 || legacyBeats.Load() != 3 || reportedOutdated.Load() != 2 {
		t.Fatalf("legacy contact or existing config changed: applied=%d beats=%d reports=%d err=%v", applied, legacyBeats.Load(), reportedOutdated.Load(), err)
	}
}

func TestClientUsesCAAndDoesNotForwardRegistrationOnRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == Prefix+"/join" {
			http.Redirect(w, r, target.URL+"/join", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tlsConfig := client.http.Transport.(*http.Transport).TLSClientConfig
	tlsConfig.RootCAs = x509.NewCertPool()
	tlsConfig.RootCAs.AddCert(server.Certificate())
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Heartbeat(t.Context(), 2, private, Heartbeat{Version: "test"}); err != nil {
		t.Fatal("CA-trusted connection failed", err)
	}
	if _, err := client.Join(t.Context(), "test-token", public, "test", "", "test"); err == nil || redirected.Load() {
		t.Fatal("registration followed the redirect")
	}
}

func TestInvalidMasterURLs(t *testing.T) {
	for _, invalid := range []string{
		"http://master.test/panel/", "https://admin@master.test/panel", "https://master.test:65536/panel",
		"https://master.test/other/panel", "https://master.test/%70anel", "https://master.test/panel?",
		"https://master.test/panel#",
	} {
		if _, err := NewClient(invalid, ""); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestClientTrustDuringCertificateSwitches(t *testing.T) {
	now := time.Now()
	serial := int64(0)
	issue := func(key *ecdsa.PrivateKey, parent *tls.Certificate, ip string, before, after time.Time) tls.Certificate {
		t.Helper()
		serial++
		cert := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test certificate"},
			NotBefore: before, NotAfter: after, BasicConstraintsValid: true, IsCA: ip == "",
			KeyUsage: x509.KeyUsageDigitalSignature,
		}
		if ip == "" {
			cert.KeyUsage |= x509.KeyUsageCertSign
		} else {
			cert.IPAddresses = []net.IP{net.ParseIP(ip)}
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		issuer, signer := cert, key
		if parent != nil {
			issuer, signer = parent.Leaf, parent.PrivateKey.(*ecdsa.PrivateKey)
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, issuer, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Leaf: leaf, Certificate: [][]byte{der}, PrivateKey: key}
	}
	newKey := func() *ecdsa.PrivateKey {
		t.Helper()
		key, err := certs.GenerateTLSKey()
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	before, after := now.Add(-time.Hour), now.Add(time.Hour)
	root := issue(newKey(), nil, "", before, after)
	intermediate := issue(newKey(), &root, "", before, after)
	acme := issue(newKey(), &intermediate, "127.0.0.1", before, after)
	acme.Certificate = append(acme.Certificate, intermediate.Certificate[0])
	wrongHost := issue(newKey(), &intermediate, "127.0.0.2", before, after)
	wrongHost.Certificate = append(wrongHost.Certificate, intermediate.Certificate[0])
	key := newKey()
	fallback := issue(key, nil, "127.0.0.1", before, after)
	expired := issue(key, nil, "127.0.0.1", before, now.Add(-time.Minute))
	future := issue(key, nil, "127.0.0.1", now.Add(time.Minute), after)
	pin := certs.SPKIPin(fallback.Leaf.RawSubjectPublicKeyInfo)
	oldPin := certs.SPKIPin([]byte("retired SPKI"))
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var serving atomic.Pointer[tls.Config]
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return serving.Load(), nil
	}}
	server.StartTLS()
	defer server.Close()
	var clients [2][3]*Client
	for trust := range clients {
		for i, savedPin := range []string{pin, oldPin, ""} {
			client, err := NewClient(server.URL, savedPin)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			cfg := client.http.Transport.(*http.Transport).TLSClientConfig
			cfg.RootCAs = x509.NewCertPool()
			cfg.RootCAs.AddCert(root.Leaf)
			if trust == 1 {
				cfg.RootCAs.AddCert(fallback.Leaf)
			}
			clients[trust][i] = client
		}
	}
	for _, step := range []struct {
		name          string
		cert          tls.Certificate
		trustFallback bool
		want          [3]bool // current pin, old pin, no pin
	}{
		{"self-signed", fallback, false, [3]bool{true, false, false}},
		{"ACME", acme, false, [3]bool{true, true, true}},
		{"untrusted fallback", fallback, false, [3]bool{true, false, false}},
		{"explicitly trusted fallback", fallback, true, [3]bool{true, true, true}},
		{"ACME recovery", acme, false, [3]bool{true, true, true}},
		{"CA accepted but host mismatch", wrongHost, false, [3]bool{}},
		{"expired pin", expired, false, [3]bool{}},
		{"not-yet-valid pin", future, false, [3]bool{}},
	} {
		t.Run(step.name, func(t *testing.T) {
			serving.Store(&tls.Config{Certificates: []tls.Certificate{step.cert}})
			trust := 0
			if step.trustFallback {
				trust = 1
			}
			for i, client := range clients[trust] {
				err := client.Heartbeat(t.Context(), 2, private, Heartbeat{Version: "test"})
				// Use a new TLS handshake after the server's next switch.
				client.Close()
				if (err == nil) != step.want[i] {
					t.Errorf("trust case %d: got %v, want success=%t", i, err, step.want[i])
				}
			}
		})
	}
}

func TestClientDetectsClockSkewOnUnauthorized(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		date string
		want int64
	}{
		{"", 0},
		{"invalid", 0},
		{now.Add(100 * time.Second).UTC().Format(http.TimeFormat), 0},
		{now.Add(-100 * time.Second).UTC().Format(http.TimeFormat), 0},
		{now.Add(101 * time.Second).UTC().Format(http.TimeFormat), 101},
		{now.Add(-101 * time.Second).UTC().Format(http.TimeFormat), 101},
	} {
		err := unauthorized(tc.date, now)
		var skew *ClockSkewError
		if tc.want == 0 && err != ErrUnauthorized || tc.want > 0 && (!errors.As(err, &skew) || skew.Seconds != tc.want) {
			t.Errorf("Date %q: %v", tc.date, err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Add(300*time.Second).Format(http.TimeFormat))
		w.WriteHeader(401)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, joinErr := client.Join(t.Context(), "test-token", public, "test", "", "test")
	for _, err := range []error{joinErr, client.Heartbeat(t.Context(), 2, private, Heartbeat{Version: "test"})} {
		var skew *ClockSkewError
		if !errors.As(err, &skew) || skew.Seconds < 299 || skew.Seconds > 300 {
			t.Fatalf("expected clock skew from 401, got %v", err)
		}
	}
}
