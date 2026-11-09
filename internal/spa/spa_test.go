package spa_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/revfanc/boxpanel/internal/spa"
)

const testIndex = `<!doctype html><html><head><!--BOXPANEL_INJECT--><script type="module" src="./assets/app.js"></script></head><body><div id="root"></div></body></html>`

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte(testIndex)},
		"assets/app.js":  {Data: []byte(`console.log("app")`)},
		"assets/app.css": {Data: []byte("body { color: black; }")},
		"robots.txt":     {Data: []byte("User-agent: *")},
		"health":         {Data: []byte("static file without extension")},
	}
}

func newHandler(t *testing.T, opts spa.Options) http.Handler {
	t.Helper()
	h, err := spa.New(testAssets(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Exercise a real HTTP round trip so deep links and refreshes use the same
// entry point as a browser, including runtime injection before relative assets.
func TestDeepLinkHTTP(t *testing.T) {
	for _, webPath := range []string{"k3m9x2p7q1ab", ""} {
		t.Run("web_path="+webPath, func(t *testing.T) {
			base := ""
			if webPath != "" {
				base = "/" + webPath
			}
			server := httptest.NewServer(newHandler(t, spa.Options{WebPath: webPath, Version: "v0.0.2"}))
			defer server.Close()
			for range 2 { // Initial navigation and refresh.
				resp, err := server.Client().Get(server.URL + base + "/users/3")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
					t.Fatalf("deep link: status=%d headers=%v", resp.StatusCode, resp.Header)
				}
				injection := `<base href="` + base + `/">` + "\n" +
					`<script>window.__BOXPANEL__={"base":"` + base + `","version":"v0.0.2"}</script>`
				want := strings.Replace(testIndex, "<!--BOXPANEL_INJECT-->", injection, 1)
				if string(body) != want {
					t.Fatalf("deep link HTML = %s, want %s", body, want)
				}
			}
		})
	}
}

func TestServingRules(t *testing.T) {
	h := newHandler(t, spa.Options{WebPath: "panel", Version: "test"})
	tests := []struct {
		name        string
		method      string
		target      string
		status      int
		location    string
		cache       string
		contentType string
		body        string
		index       bool
	}{
		{name: "1 canonical mount", target: "/panel", status: 301, location: "/panel/"},
		{name: "1 preserve query", target: "/panel?next=users%2F3", status: 301, location: "/panel/?next=users%2F3"},
		{name: "3 javascript", target: "/panel/assets/app.js?v=1", status: 200, cache: "public, max-age=31536000, immutable", contentType: "javascript", body: `console.log("app")`},
		{name: "3 stylesheet", target: "/panel/assets/app.css", status: 200, cache: "public, max-age=31536000, immutable", contentType: "text/css", body: "body { color: black; }"},
		{name: "3 non asset file", target: "/panel/robots.txt", status: 200, contentType: "text/plain", body: "User-agent: *"},
		{name: "3 extensionless file", target: "/panel/health", status: 200, body: "static file without extension"},
		{name: "3 asset HEAD", method: "HEAD", target: "/panel/assets/app.js", status: 200, cache: "public, max-age=31536000, immutable"},
		{name: "4 missing javascript", target: "/panel/assets/missing.js", status: 404},
		{name: "4 missing image", target: "/panel/missing.png", status: 404},
		{name: "4 index is not a directory", target: "/panel/index.html/", status: 404},
		{name: "4 dotted last segment", target: "/panel/users/person.json", status: 404},
		{name: "5 root", target: "/panel/", status: 200, cache: "no-store", index: true},
		{name: "5 users", target: "/panel/users", status: 200, cache: "no-store", index: true},
		{name: "5 deep link", target: "/panel/users/3?tab=links", status: 200, cache: "no-store", index: true},
		{name: "5 trailing slash", target: "/panel/users/3/", status: 200, cache: "no-store", index: true},
		{name: "5 dot in parent only", target: "/panel/team.name/users/3", status: 200, cache: "no-store", index: true},
		{name: "5 explicit index", target: "/panel/index.html", status: 200, cache: "no-store", index: true},
		{name: "5 index HEAD", method: "HEAD", target: "/panel/users/3", status: 200, cache: "no-store", contentType: "text/html"},
		{name: "API prefix boundary", target: "/panel/apiary", status: 200, cache: "no-store", index: true},
		{name: "no directory listing", target: "/panel/assets/", status: 200, cache: "no-store", index: true},
		{name: "outside root", target: "/", status: 404},
		{name: "outside path", target: "/other", status: 404},
		{name: "outside assets", target: "/assets/app.js", status: 404},
		{name: "mount prefix boundary", target: "/panelother/users/3", status: 404},
		{name: "traversal", target: "/panel/../index.html", status: 404},
		{name: "encoded traversal", target: "/panel/%2e%2e/index.html", status: 404},
		{name: "repeated slash", target: "/panel//users/3", status: 404},
		{name: "repeated root slash", target: "/panel//", status: 404},
		{name: "POST cannot receive SPA", method: "POST", target: "/panel/users/3", status: 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(method, tt.target, nil))
			if rr.Code != tt.status || rr.Header().Get("Location") != tt.location || rr.Header().Get("Cache-Control") != tt.cache {
				t.Fatalf("status=%d headers=%v", rr.Code, rr.Header())
			}
			if !strings.Contains(rr.Header().Get("Content-Type"), tt.contentType) {
				t.Errorf("Content-Type = %q, want %q", rr.Header().Get("Content-Type"), tt.contentType)
			}
			if tt.index {
				if !strings.Contains(rr.Body.String(), `<base href="/panel/">`) || !strings.Contains(rr.Body.String(), `window.__BOXPANEL__={"base":"/panel","version":"test"}`) || strings.Contains(rr.Body.String(), "<!--BOXPANEL_INJECT-->") {
					t.Errorf("missing index injection: %s", rr.Body.String())
				}
			} else if tt.status != http.StatusMovedPermanently && rr.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rr.Body.String(), tt.body)
			}
			if tt.status == http.StatusMethodNotAllowed && rr.Header().Get("Allow") != "GET, HEAD" {
				t.Error("missing allowed methods")
			}
		})
	}
}

