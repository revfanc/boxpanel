package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
)

// All test destinations are virtual. DNS returns a public fixture address and
// the native dialer is only allowed to connect to this local TLS server.
func localSender(t *testing.T, handler http.HandlerFunc) *httpsSender {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	sender := newHTTPSSender()
	sender.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	transport := sender.client.Transport.(*http.Transport)
	t.Cleanup(transport.CloseIdleConnections)
	trusted := server.Client().Transport.(*http.Transport).TLSClientConfig
	transport.TLSClientConfig.RootCAs = trusted.RootCAs
	transport.TLSClientConfig.ServerName = "example.com" // httptest certificate
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return sender.dial(ctx, network, address, func(ctx context.Context, network, pinned string) (net.Conn, error) {
			if pinned != "8.8.8.8:443" {
				return nil, fmt.Errorf("unexpected validated destination %s", pinned)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		})
	}
	return sender
}

func TestProviderRequestsOverHTTPS(t *testing.T) {
	msg := message{Title: "配额告警", Body: "用户：alice\n时间：2030-01-01 00:00:00 UTC"}
	for _, tc := range []struct {
		channel, host, path, contentType, response string
		cfg                                        db.AlertChannel
		want                                       map[string]any
	}{
		{Bark, "example.com", "/bark/push", "application/json", `{"code":200}`, db.AlertChannel{URL: "https://example.com/bark", Credential: "bark-key"}, map[string]any{"device_key": "bark-key", "title": msg.Title, "body": msg.Body}},
		{ServerChan, "sctapi.ftqq.com", "/SCTtestkey.send", "application/x-www-form-urlencoded", `{"code":0}`, db.AlertChannel{Credential: "SCTtestkey"}, map[string]any{"title": msg.Title, "desp": msg.Body}},
		{Telegram, "api.telegram.org", "/bot123:telegram_test/sendMessage", "application/json", `{"ok":true}`, db.AlertChannel{Credential: "123:telegram_test", ChatID: "-10012345"}, map[string]any{"chat_id": float64(-10012345), "text": msg.Title + "\n" + msg.Body}},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			sender := localSender(t, func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil || r.Method != http.MethodPost || r.Host != tc.host || r.URL.Path != tc.path || r.Header.Get("Content-Type") != tc.contentType {
					t.Errorf("wrong request: %s %s %s, content type %s", r.Method, r.Host, r.URL.Path, r.Header.Get("Content-Type"))
				}
				var got map[string]any
				if tc.channel == ServerChan {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					got = map[string]any{"title": r.Form.Get("title"), "desp": r.Form.Get("desp")}
				} else if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				for key, want := range tc.want {
					if got[key] != want {
						t.Errorf("wrong %s: got %v", key, got[key])
					}
				}
				io.WriteString(w, tc.response)
			})
			if err := sender.send(context.Background(), tc.channel, tc.cfg, msg); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderFailuresNeverExposeResponseOrURL(t *testing.T) {
	const secret = "123:telegram-secret"
	cfg := db.AlertChannel{Credential: secret, ChatID: "42"}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"rejected", 200, `{"ok":false,"description":"` + secret + `"}`, errRejected},
		{"invalid", 200, "raw " + secret, errResponse},
		{"redirect", 307, secret, errHTTP},
		{"unauthorized", 401, secret, errHTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			sender := localSender(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "https://example.com/receives-no-credentials")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := sender.send(context.Background(), Telegram, cfg, message{Title: "test", Body: "hello"})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), secret) || requests.Load() != 1 {
				t.Fatalf("failure was not safely classified: %v, requests=%d", err, requests.Load())
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		sender := localSender(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		})
		sender.client.Timeout = 100 * time.Millisecond
		if err := sender.send(context.Background(), Telegram, cfg, message{}); !errors.Is(err, errTimeout) {
			t.Fatalf("send timeout = %v", err)
		}
	})
}

func TestBarkSSRFValidationAndPinnedDial(t *testing.T) {
	ctx := context.Background()
	for _, invalid := range []string{"http://example.com", "https://admin:secret@example.com", "https://example.com?token=secret", "https://example.com#test",
		"https://127.0.0.1", "https://[::1]", "https://10.1.2.3", "https://169.254.169.254", "https://[::ffff:192.168.1.1]", "https://[64:ff9b::c0a8:1]", "https://example.com/%2e%2e", "https://example.com:65536"} {
		if _, err := barkURL(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	f := newFixture(t, Options{})
	answers := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("192.168.0.5")}
	f.service.sender.lookup = func(context.Context, string) ([]netip.Addr, error) { return answers, nil }
	in := ChannelUpdate{URL: pointer("https://bark.example.com"), Credential: pointer("bark-key"), Enabled: pointer(true)}
	if _, err := f.service.SaveChannel(ctx, Bark, in); !errors.Is(err, ValidationError("bark_destination")) {
		t.Fatalf("mixed DNS response accepted: %v", err)
	}
	answers = answers[:1]
	if _, err := f.service.SaveChannel(ctx, Bark, in); err != nil {
		t.Fatal(err)
	}
	dials := 0
	checkDial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dials++
		if addr != "8.8.8.8:443" {
			t.Fatalf("dial was not pinned: %s", addr)
		}
		return nil, errors.New("stop before actual network")
	}
	_, _ = f.service.sender.dial(ctx, "tcp", "bark.example.com:443", checkDial)
	answers = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	if _, err := f.service.sender.dial(ctx, "tcp", "bark.example.com:443", checkDial); !errors.Is(err, errDestination) || dials != 1 {
		t.Fatal("changed DNS response reached dial", err, dials)
	}
}

func TestOutboxRetriesEachProviderThroughHTTPS(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		channel, credential, response string
	}{
		{Bark, "device-private-value", `{"code":200}`},
		{ServerChan, "SCTprivateSendKey", `{"code":0}`},
		{Telegram, "123456:privateBotToken", `{"ok":true}`},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			var calls atomic.Int32
			sender := localSender(t, func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					http.Error(w, "provider returned token "+tc.credential, 503)
					return
				}
				io.WriteString(w, tc.response)
			})
			f := newFixture(t, Options{})
			f.service.sender = sender
			change := ChannelUpdate{Enabled: pointer(true), Credential: pointer(tc.credential)}
			if tc.channel == Telegram {
				change.ChatID = pointer("-998877")
			}
			view, err := f.service.SaveChannel(ctx, tc.channel, change)
			if err != nil || view.CredentialMask != mask(tc.credential) || strings.Contains(view.CredentialMask, tc.credential) ||
				tc.channel == Telegram && view.ChatIDMask != "****8877" {
				t.Fatalf("unsafe configuration response %+v, %v", view, err)
			}
			f.queue(tc.channel)
			job := f.claim(tc.channel, true)
			f.finish(job, sender.send(ctx, tc.channel, job.cfg, job.message))
			f.claim(tc.channel, false)
			f.now = f.now.Add(30 * time.Second)
			job = f.claim(tc.channel, true)
			f.finish(job, sender.send(ctx, tc.channel, job.cfg, job.message))
			out, err := f.service.Overview(ctx)
			if err != nil || len(out.Recent) != 1 || out.Recent[0].Status != "sent" || out.Recent[0].Error != "" || out.Recent[0].Attempts != 2 || calls.Load() != 2 {
				t.Fatalf("unexpected retries: %+v, %v", out.Recent, err)
			}
		})
	}
}
