package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/spa"
)

func agentAPI(t *testing.T) (http.Handler, *agent.Master, *sql.DB, *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO admins (username,password_hash,created_at) VALUES ('admin',?,0)", hash); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, conn, db.PublicHost, "2001:db8::8"); err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := new(atomic.Int64)
	now.Store(time.Now().Unix())
	nodes := agent.NewMaster(conn, nil, func() time.Time { return time.Unix(now.Load(), 0) })
	api := New(service, Options{DB: conn, WebPath: "panel", PanelPort: 2053, Agents: nodes, AgentPin: func() (string, error) {
		return "fallback-test-pin", nil
	}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	assets := fstest.MapFS{"index.html": {Data: []byte("<!--BOXPANEL_INJECT-->")}}
	h, err := spa.New(assets, spa.Options{WebPath: "panel", API: api})
	if err != nil {
		t.Fatal(err)
	}
	return h, nodes, conn, now
}

func TestAgentJoinTokens(t *testing.T) {
	h, _, conn, now := agentAPI(t)
	const panel = "https://master.test/panel/api/v1/"
	const join = "https://master.test/panel/api/agent/v1/join"
	requireStatus(t, request(t, h, "POST", panel+"servers/join-tokens", `{"name":"edge"}`, nil, nil), 401)
	login := request(t, h, "POST", panel+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	requireStatus(t, request(t, h, "POST", panel+"servers/join-tokens", `{"name":"edge"}`, cookie, map[string]string{"Origin": "https://other.test"}), 403)
	requireStatus(t, request(t, h, "POST", panel+"servers/join-tokens", `{"name":" "}`, cookie, nil), 400)
	type issuedToken struct {
		agent.JoinToken
		Token     string `json:"token"`
		MasterURL string `json:"master_url"`
		MasterPin string `json:"master_pin"`
	}
	issue := func() issuedToken {
		t.Helper()
		w := request(t, h, "POST", panel+"servers/join-tokens", `{"name":"  edge  "}`, cookie, nil)
		requireStatus(t, w, 201)
		var issued issuedToken
		if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(issued.Token)
		if err != nil || len(raw) != 32 || issued.Name != "edge" || issued.ExpiresAt != now.Load()+3600 ||
			issued.MasterURL != "https://[2001:db8::8]:2053/panel/" || issued.MasterPin != "fallback-test-pin" {
			t.Fatal("unexpected token response")
		}
		var hash []byte
		if err := conn.QueryRow("SELECT token_hash FROM agent_join_tokens WHERE id=?", issued.ID).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		expected := sha256.Sum256([]byte(issued.Token))
		if string(hash) != string(expected[:]) {
			t.Fatal("stored value is not the token digest")
		}
		w = request(t, h, "GET", panel+"servers/join-tokens", "", cookie, nil)
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), issued.Token) || strings.Contains(w.Body.String(), "master_pin") {
			t.Fatal("token list exposes registration credentials")
		}
		return issued
	}
	register := func(token, publicIP string) *httptest.ResponseRecorder {
		t.Helper()
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(agent.Registration{Token: token, PublicKey: base64.RawURLEncoding.EncodeToString(public),
			Hostname: "host", PublicIP: publicIP, Version: "v0.4.0"})
		if err != nil {
			t.Fatal(err)
		}
		return request(t, h, "POST", join, string(body), nil, map[string]string{"Origin": "https://other.test"})
	}
	first := issue()
	tokenURL := func(id int64) string { return panel + "servers/join-tokens/" + strconv.FormatInt(id, 10) }
	getToken := func(id int64, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(t, h, "GET", tokenURL(id), "", cookie, nil)
		requireStatus(t, w, status)
		return w
	}
	requireStatus(t, request(t, h, "GET", tokenURL(first.ID), "", nil, nil), 401)
	for _, address := range []string{"", "10.0.0.2", "127.0.0.1", "100.64.0.10", "198.51.100.5", "::ffff:127.0.0.1", "2001:db8::5", "240.0.0.1"} {
		w := register(first.Token, address)
		requireStatus(t, w, 422)
		if !strings.Contains(w.Body.String(), "agent_public_ip_required") || !strings.Contains(w.Body.String(), "--public-ip") {
			t.Fatal("missing public IP guidance", w.Body.String())
		}
	}
	if meta := decodeResult[agent.JoinToken](t, getToken(first.ID, 200)); meta.ServerID != nil || meta.ID != first.ID {
		t.Fatal("failed registration consumed the token", meta)
	}
	registered := register(first.Token, "2606:4700::1111")
	requireStatus(t, registered, 201)
	identity := decodeResult[agent.Identity](t, registered)
	w := getToken(first.ID, 200)
	meta := decodeResult[agent.JoinToken](t, w)
	if meta.ServerID == nil || *meta.ServerID != identity.ServerID || strings.Contains(w.Body.String(), first.Token) {
		t.Fatal("unexpected consumed token metadata")
	}
	requireStatus(t, request(t, h, "DELETE", tokenURL(first.ID), "", cookie, nil), 204)
	getToken(first.ID, 200)
	reused := register(first.Token, "8.8.8.8")
	requireStatus(t, reused, 401)
	revoked := issue()
	requireStatus(t, request(t, h, "DELETE", tokenURL(revoked.ID), "", cookie, nil), 204)
	getToken(revoked.ID, 404)
	expired := issue()
	now.Add(3600)
	getToken(expired.ID, 404)
	getToken(first.ID, 404)
	for _, token := range []string{revoked.Token, expired.Token, "invalid"} {
		w := register(token, "8.8.8.8")
		requireStatus(t, w, 401)
		if w.Body.String() != reused.Body.String() {
			t.Fatal("token authentication failure responses differ")
		}
	}
	w = request(t, h, "GET", panel+"servers/join-tokens", "", cookie, nil)
	requireStatus(t, w, 200)
	if w.Body.String() != "[]\n" {
		t.Fatal("unusable tokens in listing")
	}
}

func TestAgentJoinRateLimitByPeer(t *testing.T) {
	h, _, _, now := agentAPI(t)
	const target = "https://master.test/panel/api/agent/v1/join"
	for i := range 10 {
		w := request(t, h, "POST", target, `{}`, nil, map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i)})
		requireStatus(t, w, 401)
	}
	now.Add(59)
	w := request(t, h, "POST", target, `{}`, nil, nil)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") != "1" {
		t.Fatal("unexpected retry time")
	}
	now.Add(1)
	requireStatus(t, request(t, h, "POST", target, `{}`, nil, nil), 401)
	w = request(t, h, "POST", target, `{}`, nil, map[string]string{"Content-Type": "text/plain"})
	requireStatus(t, w, 400)
}

