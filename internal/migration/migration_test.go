package migration

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestMigrationTokenReplacementExpiryAndConcurrentClaim(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(dir, "boxpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	now := time.Now().Truncate(time.Second)
	s := &Service{DB: conn, ReadDB: conn, DataDir: dir, Version: "v1.2.3-rc.1", Now: func() time.Time { return now }}
	const source = "https://panel.example.com:2053/panel/"
	issue := func() Grant {
		t.Helper()
		grant, err := s.Issue(ctx, source)
		if err != nil || !ValidSecret(grant.Token) || grant.ExpiresAt != now.Add(Lifetime).Unix() {
			t.Fatalf("invalid migration grant metadata: %v", err)
		}
		return grant
	}
	first := issue()
	next := issue()
	saved, err := db.LoadMasterMigration(ctx, conn, db.MigrationOutgoing)
	if err != nil || saved.TokenHash != exchangeID(next.Token) || saved.TokenHash == next.Token {
		t.Fatal("migration grant did not store only the latest digest", err)
	}
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	claim := Claim{Token: first.Token, Secret: secret, Version: s.Version}
	if _, err := s.Claim(ctx, claim, "8.8.8.8"); !errors.Is(err, ErrToken) {
		t.Fatal("replaced token was accepted", err)
	}
	claim.Token = next.Token
	if _, err := s.Claim(ctx, claim, "127.0.0.1"); !errors.Is(err, ErrAddress) {
		t.Fatal("non-public peer accepted", err)
	}
	wrongVersion := claim
	wrongVersion.Version = "v1.2.2"
	if _, err := s.Claim(ctx, wrongVersion, "8.8.8.8"); !errors.Is(err, ErrVersion) {
		t.Fatal("a different version could claim migration", err)
	}
	claim.PublicIP = "8.8.4.4"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Claim(ctx, claim, "127.0.0.1"); results <- err })
	}
	wg.Wait()
	a, b := <-results, <-results
	if !(a == nil && errors.Is(b, ErrToken) || b == nil && errors.Is(a, ErrToken)) {
		t.Fatalf("one claim should succeed: %v / %v", a, b)
	}
	saved, err = db.LoadMasterMigration(ctx, conn, db.MigrationOutgoing)
	if err != nil || saved.Phase != db.MigrationFrozen || saved.TokenHash != "" || saved.TargetURL != "https://8.8.4.4:2053/panel/" {
		t.Fatalf("freeze not persisted: %v", err)
	}
	if _, err := s.BeginWrite(ctx); !errors.Is(err, db.ErrMigrationReadOnly) {
		t.Fatal("frozen source accepted writes", err)
	}
	if _, err := s.Issue(ctx, source); !errors.Is(err, db.ErrMigrationReadOnly) {
		t.Fatal("frozen source issued a new token", err)
	}
	// A restarted service reads the same freeze; at the deadline it immediately
	// becomes writable without a callback from the installer.
	resumed := &Service{DB: conn, ReadDB: conn, DataDir: dir, Now: s.Now}
	if status, err := resumed.PanelStatus(ctx); err != nil || status.Phase != db.MigrationFrozen {
		t.Fatal(status.Phase, err)
	}
	now = now.Add(Lifetime)
	if _, err := resumed.Claim(ctx, claim, "8.8.8.8"); !errors.Is(err, ErrToken) {
		t.Fatal("expired token could be used", err)
	}
	release, err := resumed.BeginWrite(ctx)
	if err != nil {
		t.Fatal("expired claim did not unfreeze the source", err)
	}
	release()
	claim.Token = issue().Token
	now = now.Add(Lifetime)
	if _, err := s.Claim(ctx, claim, "8.8.8.8"); !errors.Is(err, ErrToken) {
		t.Fatal("unused expired grant could be claimed", err)
	}
}
