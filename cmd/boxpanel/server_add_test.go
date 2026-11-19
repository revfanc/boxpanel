package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/db"
)

func TestServerAddPrintsJoinCommandAndStoresHash(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	t.Setenv("BOXPANEL_DATA_DIR", dir)
	t.Setenv("BOXPANEL_PUBLIC_IP", "203.0.113.9")
	if code := run([]string{"init", "--port", "25433"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	conn, err := app.OpenExisting(ctx, dir+"/boxpanel.db")
	if err != nil {
		t.Fatal(err)
	}
	// No pin yet: the command must refuse with a next step.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "add", "日本"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "systemctl restart boxpanel") {
		t.Fatalf("missing pin: %d %s", code, stderr.String())
	}
	if err := db.SetSetting(ctx, conn, db.PanelFallbackPin, "pin-value"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"server", "add", "日本"}, &stdout, &stderr); code != 0 {
		t.Fatalf("server add: %d %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"--join 'https://203.0.113.9:25433/", "--master-pin 'pin-value'", "scripts/install.sh", "--token '"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	token := strings.SplitN(strings.SplitN(out, "--token '", 2)[1], "'", 2)[0]
	var hash []byte
	if err := conn.QueryRowContext(ctx, "SELECT token_hash FROM agent_join_tokens WHERE name='日本'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(token)); !bytes.Equal(hash, sum[:]) {
		t.Fatal("stored value is not the token digest")
	}
	var plain int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM agent_join_tokens WHERE token_hash=?", []byte(token)).Scan(&plain); err != nil && err != sql.ErrNoRows || plain != 0 {
		t.Fatal("plaintext token stored")
	}
	// Agents must not issue tokens.
	if err := db.SetSetting(ctx, conn, db.NodeRole, "agent"); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"server", "add", "x"}, &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "不是主控") {
		t.Fatalf("agent issued token: %d %s", code, stderr.String())
	}
	for _, bad := range [][]string{{"server"}, {"server", "add"}, {"server", "add", "a", "b"}, {"server", "add", "   "}} {
		if run(bad, &bytes.Buffer{}, &bytes.Buffer{}) != 2 {
			t.Fatalf("accepted %v", bad)
		}
	}
}
