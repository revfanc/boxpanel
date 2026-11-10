package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/web"
)

func pageFixture() (Data, time.Time) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	return Data{PanelName: "boxpanel", PublicHost: "panel.example.com", WebPort: 2053, TLSMode: "acme", SubPath: "subscribe123",
		User: User{User: confgen.User{Name: "alice", Enabled: true, TrafficUp: 1536 * 1024 * 1024, TrafficDown: 9 * 1024 * 1024 * 1024, TrafficLimit: 100 * 1024 * 1024 * 1024, ExpireAt: now.Add(30 * 24 * time.Hour).Unix()}, Token: "test-only-subscription-token"}}, now
}

func TestPageGolden(t *testing.T) {
	d, now := pageFixture()
	got, err := Page(d, "fixed-test-nonce", now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "page.golden")
	if *update {
		if err := os.WriteFile(path, got, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("page golden mismatch; go test ./internal/subscription -run TestPageGolden -update")
	}
}

func TestPageUsageExpiryAndStatus(t *testing.T) {
	d, now := pageFixture()
	text := web.SubscriptionText()
	for _, tt := range []struct {
		name   string
		change func(*Data)
		want   []string
	}{
		{"active", func(*Data) {}, []string{text["active"], "10.50 GB", "100.00 GB", `value="10.5"`, "2026-12-09（UTC）", "剩余 30 天"}},
		{"unlimited", func(d *Data) { d.User.TrafficLimit = 0; d.User.ExpireAt = 0 }, []string{text["unlimited"], text["never"], `value="0"`}},
		{"less-than-day", func(d *Data) { d.User.ExpireAt = now.Unix() + 1 }, []string{"剩余 1 天"}},
		{"expired", func(d *Data) { d.User.ExpireAt = now.Unix() }, []string{text["expired"], "剩余 0 天"}},
		{"disabled-first", func(d *Data) { d.User.Enabled = false; d.User.ExpireAt = now.Unix(); d.User.TrafficLimit = 1 }, []string{`class="badge">` + text["disabled"]}},
		{"expired-before-exhausted", func(d *Data) { d.User.ExpireAt = now.Unix(); d.User.TrafficLimit = 1 }, []string{`class="badge">` + text["expired"]}},
		{"exhausted", func(d *Data) { d.User.TrafficLimit = d.User.TrafficUp + d.User.TrafficDown }, []string{text["exhausted"], `value="100"`}},
		{"overflow", func(d *Data) {
			d.User.TrafficUp = math.MaxInt64
			d.User.TrafficDown = math.MaxInt64
			d.User.TrafficLimit = math.MaxInt64
		}, []string{text["exhausted"], `value="100"`, "16777216.00 TB"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := d
			tt.change(&input)
			got, err := Page(input, "test-nonce", now.In(time.FixedZone("test", -7*60*60)))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(string(got), want) {
					t.Fatalf("missing %q", want)
				}
			}
		})
	}
	for _, tt := range []struct {
		n    uint64
		want string
	}{
		{0, "0 B"}, {1, "1 B"}, {1023, "1023 B"}, {1024, "1.00 KB"}, {1536, "1.50 KB"}, {1 << 20, "1.00 MB"}, {1 << 30, "1.00 GB"}, {1 << 40, "1.00 TB"},
	} {
		if got := pageBytes(tt.n); got != tt.want {
			t.Errorf("pageBytes(%d) = %s, want %s", tt.n, got, tt.want)
		}
	}
}