func TestAgentTLSJoinHeartbeatOfflineAndRemoval(t *testing.T) {
	h, master, conn, now := agentAPI(t)
	ctx := context.Background()
	server := httptest.NewTLSServer(h)
	defer server.Close()
	pin := certs.SPKIPin(server.Certificate().RawSubjectPublicKeyInfo)
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := master.IssueToken(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	for _, badPin := range []string{"", base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		client, err := agent.NewClient(server.URL+"/panel/", badPin)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Join(ctx, token, public, "host", "", "1.0"); err == nil {
			t.Fatal("untrusted certificate accepted")
		}
		client.Close()
	}
	client, err := agent.NewClient(server.URL+"/panel/", pin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id, err := client.Join(ctx, token, public, "host", "8.8.8.8", "1.0")
	if err != nil || id.ServerID <= 1 {
		t.Fatal(id, err)
	}
	check := func(want, version string, seen bool) {
		t.Helper()
		var status, gotVersion string
		var lastSeen sql.NullInt64
		if err := conn.QueryRow("SELECT status,version,last_seen_at FROM servers WHERE id=?", id.ServerID).Scan(&status, &gotVersion, &lastSeen); err != nil {
			t.Fatal(err)
		}
		if status != want || gotVersion != version || lastSeen.Valid != seen {
			t.Fatalf("status=%s version=%s lastSeen=%v", status, gotVersion, lastSeen)
		}
	}
	check("pending", "1.0", false)
	beat := agent.Heartbeat{Version: "1.1", SingboxActive: true, Uptime: 5}
	if err := client.Heartbeat(ctx, id.ServerID, key, beat); err != nil {
		t.Fatal(err)
	}
	check("online", "1.1", true)
	now.Add(90)
	if err := master.MarkOffline(ctx); err != nil {
		t.Fatal(err)
	}
	check("online", "1.1", true)
	now.Add(1)
	if err := master.MarkOffline(ctx); err != nil {
		t.Fatal(err)
	}
	check("offline", "1.1", true)
	if err := client.Heartbeat(ctx, id.ServerID, key, beat); err != nil {
		t.Fatal(err)
	}
	check("online", "1.1", true)

	const panel = "https://master.test/panel/api/v1/"
	login := request(t, h, "POST", panel+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	listed := decodeResult[[]serverRecord](t, request(t, h, "GET", panel+"servers", "", cookie, nil))
	if len(listed) != 2 || listed[0].Uptime != nil || listed[1].Uptime == nil || *listed[1].Uptime != beat.Uptime {
		t.Fatal("unexpected heartbeat uptime in server list", listed)
	}
	idPath := panel + "servers/" + strconv.FormatInt(id.ServerID, 10)
	requireStatus(t, request(t, h, "DELETE", idPath, "", nil, nil), 401)
	requireStatus(t, request(t, h, "DELETE", idPath, "", cookie, map[string]string{"Origin": "https://other.test"}), 403)
	requireStatus(t, request(t, h, "DELETE", panel+"servers/1", "", cookie, nil), 404)
	if _, err := conn.Exec(`INSERT INTO server_traffic_samples (server_id,sampled_at,interface,rx,tx) VALUES(?,1,'eth0',3,5)`, id.ServerID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, h, "DELETE", idPath, "", cookie, nil), 204)
	check("removed", "1.1", true)
	var keyMissing bool
	var samples int
	if err := conn.QueryRow(`SELECT agent_public_key IS NULL, (SELECT count(*) FROM server_traffic_samples WHERE server_id=servers.id)
		FROM servers WHERE id=?`, id.ServerID).Scan(&keyMissing, &samples); err != nil || !keyMissing || samples != 1 {
		t.Fatal("removal retained credentials or deleted history", err)
	}
	if err := client.Heartbeat(ctx, id.ServerID, key, beat); err != agent.ErrUnauthorized {
		t.Fatal("removed node still authenticates", err)
	}
	list := request(t, h, "GET", panel+"servers", "", cookie, nil)
	requireStatus(t, list, 200)
	var rows []serverRecord
	if err := json.Unmarshal(list.Body.Bytes(), &rows); err != nil || len(rows) != 1 || !rows[0].IsLocal {
		t.Fatal("removed node in server list", err)
	}
}

func TestAgentSignatureAndReplayWindow(t *testing.T) {
	h, master, _, now := agentAPI(t)
	ctx := context.Background()
	_, token, err := master.IssueToken(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := master.Register(ctx, agent.Registration{
		Token: token, PublicKey: base64.RawURLEncoding.EncodeToString(public), Hostname: "host", Version: "v1.0.0",
	}, "8.8.4.4")
	if err != nil {
		t.Fatal(err)
	}
	const path = "/panel/api/agent/v1/heartbeat"
	const body = `{"version":"v1.0.0","singbox_active":true,"uptime":20}`
	const url = "https://master.test" + path
	nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	headers := func(signedPath string, unix int64) map[string]string {
		timestamp := strconv.FormatInt(unix, 10)
		sig := ed25519.Sign(key, fmt.Appendf(nil, "POST\n%s\n%s\n%s\n%x", signedPath, timestamp, nonce, sha256.Sum256([]byte(body))))
		return map[string]string{
			agent.HeaderServer: strconv.FormatInt(id.ServerID, 10), agent.HeaderTimestamp: timestamp, agent.HeaderNonce: nonce,
			agent.HeaderSignature: base64.RawURLEncoding.EncodeToString(sig),
		}
	}
	current := headers(path, now.Load())
	w := request(t, h, "POST", url, body, nil, nil)
	requireStatus(t, w, 401)
	sameFailure := func(body string, headers map[string]string) {
		t.Helper()
		failure := request(t, h, "POST", url, body, nil, headers)
		requireStatus(t, failure, 401)
		if failure.Body.String() != w.Body.String() {
			t.Fatal("different signature error responses")
		}
	}
	sameFailure(body+" ", current)
	sameFailure(body, headers("/different"+path, now.Load()))
	sameFailure(body, headers(path, now.Load()-121))
	sameFailure(body, headers(path, now.Load()+121))
	tampered := headers(path, now.Load())
	tampered[agent.HeaderTimestamp] = strconv.FormatInt(now.Load()+1, 10)
	sameFailure(body, tampered)
	tampered = headers(path, now.Load())
	tampered[agent.HeaderServer] = "9999"
	sameFailure(body, tampered)
	requireStatus(t, request(t, h, "POST", url, body, nil, current), 204)
	sameFailure(body, current)
	now.Add(299)
	sameFailure(body, headers(path, now.Load()))
	now.Add(1)
	requireStatus(t, request(t, h, "POST", url, body, nil, headers(path, now.Load()-120)), 204)
}
