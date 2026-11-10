package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Offline Test CA"}, NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func signRequest(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, csr *x509.CertificateRequest, now time.Time, life time.Duration) []byte {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: csr.Subject, DNSNames: csr.DNSNames, IPAddresses: csr.IPAddresses, NotBefore: now, NotAfter: now.Add(life), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, csr.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestClientHTTP01DNSAndIP(t *testing.T) {
	for _, name := range []string{"panel.example.com", "203.0.113.8", "2001:db8::8"} {
		t.Run(name, func(t *testing.T) {
			ca, caKey := testCA(t)
			var mu sync.Mutex
			var challengeAddress, base string
			var certificate []byte
			var orderPayload struct {
				Identifiers []struct{ Type, Value string }
				Profile     string
			}
			validated, finalized := false, false
			now := time.Now().Truncate(time.Second).Add(-time.Minute)
			order := func() map[string]any {
				status := "pending"
				if validated {
					status = "ready"
				}
				if finalized {
					status = "valid"
				}
				result := map[string]any{"status": status, "identifiers": orderPayload.Identifiers, "authorizations": []string{base + "/auth"}, "finalize": base + "/finalize"}
				if orderPayload.Profile != "" {
					result["profile"] = orderPayload.Profile
				}
				if finalized {
					result["certificate"] = base + "/cert"
				}
				return result
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Replay-Nonce", fmt.Sprint(time.Now().UnixNano()))
				w.Header().Set("Content-Type", "application/json")
				var payload []byte
				if r.Method == "POST" {
					var jws struct{ Payload, Protected, Signature string }
					if err := json.NewDecoder(r.Body).Decode(&jws); err != nil || jws.Signature == "" || jws.Protected == "" {
						t.Error("missing library-signed JWS")
						w.WriteHeader(400)
						return
					}
					payload, _ = base64.RawURLEncoding.DecodeString(jws.Payload)
				}
				switch r.URL.Path {
				case "/directory":
					_ = json.NewEncoder(w).Encode(map[string]any{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/new-order", "meta": map[string]any{"termsOfService": base + "/tos", "profiles": map[string]string{"shortlived": base + "/profile", "classic": base + "/profile"}}})
				case "/nonce":
					w.WriteHeader(200)
				case "/account":
					w.Header().Set("Location", base+"/account/1")
					w.WriteHeader(201)
					_, _ = io.WriteString(w, `{"status":"valid"}`)
				case "/new-order":
					if err := json.Unmarshal(payload, &orderPayload); err != nil {
						t.Error(err)
					}
					if len(orderPayload.Identifiers) != 1 {
						t.Error("wrong identifiers")
						w.WriteHeader(400)
						return
					}
					id := orderPayload.Identifiers[0]
					if net.ParseIP(name) == nil {
						if id.Type != "dns" || orderPayload.Profile != "" {
							t.Error("DNS must use default profile", id, orderPayload.Profile)
						}
					} else if id.Type != "ip" || orderPayload.Profile != "shortlived" {
						t.Error("IP must use shortlived profile", id, orderPayload.Profile)
					}
					if id.Value != name {
						t.Error("wrong identifier value")
					}
					w.Header().Set("Location", base+"/order")
					w.WriteHeader(201)
					_ = json.NewEncoder(w).Encode(order())
				case "/auth":
					status := "pending"
					if validated {
						status = "valid"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "identifier": orderPayload.Identifiers[0], "challenges": []any{map[string]any{"type": "http-01", "url": base + "/challenge", "token": "offline-token", "status": status}, map[string]any{"type": "tls-alpn-01", "url": base + "/forbidden", "token": "forbidden"}}})
				case "/challenge":
					response, err := http.Get("http://" + challengeAddress + "/.well-known/acme-challenge/offline-token")
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					body, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode != 200 || !strings.HasPrefix(string(body), "offline-token.") {
						t.Error("missing HTTP-01 key authorization")
					}
					missing, err := http.Get("http://" + challengeAddress + "/other")
					if err != nil {
						t.Error(err)
					} else {
						missing.Body.Close()
						if missing.StatusCode != 404 {
							t.Error("challenge server leaked other paths")
						}
					}
					validated = true
					_, _ = io.WriteString(w, `{"status":"valid","type":"http-01"}`)
				case "/order":
					_ = json.NewEncoder(w).Encode(order())
				case "/finalize":
					var value struct{ CSR string }
					_ = json.Unmarshal(payload, &value)
					der, _ := base64.RawURLEncoding.DecodeString(value.CSR)
					csr, err := x509.ParseCertificateRequest(der)
					if err != nil || csr.CheckSignature() != nil {
						t.Error("invalid CSR")
						w.WriteHeader(400)
						return
					}
					if net.ParseIP(name) == nil {
						if len(csr.DNSNames) != 1 || csr.DNSNames[0] != name || len(csr.IPAddresses) != 0 {
							t.Error("DNS SAN mismatch")
						}
					} else if len(csr.IPAddresses) != 1 || csr.IPAddresses[0].String() != name || len(csr.DNSNames) != 0 {
						t.Error("IP SAN mismatch")
					}
					certificate = signRequest(t, ca, caKey, csr, now, 160*time.Hour)
					finalized = true
					_ = json.NewEncoder(w).Encode(order())
				case "/cert":
					w.Header().Set("Content-Type", "application/pem-certificate-chain")
					_, _ = w.Write(certificate)
				default:
					t.Error("unexpected ACME request", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			base = server.URL
			dir := t.TempDir()
			client := &Client{Directory: base + "/directory", Dir: dir, HTTPClient: server.Client(), Listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				if address != ":80" || network != "tcp" {
					t.Error("wrong challenge listener", network, address)
				}
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err == nil {
					challengeAddress = listener.Addr().String()
				}
				return listener, err
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			material, err := client.Issue(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseMaterial(material, name, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !validated || !finalized {
				t.Fatal("incomplete ACME exchange")
			}
			connection, err := net.DialTimeout("tcp", challengeAddress, 100*time.Millisecond)
			if err == nil {
				connection.Close()
				t.Fatal("HTTP-01 listener left open")
			}
			files, _ := filepath.Glob(filepath.Join(dir, "account-*.key"))
			if len(files) != 1 {
				t.Fatal(files)
			}
			info, _ := os.Stat(files[0])
			if info.Mode().Perm() != 0600 {
				t.Fatal("insecure account key")
			}
			first, _ := os.ReadFile(files[0])
			if _, err := accountKey(dir, client.Directory); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(files[0])
			if string(first) != string(second) {
				t.Fatal("account key replaced")
			}
		})
	}
}

func TestClientOccupiedPortNeverContactsCA(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client := &Client{Directory: "http://127.0.0.1:1/directory", Dir: t.TempDir(), Listen: func(context.Context, string, string) (net.Listener, error) {
		return net.Listen("tcp", listener.Addr().String())
	}}
	_, err = client.Issue(context.Background(), "panel.example.com")
	if !errors.Is(err, ErrPort) {
		t.Fatal(err)
	}
	for _, name := range []string{"127.0.0.1", "192.168.1.1", "localhost", "https://example.com"} {
		if _, err := client.Issue(context.Background(), name); !errors.Is(err, ErrHost) {
			t.Fatal(name, err)
		}
	}
}
