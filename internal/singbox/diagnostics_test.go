package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCurrentConfigAndJournalRedaction(t *testing.T) {
	f := newApplyFixture(t)
	if _, err := f.m.Config(context.Background()); !errors.Is(err, ErrNoConfig) {
		t.Fatal(err)
	}
	raw := `{"inbounds":[{"users":[{"uuid":"11111111-2222-3333-4444-555555555555","password":"short-pass"}],"tls":{"reality":{"private_key":"private-material","short_id":["short-pass"]}},"obfs":{"password":"obfs-secret"}}],"experimental":{"clash_api":{"secret":"control-secret"}}}`
	if err := os.WriteFile(f.path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := f.m.Config(context.Background())
	if err != nil || !json.Valid(data) {
		t.Fatal(err)
	}
	for _, secret := range []string{"11111111-2222-3333-4444-555555555555", "short-pass", "private-material", "obfs-secret", "control-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("config leaked %s", secret)
		}
	}
	if !strings.Contains(string(data), "1111…5555") {
		t.Fatal("UUID is not masked")
	}
	lines, err := f.m.Logs(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "bind failed") || strings.Contains(strings.Join(lines, "\n"), "alice-password-secret") {
		t.Fatal(lines)
	}
	if _, err := f.m.Logs(context.Background(), 1001); err == nil {
		t.Fatal("unbounded journal query")
	}
	found := false
	for _, cmd := range f.r.commands {
		if cmd[0] == "journalctl" {
			found = true
			if !strings.Contains(strings.Join(cmd, " "), "--lines=200") {
				t.Fatal(cmd)
			}
		}
	}
	if !found {
		t.Fatal("journalctl not called")
	}
	if err := os.WriteFile(f.path, []byte("malformed private-material"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Config(context.Background()); err == nil || strings.Contains(err.Error(), "private-material") {
		t.Fatal("bad config exposed")
	}
}

func TestRedactUnstructuredDiagnostics(t *testing.T) {
	secret := "a\"b?c&d"
	raw := "plain " + secret + "\nescaped a\\\"b?c&d\nurl a%22b%3Fc%26d\nprivate_key: old-secret\n-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\nstart failed"
	got := redactText(raw, []string{secret})
	for _, text := range []string{secret, "a\\\"b?c&d", "a%22b%3Fc%26d", "old-secret", "\nabc\n"} {
		if strings.Contains(got, text) {
			t.Fatal("leaked credential representation")
		}
	}
	if !strings.Contains(got, "start failed") {
		t.Fatal("diagnostic removed")
	}
}
