package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/subscription"
	"github.com/revfanc/boxpanel/internal/traffic"
	"github.com/revfanc/boxpanel/web"
)

func TestTrafficResetValidationApplyAndRecovery(t *testing.T) {
	f := newResources(t, false)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality"}`, 201)
	u := decodeResult[userView](t, f.write(t, "POST", "users", `{"name":"alice","traffic_limit":100,"traffic_reset_day":28}`, 201))
	if u.TrafficResetDay != 28 || u.TrafficResetAt != 0 {
		t.Fatal(u)
	}
	for _, body := range []string{`{"traffic_reset_day":29}`, `{"traffic_reset_day":-1}`, `{"traffic_reset_day":1.5}`, `{"traffic_reset_day":null}`, `{"traffic_reset_at":1}`, `{"traffic_epoch":1}`} {
		f.call(t, "PUT", "users/1", body, 400)
	}
	f.call(t, "POST", "users/1/reset-traffic", `{}`, 400)
	f.call(t, "POST", "users/0/reset-traffic", "", 400)
	f.call(t, "POST", "users/999/reset-traffic", "", 404)
	f.call(t, "GET", "users/1/reset-traffic", "", 405)
	// Real collector + real Manager (system boundaries fake) enforce unflushed quota.
	c := runningTrafficCollector(t, f, func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{{UserID: 1, Upload: 60, Download: 70}}, nil
	})
	s := traffic.NewScheduler(c, f.apply, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	u = decodeResult[userView](t, f.call(t, "GET", "users/1", "", 200))
	if u.Status != "exhausted" || u.TrafficUp != 60 || u.TrafficDown != 70 {
		t.Fatal(u)
	}
	config, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), `"name": "alice"`) {
		t.Fatal("quota user remained in applied config")
	}
	calls := f.apply.calls
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.apply.calls != calls {
		t.Fatal("unnecessary restart")
	}
	var epoch int64
	if err := f.conn.QueryRow("SELECT traffic_epoch FROM users WHERE id=1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	f.runner.failCheck = true
	f.write(t, "POST", "users/1/reset-traffic", "", 422)
	after := decodeResult[userView](t, f.call(t, "GET", "users/1", "", 200))
	if after.TrafficUp != 60 || after.TrafficResetAt != 0 {
		t.Fatal("failed reset committed", after)
	}
	f.runner.failCheck = false
	u = decodeResult[userView](t, f.write(t, "POST", "users/1/reset-traffic", "", 200))
	if u.Status != "active" || u.TrafficUp != 0 || u.TrafficDown != 0 || u.TrafficResetAt == 0 {
		t.Fatal(u)
	}
	var next int64
	if err := f.conn.QueryRow("SELECT traffic_epoch FROM users WHERE id=1").Scan(&next); err != nil || next != epoch+1 {
		t.Fatal(next, err)
	}
	config, err = os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), `"name": "alice"`) {
		t.Fatal("reset did not restore user")
	}
	active, err := db.GetSetting(context.Background(), f.conn, db.AppliedUsers)
	if err != nil || len(active) != 1 || active[0] != 1 {
		t.Fatal(active, err)
	}
	// Expiry stops a still-applied user and extending expiry restores it via PUT.
	if _, err := f.conn.Exec("UPDATE users SET expire_at=? WHERE id=1", time.Now().Unix()-1); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, err = db.GetSetting(context.Background(), f.conn, db.AppliedUsers)
	if err != nil || len(active) != 0 {
		t.Fatal(active, err)
	}
	f.write(t, "PUT", "users/1", `{"expire_at":0,"traffic_limit":1000,"traffic_reset_day":0}`, 200)
	active, err = db.GetSetting(context.Background(), f.conn, db.AppliedUsers)
	if err != nil || len(active) != 1 {
		t.Fatal(active, err)
	}
}

func TestCollectedUsageReachesSubscriptionHeadersAndPage(t *testing.T) {
	f, store, _, _ := newSubscriptionAPI(t)
	f.write(t, "POST", "users", `{"name":"alice"}`, 201)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality","options":{"server_name":"www.example.com"}}`, 201)
	c := runningTrafficCollector(t, f, func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{{UserID: 1, Upload: 1024, Download: 2048}}, nil
	})
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := store.Load(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &subscription.Handler{Store: store, PanelPort: 2053, Limiter: &subscription.Limiter{}}
	response := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://example.test/subscribe/"+data.User.Token, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "text/html")
	handler.ServeHTTP(response, req)
	if response.Code != 200 || response.Header().Get("Subscription-Userinfo") != "upload=1024; download=2048; total=0" {
		t.Fatal(response.Code, response.Header())
	}
	if !strings.Contains(response.Body.String(), "3.00 KB") || !strings.Contains(response.Body.String(), web.SubscriptionText()["usageHint"]) {
		t.Fatal("page did not show recorded usage and approximation notice")
	}
}

func TestMonthlyResetWithEligibleUserDoesNotRestart(t *testing.T) {
	f := newResources(t, false)
	f.write(t, "POST", "inbounds", `{"protocol":"vless_reality"}`, 201)
	f.write(t, "POST", "users", `{"name":"alice","traffic_limit":1000,"traffic_reset_day":1}`, 201)
	if _, err := f.conn.Exec("UPDATE users SET traffic_up=10,traffic_down=20 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c := runningTrafficCollector(t, f, func(context.Context) ([]firewall.Counter, error) { return nil, nil })
	s := traffic.NewScheduler(c, f.apply, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checks, restarts, fwCalls := f.runner.checks, f.runner.restarts, f.firewall.calls
	f.runner.failCheck = true
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := decodeResult[userView](t, f.call(t, "GET", "users/1", "", 200))
	if u.TrafficUp != 0 || u.TrafficDown != 0 || u.TrafficResetAt != now.Unix() {
		t.Fatal("monthly reset not committed", u)
	}
	calls := f.apply.calls
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.apply.calls != calls || f.runner.checks != checks || f.runner.restarts != restarts || f.firewall.calls != fwCalls {
		t.Fatal("unchanged monthly reset restarted or repeated")
	}
}

func runningTrafficCollector(t *testing.T, f *resourceFixture, fetch traffic.FetchFunc) *traffic.Collector {
	t.Helper()
	initial := true
	c := traffic.NewCollector(f.conn, f.conn, func(ctx context.Context) ([]firewall.Counter, error) {
		if initial {
			initial = false
			return nil, nil
		}
		return fetch(ctx)
	})
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}
