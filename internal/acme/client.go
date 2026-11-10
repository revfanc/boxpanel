// Package acme manages panel certificates. It never changes sing-box TLS.
package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/reality"
	xacme "golang.org/x/crypto/acme"
)

const DefaultDirectory = xacme.LetsEncryptURL

var (
	ErrPort     = errors.New("ACME HTTP-01: TCP 80 unavailable")
	ErrHost     = errors.New("ACME: public IP or DNS name required")
	ErrBusy     = errors.New("ACME: issuance already pending")
	ErrDisabled = errors.New("ACME: unavailable in development mode")
)

type Material struct{ Certificate, Key []byte }
type Issuer interface {
	Issue(context.Context, string) (Material, error)
}

// Client uses x/crypto for all ACME/JWS operations. Listen is injectable so
// tests need neither port 80 privileges nor an external certificate authority.
type Client struct {
	Directory, Dir string
	HTTPClient     *http.Client
	Listen         func(context.Context, string, string) (net.Listener, error)
}

func ValidateDirectory(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
		return fmt.Errorf("configure ACME directory: use HTTPS, or loopback HTTP for an offline test server")
	}
	return nil
}

func (c *Client) Issue(ctx context.Context, name string) (Material, error) {
	var result Material
	if !reality.ValidServer(name) {
		return result, ErrHost
	}
	directory := c.Directory
	if directory == "" {
		directory = DefaultDirectory
	}
	if err := ValidateDirectory(directory); err != nil {
		return result, err
	}
	listen := c.Listen
	if listen == nil {
		listen = (&net.ListenConfig{}).Listen
	}
	listener, err := listen(ctx, "tcp", ":80")
	if err != nil {
		return result, fmt.Errorf("listen for HTTP-01: %w", ErrPort)
	}
	defer listener.Close()
	var mu sync.RWMutex
	responses := map[string]string{}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		response, ok := responses[r.URL.Path]
		mu.RUnlock()
		if r.Method != http.MethodGet || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, response)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	account, err := accountKey(c.Dir, directory)
	if err != nil {
		return result, err
	}
	client := &xacme.Client{Key: account, DirectoryURL: directory, HTTPClient: c.HTTPClient, UserAgent: "boxpanel"}
	if _, err := client.Register(ctx, &xacme.Account{}, xacme.AcceptTOS); err != nil && !errors.Is(err, xacme.ErrAccountAlreadyExists) {
		return result, fmt.Errorf("register ACME account: %w", err)
	}
	ids := xacme.DomainIDs(name)
	var options []xacme.OrderOption
	ip := net.ParseIP(name)
	if ip != nil {
		ids = xacme.IPIDs(ip.String())
		options = append(options, xacme.WithOrderIssuanceProfile("shortlived"))
	}
	// DNS deliberately omits profile: use the CA's default (LE classic).
	order, err := client.AuthorizeOrder(ctx, ids, options...)
	if err != nil {
		return result, fmt.Errorf("create ACME order: %w", err)
	}
	for _, authURL := range order.AuthzURLs {
		auth, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return result, fmt.Errorf("read ACME authorization: %w", err)
		}
		if auth.Identifier != ids[0] {
			return result, fmt.Errorf("ACME authorization identifier mismatch")
		}
		if auth.Status == xacme.StatusValid {
			continue
		}
		var challenge *xacme.Challenge
		for _, ch := range auth.Challenges {
			if ch.Type == "http-01" {
				challenge = ch
				break
			}
		}
		if challenge == nil {
			return result, fmt.Errorf("ACME authority offers no HTTP-01 challenge")
		}
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return result, fmt.Errorf("prepare HTTP-01 response: %w", err)
		}
		path := client.HTTP01ChallengePath(challenge.Token)
		mu.Lock()
		responses[path] = response
		mu.Unlock()
		if _, err := client.Accept(ctx, challenge); err != nil {
			return result, fmt.Errorf("accept HTTP-01 challenge: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, authURL); err != nil {
			return result, fmt.Errorf("validate HTTP-01 challenge: %w", err)
		}
		mu.Lock()
		delete(responses, path)
		mu.Unlock()
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return result, fmt.Errorf("wait for ACME order: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return result, fmt.Errorf("generate certificate key: %w", err)
	}
	request := &x509.CertificateRequest{DNSNames: []string{name}}
	if ip != nil {
		request.DNSNames = nil
		request.IPAddresses = []net.IP{ip}
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, request, key)
	if err != nil {
		return result, fmt.Errorf("create certificate request: %w", err)
	}
	chain, _, err := client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return result, fmt.Errorf("finalize ACME order: %w", err)
	}
	for _, der := range chain {
		result.Certificate = append(result.Certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return result, fmt.Errorf("encode certificate key: %w", err)
	}
	result.Key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return result, nil
}

func accountKey(dir, directory string) (*ecdsa.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create ACME directory: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("account-%x.key", sha256.Sum256([]byte(directory))))
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ACME account key: %w", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("encode ACME account key: %w", err)
		}
		data = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := privateFile(path, data); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode ACME account key; restore the account file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ACME account key: %w", err)
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ACME account key must use ECDSA")
	}
	return ecdsaKey, nil
}

func privateFile(path string, data []byte) error {
	if err := (host.Native{}).WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("save certificate material: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open certificate directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync certificate directory: %w", err)
	}
	return nil
}
