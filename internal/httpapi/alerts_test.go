package httpapi

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/auth"
)

func TestAlertAPISettingsMaskingAndFallback(t *testing.T) {
	_, conn, _ := testAPI(t, false)
	authService, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	notificationService := alerts.New(conn, nil, alerts.Options{})
	h := New(authService, Options{Alerts: notificationService, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	base := "https://example.test/api/v1/"
	same := map[string]string{"Origin": "https://example.test", "Sec-Fetch-Site": "same-origin"}
	cross := map[string]string{"Origin": "https://other.test", "Sec-Fetch-Site": "cross-site"}
	login := request(t, h, "POST", base+"auth/login", `{"username":"admin","password":"test-password"}`, nil, same)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]

	requireStatus(t, request(t, h, "GET", base+"alerts", "", nil, nil), 401)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"quota_gb":20}`, cookie, cross), 403)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"billing_day":29}`, cookie, same), 400)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"billing_mode":"invalid"}`, cookie, same), 400)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"events":{"user_quota_80":null}}`, cookie, same), 400)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"events":{"unknown":false}}`, cookie, same), 400)
	requireStatus(t, request(t, h, "PUT", base+"alerts/settings", `{"quota_gb":20,"billing_day":28,"billing_mode":"max","events":{"user_quota_80":false}}`, cookie, same), 200)
	const bot = "987123:PRIVATE-bot-XYZ1234"
	const chat = "-1234567890"
	save := request(t, h, "PUT", base+"alerts/channels/telegram", `{"credential":"`+bot+`","chat_id":"`+chat+`","enabled":true}`, cookie, same)
	requireStatus(t, save, 200)
	get := request(t, h, "GET", base+"alerts/settings", "", cookie, nil)
	requireStatus(t, get, 200)
	for _, response := range []string{save.Body.String(), get.Body.String()} {
		if strings.Contains(response, bot) || strings.Contains(response, chat) || !strings.Contains(response, "****1234") || !strings.Contains(response, "****7890") {
			t.Fatal("incorrectly masked channel response")
		}
	}
	for _, field := range []string{`"quota_gb":20`, `"billing_day":28`, `"billing_mode":"max"`, `"user_quota_80":false`, `"user_quota_100":true`} {
		if !strings.Contains(get.Body.String(), field) {
			t.Errorf("setting did not persist: %s", field)
		}
	}
	requireStatus(t, request(t, h, "PUT", base+"alerts/channels/bark", `{"url":"https://127.0.0.1"}`, cookie, same), 400)
	requireStatus(t, request(t, h, "POST", base+"alerts/channels/telegram/test", "", cookie, cross), 403)
	requireStatus(t, request(t, h, "POST", base+"alerts/channels/telegram/test", `{}`, cookie, same), 400)
	requireStatus(t, request(t, h, "POST", base+"alerts/channels/telegram/test", "", cookie, same), 202)
	if _, err := conn.Exec(`INSERT INTO alert_events(event_key,server_id,kind,address,username,used,limit_bytes,threshold,deadline,created_at)
		VALUES ('demo',1,'user_quota_80','panel.example.com','alice',80,100,80,0,?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	view := request(t, h, "GET", base+"alerts", "", cookie, nil)
	requireStatus(t, view, 200)
	if !strings.Contains(view.Body.String(), `"unread_count":1`) || !strings.Contains(view.Body.String(), `"status":"pending"`) {
		t.Fatal("missing event or test delivery in overview")
	}
	requireStatus(t, request(t, h, "POST", base+"alerts/events/demo/read", "", cookie, cross), 403)
	requireStatus(t, request(t, h, "POST", base+"alerts/events/demo/read", "", cookie, same), 204)
	requireStatus(t, request(t, h, "POST", base+"alerts/events/missing/read", "", cookie, same), 404)
	view = request(t, h, "GET", base+"alerts", "", cookie, nil)
	requireStatus(t, view, 200)
	if !strings.Contains(view.Body.String(), `"unread_count":0`) {
		t.Fatal("marked event remained unread")
	}
	if strings.Contains(logs.String(), bot) || strings.Contains(logs.String(), chat) || strings.Contains(view.Body.String(), bot) {
		t.Fatal("provider credential leaked into log or overview")
	}
}
