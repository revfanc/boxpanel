package subscription

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/web"
)

func TestRetiredSubscriptionsRedirectWithWarningAndNewShareLinks(t *testing.T) {
	ctx := t.Context()
	old, store := publicFixture(t)
	next, targetStore := publicFixture(t)
	const target = "https://new-master.example:2053/anotherpanel/"
	if err := db.SetSetting(ctx, store.DB, db.MigrationOutgoing, db.MasterMigration{Phase: db.MigrationRetired, TargetURL: target}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, targetStore.DB, db.MigrationReceived, db.MasterMigration{Phase: db.MigrationActive}); err != nil {
		t.Fatal(err)
	}
	base := "/subscribe123/token-that-must-not-leak"
	warning := web.SubscriptionText()["migrationNode"]
	for _, format := range []string{"singbox", "clash", "base64"} {
		t.Run(format, func(t *testing.T) {
			bodyHasWarning := func(body string) bool {
				t.Helper()
				if format == "base64" {
					data, err := base64.StdEncoding.DecodeString(body)
					if err != nil {
						t.Fatal(err)
					}
					body, err = url.PathUnescape(string(data))
					if err != nil {
						t.Fatal(err)
					}
				}
				return strings.Contains(body, warning)
			}
			query := "?format=" + format
			origin := publicRequest(old, "GET", base+query, nil)
			expected := "https://new-master.example:2053" + base + "?bp_migrated=1&format=" + format
			if origin.Code != http.StatusMovedPermanently || origin.Header().Get("Location") != expected || !bodyHasWarning(origin.Body.String()) {
				t.Fatalf("retired subscription is missing its redirect or warning: %d", origin.Code)
			}
			if head := publicRequest(old, "HEAD", base+query, nil); head.Code != origin.Code || head.Header().Get("Location") != expected || head.Body.Len() != 0 {
				t.Fatal("retired HEAD did not preserve status and redirect")
			}
			follow := publicRequest(next, "GET", base+query+"&bp_migrated=1", nil)
			if follow.Code != 200 || follow.Header().Get("Location") != "" || !bodyHasWarning(follow.Body.String()) {
				t.Fatal("redirect followers did not receive the warning")
			}
			fresh := publicRequest(next, "GET", base+query, nil)
			if fresh.Code != 200 || bodyHasWarning(fresh.Body.String()) {
				t.Fatal("new subscription unexpectedly contains a migration prompt")
			}
			if format == "singbox" {
				binary, err := exec.LookPath("sing-box")
				if err != nil {
					t.Skip("sing-box not installed, format assertions passed")
				}
				path := filepath.Join(t.TempDir(), "migrated.json")
				if err := os.WriteFile(path, follow.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.CommandContext(ctx, binary, "check", "-c", path).CombinedOutput(); err != nil {
					t.Fatalf("validate migration subscription: %v\n%s", err, out)
				}
			}
		})
	}
	share := publicRequest(old, "GET", base, map[string]string{"User-Agent": "Mozilla/5.0", "Accept": "text/html"})
	if share.Code != 200 || !strings.Contains(share.Body.String(), "https://new-master.example:2053"+base) ||
		!strings.Contains(share.Body.String(), web.SubscriptionText()["migrationLink"]) {
		t.Fatal("old share page does not advertise the new address")
	}
	if missing := publicRequest(old, "GET", "/subscribe123/unknown?format=clash", nil); missing.Code != 404 || missing.Header().Get("Location") != "" {
		t.Fatal("retired subscription redirects invalid tokens")
	}
}
