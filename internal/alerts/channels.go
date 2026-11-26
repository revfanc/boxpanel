package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
)

const (
	Bark           = "bark"
	ServerChan     = "server_chan"
	Telegram       = "telegram"
	DefaultBarkURL = "https://api.day.app"
)

var channels = [...]string{Bark, ServerChan, Telegram}

func KnownChannel(channel string) bool {
	return channel == Bark || channel == ServerChan || channel == Telegram
}

type ChannelUpdate struct {
	Enabled    *bool   `json:"enabled"`
	URL        *string `json:"url"`
	Credential *string `json:"credential"`
	ChatID     *string `json:"chat_id"`
}

type ChannelView struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url,omitempty"`
	CredentialMask string `json:"credential_mask"`
	ChatIDMask     string `json:"chat_id_mask,omitempty"`
}

func mask(secret string) string {
	switch {
	case len(secret) == 0:
		return ""
	case len(secret) <= 4:
		return "****"
	default:
		return "****" + secret[len(secret)-4:]
	}
}

func channelView(cfg db.AlertChannel) ChannelView {
	return ChannelView{Enabled: cfg.Enabled, URL: cfg.URL, CredentialMask: mask(cfg.Credential), ChatIDMask: mask(cfg.ChatID)}
}

func channelReady(channel string, cfg db.AlertChannel) bool {
	return cfg.Credential != "" && (channel != Telegram || cfg.ChatID != "")
}

