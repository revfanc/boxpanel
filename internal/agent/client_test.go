package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
