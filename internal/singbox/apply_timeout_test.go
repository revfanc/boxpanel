package singbox

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/firewall"
)

type timeoutFirewall struct {
	FirewallSync
}

func (f timeoutFirewall) Sync(ctx context.Context, previous, next firewall.Plan) error {
	// Only the forward change times out. The recovery context is independent.
	if deadline, _ := ctx.Deadline(); time.Until(deadline) < 100*time.Millisecond {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.FirewallSync.Sync(ctx, previous, next)
}

func TestApplyTimeoutRestoresState(t *testing.T) {
	for _, phase := range []string{"check", "restart", "is-active", "firewall"} {
		t.Run(phase, func(t *testing.T) {
			f := newApplyFixture(t)
			if err := f.m.Apply(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, f.path)
			f.m.applyTimeout = 40 * time.Millisecond
			f.m.recoveryTimeout = time.Second
			f.m.healthTimeout = time.Second
			hit := false
			f.r.hook = func(ctx context.Context, _ string, args []string) {
				if !hit && args[0] == phase {
					hit = true
					<-ctx.Done()
				}
			}
			if phase == "firewall" {
				f.m.firewall = timeoutFirewall{f.fw}
			}
			start := time.Now()
			applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_apply_timeout", false)
			if time.Since(start) >= time.Second {
				t.Fatal("apply exceeded bounded recovery")
			}
			f.bt(t, true)
			if string(readFile(t, f.path)) != string(before) {
				t.Fatal("timeout left candidate configuration active")
			}
			assertMissing(t, f.path+".pending")
			assertMissing(t, f.path+".tmp")
			if phase != "check" && f.r.restarts < 2 {
				t.Fatal("did not restart old core during recovery")
			}
		})
	}
}

func TestApplyQueueTimeoutDoesNotMutate(t *testing.T) {
	f := newApplyFixture(t)
	f.m.applyTimeout = 30 * time.Millisecond
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	mutated := false
	start := time.Now()
	applyFailure(t, f.m.Apply(context.Background(), func(context.Context, *sql.Tx) error { mutated = true; return nil }), "singbox_apply_timeout", false)
	if mutated || time.Since(start) >= time.Second {
		t.Fatal("queued apply mutated or exceeded deadline")
	}
}

func TestApplyTimeoutFailedRecoveryKeepsJournal(t *testing.T) {
	f := newApplyFixture(t)
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, f.path)
	f.m.applyTimeout = 30 * time.Millisecond
	f.m.recoveryTimeout = 40 * time.Millisecond
	f.m.healthTimeout = time.Second
	f.r.hook = func(ctx context.Context, _ string, args []string) {
		if args[0] == "restart" {
			<-ctx.Done()
		}
	}
	failure := applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_apply_timeout", true)
	if !errors.Is(failure, context.DeadlineExceeded) {
		t.Fatal("lost deadline cause")
	}
	if _, err := os.Stat(f.path + ".pending"); err != nil {
		t.Fatal("lost recovery journal")
	}
	f.bt(t, true)
	f.r.hook = nil
	f.m.applyTimeout = time.Second
	f.m.recoveryTimeout = time.Second
	if err := f.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, f.path)) != string(before) {
		t.Fatal("pending recovery changed committed configuration")
	}
	assertMissing(t, f.path+".pending")
}

func TestApplyFlockWaitIsBounded(t *testing.T) {
	f := newApplyFixture(t)
	unlock, err := lockApply(context.Background(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	f.m.applyTimeout = 30 * time.Millisecond
	start := time.Now()
	applyFailure(t, f.m.Apply(context.Background(), changeBT), "singbox_apply_timeout", false)
	if time.Since(start) >= time.Second {
		t.Fatal("flock wait exceeded deadline")
	}
	f.bt(t, true)
}

func TestDiagnosticsDoNotWaitForAnotherApply(t *testing.T) {
	f := newApplyFixture(t)
	unlock, err := lockApply(context.Background(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	if _, err := f.m.Logs(context.Background(), 10); !errors.Is(err, ErrApplying) {
		t.Fatal(err)
	}
	if _, err := f.m.Config(context.Background()); !errors.Is(err, ErrApplying) {
		t.Fatal(err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("diagnostics waited for apply")
	}
}