func TestPageNonceResourcesAndEscaping(t *testing.T) {
	h, store := publicFixture(t)
	seen := map[string]bool{}
	for _, method := range []string{"GET", "GET", "HEAD"} {
		w := publicRequest(h, method, "/subscribe123/token-that-must-not-leak", map[string]string{"User-Agent": "Mozilla/5.0", "Accept": "text/html"})
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		match := regexp.MustCompile(`script-src 'nonce-([^']+)'`).FindStringSubmatch(csp)
		if len(match) != 2 {
			t.Fatal("missing nonce CSP")
		}
		nonce := match[1]
		raw, err := base64.RawURLEncoding.DecodeString(nonce)
		if err != nil || len(raw) != 32 || seen[nonce] {
			t.Fatal("invalid or reused nonce")
		}
		seen[nonce] = true
		for _, directive := range []string{"default-src 'none'", "img-src data:", "style-src 'unsafe-inline'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("missing CSP %s", directive)
			}
		}
		if method == "HEAD" {
			if w.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
			continue
		}
		html := w.Body.String()
		if !strings.Contains(html, `<script nonce="`+nonce+`">`) || strings.Count(html, "<script") != 1 || !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatal("nonce mismatch or unsafe escaping")
		}
		if strings.Contains(html, "#ZgotmplZ") || strings.Contains(html, "ZgotmplZ") {
			t.Fatal("invalid template URL or CSS")
		}
		// The sole fetched resource must be the local PNG; external URLs are
		// clickable download anchors only, never src/link/CSS/script requests.
		for _, attr := range regexp.MustCompile(`(?i)\b(?:src|srcset|poster|background)="([^"]*)"`).FindAllStringSubmatch(html, -1) {
			if !strings.HasPrefix(attr[1], "data:image/png;base64,") {
				t.Fatalf("external resource: %s", attr[0])
			}
		}
		for _, forbidden := range []string{"<link", "<iframe", "<object", "<embed", "<base", "@import", "url(", "fetch(", "XMLHttpRequest", "sendBeacon", " onclick=", " onload="} {
			if strings.Contains(html, forbidden) {
				t.Fatalf("unexpected resource/event %q", forbidden)
			}
		}
		allowed := map[string]bool{}
		for _, c := range web.SubscriptionClients() {
			allowed[c.Download] = true
		}
		for _, a := range regexp.MustCompile(`<a\b[^>]*href="([^"]*)"`).FindAllStringSubmatch(html, -1) {
			if !allowed[a[1]] && !strings.HasPrefix(a[1], "sing-box://") && !strings.HasPrefix(a[1], "clash://") {
				t.Fatalf("unexpected link %s", a[1])
			}
		}
	}
	// Browser requests must preserve the established inactive-user 404 disguise.
	for _, assignment := range []string{"enabled=0", "enabled=1,expire_at=1", "expire_at=0,traffic_limit=1,traffic_up=1"} {
		if _, err := store.DB.ExecContext(context.Background(), "UPDATE users SET "+assignment); err != nil {
			t.Fatal(err)
		}
		w := publicRequest(h, "GET", "/subscribe123/token-that-must-not-leak", map[string]string{"User-Agent": "Mozilla/5.0", "Accept": "text/html"})
		if w.Code != 404 || w.Body.Len() != 0 || w.Header().Get("Subscription-Userinfo") != "" {
			t.Fatal("inactive HTML exposed")
		}
	}
}

func TestPageThemeTokensMatchPanel(t *testing.T) {
	css, err := os.ReadFile("../../web/src/index.css")
	if err != nil {
		t.Fatal(err)
	}
	panels := regexp.MustCompile(`(?s)(?::root|\.dark) \{(.*?)\n\}`).FindAllStringSubmatch(string(css), -1)
	pages := strings.Split(pageSource, "@media (prefers-color-scheme: dark)")
	if len(panels) != 2 || len(pages) != 2 {
		t.Fatal("missing light/dark themes")
	}
	tokens := regexp.MustCompile(`--[\w-]+: [^;]+;`)
	for i, block := range pages {
		if i == 1 {
			block = strings.Split(block, "* { box-sizing")[0]
		}
		for _, token := range tokens.FindAllString(block, -1) {
			if !strings.Contains(panels[i][1], token) {
				t.Error(fmt.Sprintf("theme %d differs from panel: %s", i, token))
			}
		}
	}
}
