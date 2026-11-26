package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/rules"
	"github.com/revfanc/boxpanel/internal/spa"
)

var ErrMasterOutdated = errors.New("主控版本过旧，请先升级主控，节点会继续使用上次生效的配置")
var ErrMasterUnreachable = errors.New("无法连接主控，请检查 HTTPS 地址、网络和证书信任或指纹")

type Client struct {
	base string
	pin  string
	http *http.Client
}

func NormalizeMasterURL(masterURL string) (string, error) {
	u, err := url.Parse(masterURL)
	if err != nil || len(masterURL) > 512 || u.Scheme != "https" || !rules.ValidHost(u.Hostname()) || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(masterURL, "#") || u.RawPath != "" || strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("主控地址无效，请复制主控面板的 HTTPS 地址")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("主控端口无效，请复制主控面板的 HTTPS 地址")
		}
	}
	if err := spa.ValidateWebPath(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")); err != nil || u.Path == "//" {
		return "", fmt.Errorf("主控路径无效，请复制主控面板首页的完整地址")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/"
	return u.String(), nil
}

// MasterHTTPClient provides the node trust policy for other master-to-master
// HTTPS calls. It uses the URL's hostname for the system CA verification branch.
func MasterHTTPClient(masterURL, pin string, timeout time.Duration) (*http.Client, error) {
	u, err := url.Parse(masterURL)
	if err != nil {
		return nil, fmt.Errorf("parse master TLS address: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if pin != "" {
		expected := decodeBytes(pin, sha256.Size)
		if expected == nil {
			return nil, fmt.Errorf("主控证书指纹格式有误，请重新复制加入命令")
		}
		// Defer verification to the callback so either trust anchor can be
		// used. The TLS handshake still proves possession of the leaf key.
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("主控未提供证书")
			}
			leaf := state.PeerCertificates[0]
			hash := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
			now := time.Now()
			if subtle.ConstantTimeCompare(expected, hash[:]) == 1 && !now.Before(leaf.NotBefore) && !now.After(leaf.NotAfter) {
				return nil
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := leaf.Verify(x509.VerifyOptions{
				DNSName: u.Hostname(), Roots: tlsConfig.RootCAs, Intermediates: intermediates, CurrentTime: now,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		}
	}
	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func NewClient(masterURL, pin string) (*Client, error) {
	base, err := NormalizeMasterURL(masterURL)
	if err != nil {
		return nil, err
	}
	client, err := MasterHTTPClient(base, pin, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{base: strings.TrimSuffix(base, "/"), pin: pin, http: client}, nil
}

func (c *Client) MasterURL() string { return c.base + "/" }
func (c *Client) Close()            { c.http.CloseIdleConnections() }

type ClockSkewError struct {
	Seconds int64
}

func (e *ClockSkewError) Error() string {
	return fmt.Sprintf("系统时间与主控相差 %d 秒，请启用 NTP（timedatectl set-ntp true）", e.Seconds)
}

func unauthorized(date string, now time.Time) error {
	master, err := http.ParseTime(date)
	if err == nil {
		seconds := master.Unix() - now.Unix()
		if seconds < 0 {
			seconds = -seconds
		}
		if seconds > 100 {
			return &ClockSkewError{Seconds: seconds}
		}
	}
	return ErrUnauthorized
}

func (c *Client) Join(ctx context.Context, token string, key ed25519.PublicKey, hostname, publicIP, version string) (Identity, error) {
	var id Identity
	in := Registration{Token: token, PublicKey: encoding.EncodeToString(key), Hostname: hostname, PublicIP: publicIP, Version: version, Protocol: ProtocolVersion}
	resp, err := c.post(ctx, "/join", in, 0, nil)
	if err != nil {
		return id, err
	}
	if resp.StatusCode == http.StatusBadRequest {
		// Pre-negotiation binaries reject the unknown protocol field before
		// consuming a token. Retry registration with their original shape.
		resp.Body.Close()
		in.Protocol = 0
		resp, err = c.post(ctx, "/join", in, 0, nil)
		if err != nil {
			return id, err
		}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&id)
		if err != nil || id.ServerID <= 0 {
			return Identity{}, fmt.Errorf("主控返回的服务器编号无效，请在主控检查注册记录后重新生成加入命令")
		}
		return id, nil
	case http.StatusUnauthorized:
		return id, unauthorized(resp.Header.Get("Date"), time.Now())
	case http.StatusUnprocessableEntity:
		return id, ErrPublicIP
	case http.StatusTooManyRequests:
		return id, fmt.Errorf("加入请求过多，请等待一分钟后重试")
	default:
		return id, fmt.Errorf("加入请求失败（HTTP %d），请核对主控地址与版本后重试", resp.StatusCode)
	}
}

func (c *Client) Heartbeat(ctx context.Context, id int64, key ed25519.PrivateKey, heartbeat Heartbeat) error {
	if heartbeat.Protocol == 0 {
		heartbeat.Protocol = ProtocolVersion
	}
	resp, err := c.post(ctx, "/heartbeat", heartbeat, id, key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return unauthorized(resp.Header.Get("Date"), time.Now())
	default:
		return fmt.Errorf("heartbeat returned HTTP %d", resp.StatusCode)
	}
}

// SyncHeartbeat exchanges the applied and desired generations.
func (c *Client) SyncHeartbeat(ctx context.Context, id int64, key ed25519.PrivateKey, in Heartbeat) (HeartbeatReply, error) {
	return c.syncHeartbeat(ctx, id, key, in, false)
}

func (c *Client) ProbeHeartbeat(ctx context.Context, id int64, key ed25519.PrivateKey, in Heartbeat) (HeartbeatReply, error) {
	return c.syncHeartbeat(ctx, id, key, in, true)
}

const HeaderMasterProbe = "X-Boxpanel-Master-Probe"

func (c *Client) syncHeartbeat(ctx context.Context, id int64, key ed25519.PrivateKey, in Heartbeat, probe bool) (HeartbeatReply, error) {
	var reply HeartbeatReply
	send := func() (*http.Response, error) {
		body, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encode heartbeat: %w", err)
		}
		return c.requestWithProbe(ctx, http.MethodPost, "/heartbeat", body, id, key, probe)
	}
	resp, err := send()
	if err != nil {
		return reply, err
	}
	if resp.StatusCode == http.StatusBadRequest && (in.Metrics != nil || in.RestartResult != nil) {
		// T9.4 and earlier use strict JSON, including masters on protocol 2.
		// Drop only the new fields first so existing sync can continue.
		resp.Body.Close()
		in.Metrics, in.RestartResult = nil, nil
		resp, err = send()
		if err != nil {
			return reply, err
		}
	}
	legacy := resp.StatusCode == http.StatusBadRequest && in.Protocol != 0
	if legacy {
		// Old masters also reject new sync error codes. Keep contact with
		// them after attempting the modern heartbeat.
		resp.Body.Close()
		in.Protocol, in.Interfaces, in.SyncError = 0, nil, ""
		resp, err = send()
		if err != nil {
			return reply, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return reply, nil
	}
	if err := nodeResponse(resp, http.StatusOK); err != nil {
		return reply, err
	}
	if err := decodeResponse(resp, 8192, &reply); err != nil || reply.DesiredGeneration <= 0 {
		return HeartbeatReply{}, fmt.Errorf("invalid desired generation in heartbeat response")
	}
	if legacy {
		reply.Protocol = 1
	}
	if len(reply.MasterURLs) > 0 && (resp.Request == nil || !VerifyMasterURLs(reply, c.pin, resp.Request.Header.Get(HeaderNonce))) {
		reply.MasterURLs = nil
	}
	return reply, nil
}

func (c *Client) Desired(ctx context.Context, id int64, key ed25519.PrivateKey) (Desired, error) {
	var out Desired
	resp, err := c.request(ctx, http.MethodGet, "/desired", nil, id, key)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if err := nodeResponse(resp, http.StatusOK); err != nil {
		return out, err
	}
	const maxSnapshot = 32 << 20
	if err := decodeResponse(resp, maxSnapshot, &out); err != nil || out.Generation <= 0 || out.Inbounds == nil || out.Users == nil {
		return Desired{}, fmt.Errorf("invalid desired state response")
	}
	return out, nil
}

func (c *Client) PublishKeys(ctx context.Context, id int64, key ed25519.PrivateKey, in InboundKeys) error {
	resp, err := c.post(ctx, "/inbound-keys", in, id, key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nodeResponse(resp, http.StatusNoContent)
}

func (c *Client) ReportTraffic(ctx context.Context, id int64, key ed25519.PrivateKey, report TrafficReport) error {
	resp, err := c.post(ctx, "/traffic", report, id, key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return nodeResponse(resp, http.StatusNoContent)
}

func (c *Client) ReportInterfaceSamples(ctx context.Context, id int64, key ed25519.PrivateKey, report InterfaceSamples) error {
	resp, err := c.post(ctx, "/interface-samples", report, id, key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nodeResponse(resp, http.StatusNoContent)
}

func (c *Client) ReportLogs(ctx context.Context, id int64, key ed25519.PrivateKey, report LogReport) error {
	resp, err := c.post(ctx, "/logs", report, id, key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrLogInactive
	}
	return nodeResponse(resp, http.StatusNoContent)
}

func nodeResponse(resp *http.Response, want int) error {
	switch resp.StatusCode {
	case want:
		return nil
	case http.StatusUnauthorized:
		return unauthorized(resp.Header.Get("Date"), time.Now())
	case http.StatusNotFound:
		return ErrMasterOutdated
	case http.StatusUpgradeRequired:
		return ErrAgentUpgradeRequired
	default:
		return fmt.Errorf("node request returned HTTP %d", resp.StatusCode)
	}
}

func decodeResponse(resp *http.Response, limit int64, out any) error {
	r := &io.LimitedReader{R: resp.Body, N: limit + 1}
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(out); err != nil || decoder.Decode(new(any)) != io.EOF || r.N <= 0 {
		return fmt.Errorf("invalid node response body")
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, value any, id int64, key ed25519.PrivateKey) (*http.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode node request: %w", err)
	}
	return c.request(ctx, http.MethodPost, path, body, id, key)
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, id int64, key ed25519.PrivateKey) (*http.Response, error) {
	return c.requestWithProbe(ctx, method, path, body, id, key, false)
}

func (c *Client) requestWithProbe(ctx context.Context, method, path string, body []byte, id int64, key ed25519.PrivateKey, probe bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+Prefix+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build node request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if probe {
		req.Header.Set(HeaderMasterProbe, "1")
	}
	if id != 0 {
		if err := sign(req, id, key, time.Now(), body); err != nil {
			return nil, err
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Avoid displaying peer-provided text, headers or request details.
		return nil, ErrMasterUnreachable
	}
	return resp, nil
}