var (
	secretPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	serverKeyPattern = regexp.MustCompile(`^SCT[A-Za-z0-9_-]{1,253}$`)
	botTokenPattern  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	chatPattern      = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]+)$`)
	barkPathPattern  = regexp.MustCompile(`^(/[A-Za-z0-9_-]+)*/*$`)
)

func updateChannel(channel string, current db.AlertChannel, update ChannelUpdate) (db.AlertChannel, error) {
	if update.Enabled != nil {
		current.Enabled = *update.Enabled
	}
	if update.URL != nil {
		if channel != Bark {
			return current, invalid("url")
		}
		current.URL = strings.TrimRight(strings.TrimSpace(*update.URL), "/")
		if current.URL == "" {
			current.URL = DefaultBarkURL
		}
	}
	if update.Credential != nil {
		current.Credential = strings.TrimSpace(*update.Credential)
	}
	if update.ChatID != nil {
		if channel != Telegram {
			return current, invalid("chat_id")
		}
		current.ChatID = strings.TrimSpace(*update.ChatID)
	}

	pattern := secretPattern
	switch channel {
	case Bark:
		if _, err := barkURL(current.URL); err != nil {
			return current, err
		}
	case ServerChan:
		pattern = serverKeyPattern
	case Telegram:
		pattern = botTokenPattern
		if current.ChatID != "" && (len(current.ChatID) > 64 || !chatPattern.MatchString(current.ChatID)) {
			return current, invalid("chat_id")
		}
	}
	if current.Credential != "" && (len(current.Credential) > 256 || !pattern.MatchString(current.Credential)) {
		return current, invalid("credential")
	}
	if current.Enabled && !channelReady(channel, current) {
		return current, invalid("credentials_required")
	}
	return current, nil
}

func barkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || u.Scheme != "https" || u.User != nil || !reality.ValidServer(u.Hostname()) ||
		u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") || u.RawPath != "" || !barkPathPattern.MatchString(u.Path) {
		return nil, invalid("url")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, invalid("url")
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicDestination(ip) {
		return nil, invalid("url")
	}
	return u, nil
}

type deliveryError string

func (e deliveryError) Error() string { return string(e) }

const (
	errDestination     deliveryError = "unsafe_destination"
	errTimeout         deliveryError = "timeout"
	errNetwork         deliveryError = "network_error"
	errHTTP            deliveryError = "http_error"
	errResponse        deliveryError = "invalid_response"
	errRejected        deliveryError = "provider_rejected"
	errChannelDisabled deliveryError = "channel_disabled"
)

type message struct{ Title, Body string }

type httpsSender struct {
	client *http.Client
	lookup func(context.Context, string) ([]netip.Addr, error)
}

func newHTTPSSender() *httpsSender {
	s := &httpsSender{lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host+".")
	}}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	s.client = &http.Client{
		Timeout: 8 * time.Second,
		// Provider credentials can be in the URL or body. Never forward them.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			IdleConnTimeout: 60 * time.Second,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
				defer cancel()
				return s.dial(ctx, network, addr, dialer.DialContext)
			},
		},
	}
	return s
}

var specialNetworks = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicDestination(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !reality.PublicIP(ip) {
		return false
	}
	for _, network := range specialNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func (s *httpsSender) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ips := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = append(ips, ip)
	} else {
		answers, err := s.lookup(ctx, host)
		if err != nil || len(answers) == 0 {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
				return nil, errTimeout
			}
			return nil, errNetwork
		}
		ips = answers
	}
	for _, ip := range ips {
		if !publicDestination(ip) {
			return nil, errDestination
		}
	}
	return ips, nil
}

func (s *httpsSender) dial(ctx context.Context, network, addr string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errDestination
	}
	ips, err := s.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	// Connect to an already validated address while HTTP keeps the original
	// hostname for SNI and certificate verification. No proxy or second lookup.
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errNetwork
}

func (s *httpsSender) validateBark(ctx context.Context, raw string) error {
	u, err := barkURL(raw)
	if err != nil || raw == DefaultBarkURL {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = s.resolve(ctx, u.Hostname())
	return err
}

func (s *httpsSender) send(ctx context.Context, channel string, cfg db.AlertChannel, msg message) error {
	var endpoint, contentType string
	var payload []byte
	switch channel {
	case Bark:
		u, err := barkURL(cfg.URL)
		if err != nil {
			return errDestination
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/push"
		endpoint = u.String()
		contentType = "application/json"
		payload, _ = json.Marshal(map[string]string{"device_key": cfg.Credential, "title": msg.Title, "body": msg.Body})
	case ServerChan:
		endpoint = "https://sctapi.ftqq.com/" + url.PathEscape(cfg.Credential) + ".send"
		contentType = "application/x-www-form-urlencoded"
		// Escape Markdown so hostnames and usernames display as plain text.
		body := strings.NewReplacer("\\", "\\\\", "_", "\\_", "*", "\\*", "`", "\\`").Replace(msg.Body)
		payload = []byte(url.Values{"title": {msg.Title}, "desp": {body}}.Encode())
	case Telegram:
		endpoint = "https://api.telegram.org/bot" + url.PathEscape(cfg.Credential) + "/sendMessage"
		contentType = "application/json"
		chat := any(cfg.ChatID)
		if id, err := strconv.ParseInt(cfg.ChatID, 10, 64); err == nil {
			chat = id
		}
		payload, _ = json.Marshal(map[string]any{"chat_id": chat, "text": msg.Title + "\n" + msg.Body, "link_preview_options": map[string]bool{"is_disabled": true}})
	default:
		return errDestination
	}
	// A deadline of our own lets a response cut short by the timeout be told
	// apart from a provider's malformed reply: a timed-out body read can end in
	// a clean but truncated EOF instead of a timeout error.
	if s.client.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.client.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return errDestination
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := s.client.Do(req)
	if err != nil {
		// net/http errors may contain the full URL, including a provider token.
		var timeout net.Error
		switch {
		case errors.Is(err, errDestination):
			return errDestination
		case errors.Is(err, errTimeout) || errors.As(err, &timeout) && timeout.Timeout() || ctx.Err() == context.DeadlineExceeded:
			return errTimeout
		default:
			return errNetwork
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errHTTP
	}
	const maxResponse = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if ctx.Err() == context.DeadlineExceeded {
		return errTimeout
	}
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return errTimeout
		}
		return errNetwork
	}
	var result struct {
		Code *int  `json:"code"`
		OK   *bool `json:"ok"`
	}
	if len(body) > maxResponse || json.Unmarshal(body, &result) != nil {
		return errResponse
	}
	if channel == Telegram {
		if result.OK == nil {
			return errResponse
		}
		if !*result.OK {
			return errRejected
		}
		return nil
	}
	if result.Code == nil {
		return errResponse
	}
	want := 0
	if channel == Bark {
		want = 200
	}
	if *result.Code != want {
		return errRejected
	}
	return nil
}

// Never persist an arbitrary error from the HTTP transport or provider.
func failureCode(err error) string {
	var failure deliveryError
	if errors.As(err, &failure) {
		switch failure {
		case errDestination, errTimeout, errNetwork, errHTTP, errResponse, errRejected, errChannelDisabled:
			return string(failure)
		}
	}
	return string(errNetwork)
}

type ValidationError string

func (e ValidationError) Error() string { return fmt.Sprintf("invalid alert setting %s", string(e)) }

func invalid(field string) error { return ValidationError(field) }
