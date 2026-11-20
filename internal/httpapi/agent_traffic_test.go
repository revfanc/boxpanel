package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/agent"
)

func TestAgentTrafficReportValidationAndAccounting(t *testing.T) {
	h, master, conn, now := agentAPI(t)
	var logs bytes.Buffer
	master.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	register := func(name string) (int64, ed25519.PrivateKey) {
		t.Helper()
		_, token, err := master.IssueToken(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id, err := master.Register(t.Context(), agent.Registration{
			Token: token, PublicKey: base64.RawURLEncoding.EncodeToString(public), Hostname: name, Version: "test",
		}, "8.8.8.8")
		if err != nil {
			t.Fatal(err)
		}
		return id.ServerID, private
	}
	edge, edgeKey := register("edge")
	other, otherKey := register("other")
	if _, err := conn.Exec(`INSERT INTO users (id,name,uuid,password,sub_token,created_at,traffic_up,traffic_down)
		VALUES (1,'alice','uuid-1','secret-1','token-1',1,2,3),(2,'bob','uuid-2','secret-2','token-2',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	send := func(path, body string, id int64, key ed25519.PrivateKey, status int) {
		t.Helper()
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		nonce := base64.RawURLEncoding.EncodeToString(random[:])
		timestamp := strconv.FormatInt(now.Load(), 10)
		sig := ed25519.Sign(key, fmt.Appendf(nil, "POST\n%s\n%s\n%s\n%x", path, timestamp, nonce, sha256.Sum256([]byte(body))))
		w := request(t, h, "POST", "https://master.test"+path, body, nil, map[string]string{
			agent.HeaderServer: strconv.FormatInt(id, 10), agent.HeaderTimestamp: timestamp, agent.HeaderNonce: nonce,
			agent.HeaderSignature: base64.RawURLEncoding.EncodeToString(sig),
		})
		requireStatus(t, w, status)
	}
	const path = "/panel" + agent.TrafficPath
	const first = `{"counters":[{"user_id":1,"epoch":12,"up":10,"down":20}]}`
	const larger = `{"counters":[{"user_id":1,"epoch":12,"up":12,"down":25}]}`
	const regressed = `{"counters":[{"user_id":1,"epoch":12,"up":9,"down":27}]}`
	const reset = `{"counters":[{"user_id":1,"epoch":13,"up":4,"down":2}]}`
	assertUsage := func(up, down int64) {
		t.Helper()
		var gotUp, gotDown int64
		if err := conn.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=1").Scan(&gotUp, &gotDown); err != nil || gotUp != up || gotDown != down {
			t.Fatalf("master usage = %d/%d, want %d/%d: %v", gotUp, gotDown, up, down, err)
		}
	}
	requireStatus(t, request(t, h, http.MethodPost, "https://master.test"+path, first, nil, nil), 401)
	send(path, first, other, edgeKey, 401)
	send(path, `{"server_id":3,"counters":[]}`, edge, edgeKey, 400)
	for _, body := range []string{
		`{"counters":[{"user_id":1,"epoch":12,"up":1,"down":2},{"user_id":1,"epoch":13,"up":3,"down":4}]}`,
		`{"counters":[{"user_id":1,"epoch":12,"up":-1,"down":2}]}`,
		`{"counters":[{"user_id":1,"epoch":0,"up":1,"down":2}]}`,
		`{"counters":[{"user_id":1,"epoch":12,"up":1,"up":2,"down":2}]}`,
		`{"counters":null}`, `{"counters":[]} {}`,
	} {
		send(path, body, edge, edgeKey, 400)
	}
	tooMany := agent.TrafficReport{Counters: make([]agent.TrafficCounter, agent.MaxTrafficUsers+1)}
	for i := range tooMany.Counters {
		tooMany.Counters[i] = agent.TrafficCounter{UserID: int64(i + 1), Epoch: 1, Up: 1}
	}
	tooManyBody, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	send(path, string(tooManyBody), edge, edgeKey, 400)
	send(path, first+strings.Repeat(" ", agent.MaxTrafficBody), edge, edgeKey, 413)
	send("/panel"+agent.Prefix+"/heartbeat", `{"version":"test"}`+strings.Repeat(" ", 8192), edge, edgeKey, 401)
	assertUsage(2, 3)
	if _, err := conn.Exec(`CREATE TABLE traffic_test_fk (id INTEGER REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER traffic_test_abort AFTER UPDATE OF traffic_up ON users
		WHEN NEW.id=2 BEGIN INSERT INTO traffic_test_fk VALUES (9999); END`); err != nil {
		t.Fatal(err)
	}
	send(path, `{"counters":[{"user_id":1,"epoch":12,"up":10,"down":20},{"user_id":2,"epoch":1,"up":1,"down":1}]}`, edge, edgeKey, 500)
	assertUsage(2, 3)
	if _, err := conn.Exec("DROP TRIGGER traffic_test_abort; DROP TABLE traffic_test_fk"); err != nil {
		t.Fatal(err)
	}

	send(path, first+strings.Repeat(" ", 8192), edge, edgeKey, 204)
	send(path, first, edge, edgeKey, 204)
	assertUsage(12, 23)
	send(path, larger, edge, edgeKey, 204)
	send(path, regressed, edge, edgeKey, 204)
	send(path, larger, edge, edgeKey, 204)
	assertUsage(14, 30)
	if log := logs.String(); !strings.Contains(log, "level=WARN") || strings.Contains(log, "user_id") || strings.Contains(log, "alice") {
		t.Fatal("counter regression did not produce a redacted warning")
	}
	send(path, reset, edge, edgeKey, 204)
	send(path, larger, edge, edgeKey, 204)
	send(path, first, other, otherKey, 204)
	send(path, `{"counters":[{"user_id":999,"epoch":99,"up":100,"down":100}]}`, edge, edgeKey, 204)
	assertUsage(28, 52)

	const panel = "https://master.test/panel/api/v1/"
	login := request(t, h, "POST", panel+"auth/login", `{"username":"admin","password":"test-password"}`, nil, nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	list := func() []serverRecord {
		t.Helper()
		return decodeResult[[]serverRecord](t, request(t, h, "GET", panel+"servers", "", cookie, nil))
	}
	rows := list()
	if len(rows) != 3 || rows[0].TrafficUp != nil || rows[1].TrafficUp == nil || rows[1].TrafficDown == nil ||
		rows[2].TrafficUp == nil || *rows[1].TrafficUp != 16 || *rows[1].TrafficDown != 29 || *rows[2].TrafficUp != 10 {
		t.Fatalf("unexpected server totals: %v", rows)
	}
	send(path, fmt.Sprintf(`{"counters":[{"user_id":1,"epoch":14,"up":%d,"down":0},{"user_id":2,"epoch":1,"up":4,"down":0}]}`, math.MaxInt64), edge, edgeKey, 204)
	assertUsage(math.MaxInt64, 52)
	if rows := list(); *rows[1].TrafficUp != math.MaxInt64 {
		t.Fatal("server total overflowed")
	}
	if err := master.Remove(t.Context(), edge); err != nil {
		t.Fatal(err)
	}
	send(path, reset, edge, edgeKey, 401)
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM server_user_traffic WHERE server_id=?", edge).Scan(&count); err != nil || count != 2 {
		t.Fatal("server removal lost traffic history", count, err)
	}
	if _, err := conn.Exec("DELETE FROM users WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := conn.QueryRow("SELECT user_id FROM server_user_traffic WHERE user_id<>2").Scan(&id); err != sql.ErrNoRows {
		t.Fatal("deleted or unknown user traffic retained", id, err)
	}
}