func TestAPIRequestsNeverFallBack(t *testing.T) {
	for _, webPath := range []string{"panel", ""} {
		base := ""
		if webPath != "" {
			base = "/" + webPath
		}
		h := newHandler(t, spa.Options{WebPath: webPath})
		for _, method := range []string{"GET", "POST", "HEAD", "OPTIONS"} {
			for _, target := range []string{"/api", "/api/", "/api/v1/missing", "/api/missing.js"} {
				t.Run(webPath+method+target, func(t *testing.T) {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, httptest.NewRequest(method, base+target, nil))
					if rr.Code != 404 || rr.Header().Get("Content-Type") != "application/json; charset=utf-8" || rr.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("API 404: status=%d headers=%v", rr.Code, rr.Header())
					}
					if method == "HEAD" {
						if rr.Body.Len() != 0 {
							t.Fatal("HEAD response has a body")
						}
						return
					}
					var body struct {
						Error struct{ Code, Message, Hint string }
					}
					if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.Error.Code != "not_found" || body.Error.Message == "" || body.Error.Hint == "" {
						t.Fatalf("invalid API error: %s", rr.Body.String())
					}
				})
			}
		}
		api := http.NewServeMux()
		api.HandleFunc("POST /api/v1/example", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if r.URL.RawQuery != "test=1" || string(body) != `{"name":"test"}` {
				t.Errorf("API request changed: query=%q body=%s", r.URL.RawQuery, body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		h = newHandler(t, spa.Options{WebPath: webPath, API: api})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", base+"/api/v1/example?test=1", strings.NewReader(`{"name":"test"}`)))
		if rr.Code != 201 || rr.Body.String() != `{"ok":true}` {
			t.Fatalf("API not dispatched: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestRuntimeEscaping(t *testing.T) {
	version := "</script><script>alert(1)</script>\"&\u2028"
	h := newHandler(t, spa.Options{WebPath: "panel", Version: version})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/panel/", nil))
	body := rr.Body.String()
	if strings.Contains(body, version) || strings.Count(body, "</script>") != 2 {
		t.Fatalf("unsafe script injection: %s", body)
	}
	_, runtime, _ := strings.Cut(body, "window.__BOXPANEL__=")
	runtime, _, _ = strings.Cut(runtime, "</script>")
	var value struct{ Base, Version string }
	if err := json.Unmarshal([]byte(runtime), &value); err != nil {
		t.Fatal(err)
	}
	if value.Base != "/panel" || value.Version != version {
		t.Fatalf("runtime values changed: %+v", value)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, webPath := range []string{"/panel", "panel/", "a/b", "..", "Panel", `a"><script>`, "a?b", "a#b"} {
		if _, err := spa.New(testAssets(), spa.Options{WebPath: webPath}); err == nil {
			t.Errorf("accepted invalid web_path %q", webPath)
		}
	}
	for _, index := range []string{"", "<head></head>", "<!--BOXPANEL_INJECT--><!--BOXPANEL_INJECT-->"} {
		assets := fstest.MapFS{}
		if index != "" {
			assets["index.html"] = &fstest.MapFile{Data: []byte(index)}
		}
		if _, err := spa.New(assets, spa.Options{}); err == nil || !strings.Contains(err.Error(), "make build") {
			t.Errorf("invalid frontend must give build instructions, got %v", err)
		}
	}
}
