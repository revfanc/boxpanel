package subscription

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/spa"
)

func publicFixture(t *testing.T) (*Handler, *Store) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, x := range []struct {
		k db.SettingKey[string]
		v string
	}{{db.PublicHost, "2001:db8::8"}, {db.PanelName, "面板 <script>alert(1)</script>"}, {db.SubPath, "subscribe123"}, {db.WebPath, "panel"}, {db.PanelTLSMode, "self_signed"}} {
		if err := db.SetSetting(ctx, conn, x.k, x.v); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, conn, db.WebPort, 2053); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, conn, db.Rules, fixture(t).Rules); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO users (id,name,uuid,password,sub_token,created_at) VALUES (1,'test','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','test-password','token-that-must-not-leak',0)"); err != nil {
		t.Fatal(err)
	}
	v := confgen.RealityOptions{ServerName: "www.example.com", PrivateKey: "NEVER-IN-CLIENT", PublicKey: fixture(t).Nodes[0].Reality.PublicKey, ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome"}
	raw, _ := json.Marshal(v)
	if _, err := conn.Exec("INSERT INTO inbounds (tag,protocol,port,options,created_at,updated_at) VALUES ('vless','vless_reality',443,?,0,0)", string(raw)); err != nil {
		t.Fatal(err)
	}
	store := &Store{DB: conn, Now: func() time.Time { return time.Unix(1800000000, 0) }}
	next, err := spa.New(fstest.MapFS{"index.html": {Data: []byte("<head><!--BOXPANEL_INJECT--></head>")}}, spa.Options{WebPath: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{Store: store, Next: next, Limiter: &Limiter{}}, store
}
func publicRequest(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://evil-host.example"+path, nil)
	r.RemoteAddr = "192.0.2.1:5000"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPFormatsHeadersHTMLAnd404(t *testing.T) {
	h, _ := publicFixture(t)
	base := "/subscribe123/token-that-must-not-leak"
	for _, format := range []string{"singbox", "clash", "base64"} {
		w := publicRequest(h, "GET", base+"?format="+format, nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Subscription-Userinfo") != "upload=0; download=0; total=0; expire=0" || w.Header().Get("Profile-Update-Interval") != "12" || !strings.Contains(w.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
			t.Fatal(w.Code, w.Header())
		}
		if strings.Contains(w.Body.String(), "NEVER-IN-CLIENT") {
			t.Fatal("server private key leaked")
		}
		head := publicRequest(h, "HEAD", base+"?format="+format, nil)
		if head.Code != 200 || head.Body.Len() != 0 || !reflect.DeepEqual(head.Header(), w.Header()) {
			t.Fatal("HEAD differs")
		}
	}
	w := publicRequest(h, "GET", base, map[string]string{"User-Agent": "Mozilla/5.0", "Accept": "text/html"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data:image/png;base64,") || !strings.Contains(w.Body.String(), "sing-box://") || !strings.Contains(w.Body.String(), "clash://") || strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "evil-host") || w.Header().Get("Content-Disposition") != "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("unsafe or incomplete HTML", w.Code)
	}
	missing := publicRequest(h, "GET", "/missing/path", nil)
	for _, path := range []string{"/subscribe123/incorrect", "/subscribe123/", "/subscribe123/token-that-must-not-leak/extra", "/subscribe123/%74oken-that-must-not-leak", "/subscribe123//test"} {
		bad := publicRequest(h, "GET", path, nil)
		if bad.Code != 404 || bad.Body.String() != missing.Body.String() || !reflect.DeepEqual(bad.Header(), missing.Header()) {
			t.Fatalf("404 differs for %s: %v", path, bad.Result())
		}
	}
	if w := publicRequest(h, "GET", base+"?format=unknown", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestInactiveResetTokenAndPath(t *testing.T) {
	h, s := publicFixture(t)
	base := "/subscribe123/token-that-must-not-leak"
	for _, sql := range []string{"UPDATE users SET enabled=0", "UPDATE users SET enabled=1,expire_at=1800000000", "UPDATE users SET expire_at=0,traffic_limit=100,traffic_up=40,traffic_down=60", "UPDATE users SET traffic_limit=9223372036854775807,traffic_up=9223372036854775806,traffic_down=20"} {
		if _, err := s.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
		if w := publicRequest(h, "GET", base, nil); w.Code != 404 || w.Body.Len() != 0 {
			t.Fatal("inactive subscription served", w.Code)
		}
	}
	_, _ = s.DB.Exec("UPDATE users SET traffic_limit=0,sub_token='new-token'")
	if w := publicRequest(h, "GET", base, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := publicRequest(h, "GET", "/subscribe123/new-token", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := db.SetSetting(context.Background(), s.DB, db.SubPath, "changedpath"); err != nil {
		t.Fatal(err)
	}
	if w := publicRequest(h, "GET", "/subscribe123/new-token", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := publicRequest(h, "GET", "/changedpath/new-token", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestRateLimitAndConstantTimeTokenComparison(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{{"token", "token", true}, {"token", "Token", false}, {"token", "token\x00", false}, {"a", "", false}, {strings.Repeat("x", 32), strings.Repeat("x", 31), false}} {
		if EqualToken(tt.a, tt.b) != tt.want {
			t.Fatal(tt)
		}
	}
	h, _ := publicFixture(t)
	now := time.Unix(1, 0)
	h.Limiter.Now = func() time.Time { return now }
	for i := 0; i < 30; i++ {
		if w := publicRequest(h, "GET", "/subscribe123/bad", nil); w.Code != 404 {
			t.Fatal(i, w.Code)
		}
	}
	for _, headers := range []map[string]string{nil, {"X-Forwarded-For": "203.0.113.5", "X-Real-IP": "203.0.113.6"}} {
		w := publicRequest(h, "GET", "/subscribe123/token-that-must-not-leak", headers)
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" {
			t.Fatal(w.Code, w.Header())
		}
	}
	now = now.Add(59 * time.Second)
	if ok, retry := h.Limiter.Allow("192.0.2.1"); ok || retry != 1 {
		t.Fatal(ok, retry)
	}
	now = now.Add(time.Second)
	if ok, _ := h.Limiter.Allow("192.0.2.1"); !ok {
		t.Fatal("window did not reset")
	}
	l := &Limiter{}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("same"); ok {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 30 {
		t.Fatal("concurrent limit exceeded")
	}
	l = &Limiter{entries: map[string]bucket{}, Now: func() time.Time { return now }}
	for i := 0; i < 10000; i++ {
		l.entries[string(rune(i))] = bucket{start: now, count: 1}
	}
	if ok, _ := l.Allow("new-ip"); ok {
		t.Fatal("unbounded table")
	}
}
func TestActiveBoundaries(t *testing.T) {
	u := confgen.User{Enabled: true, TrafficUp: math.MaxInt64, TrafficDown: math.MaxInt64}
	now := time.Unix(100, 0)
	if !Active(u, now) {
		t.Fatal("unlimited overflow")
	}
	u.TrafficLimit = math.MaxInt64
	if Active(u, now) {
		t.Fatal("overflow bypass")
	}
	u = confgen.User{Enabled: true, ExpireAt: 100}
	if Active(u, now) {
		t.Fatal("expiry boundary")
	}
}
