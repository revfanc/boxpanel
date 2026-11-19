package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"sync"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestConcurrentRegistrationConsumesTokenOnce(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	master := NewMaster(conn, nil, nil)
	_, token, err := master.IssueToken(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var workers sync.WaitGroup
	for range cap(results) {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		workers.Go(func() {
			<-start
			_, err := master.Register(ctx, Registration{
				Token: token, PublicKey: encoding.EncodeToString(public), Hostname: "edge", Version: "test",
			}, "8.8.8.8")
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch err {
		case nil:
			succeeded++
		case ErrUnauthorized:
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful registrations = %d", succeeded)
	}
	var servers, tokens int
	if err := conn.QueryRow(`SELECT (SELECT count(*) FROM servers WHERE name='edge' AND status='pending'),
		(SELECT count(*) FROM agent_join_tokens)`).Scan(&servers, &tokens); err != nil || servers != 1 || tokens != 0 {
		t.Fatal("unexpected registration transaction result", servers, tokens, err)
	}
}
