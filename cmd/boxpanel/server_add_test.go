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

func TestServerListAndRemove(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	t.Setenv("BOXPANEL_DATA_DIR", dir)
	t.Setenv("BOXPANEL_PUBLIC_IP", "203.0.113.9")
	if code := run([]string{"init", "--port", "25434"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	conn, err := app.OpenExisting(ctx, dir+"/boxpanel.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO servers (id,name,public_host,status,version,agent_public_key,last_seen_at) VALUES (2,'日本','198.51.100.7','online','v1','k',1795083649)`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"server", "list"}, &out, &bytes.Buffer{}); code != 0 || !strings.Contains(out.String(), "日本") || !strings.Contains(out.String(), "online") {
		t.Fatalf("list: %d %s", code, out.String())
	}
	if code := run([]string{"server", "remove", "1"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal("local server must not be removable")
	}
	out.Reset()
	if code := run([]string{"server", "remove", "2"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("remove: %d", code)
	}
	var status string
	var key sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT status, agent_public_key FROM servers WHERE id=2").Scan(&status, &key); err != nil || status != "removed" || key.Valid {
		t.Fatalf("not removed: %s %v %v", status, key, err)
	}
	if code := run([]string{"server", "remove", "2"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal("double removal must fail")
	}
	if run([]string{"server", "remove", "x"}, &bytes.Buffer{}, &bytes.Buffer{}) != 2 || run([]string{"server", "list", "extra"}, &bytes.Buffer{}, &bytes.Buffer{}) != 2 {
		t.Fatal("bad arguments accepted")
	}
}
