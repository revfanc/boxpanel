package traffic

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
)

func fixture(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "traffic.db")
	w, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	r, err := db.OpenReader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	exec(t, w, `INSERT INTO users(id,name,uuid,password,sub_token,created_at) VALUES (1,'one','uuid-one','secret-one','token-one',1),(2,'two','uuid-two','secret-two','token-two',1),(3,'three','uuid-three','secret-three','token-three',1)`)
	for _, err := range []error{db.SetSetting(ctx, w, db.ClashAPIPort, 12345), db.SetSetting(ctx, w, db.ClashAPISecret, "test-secret"), db.SetSetting(ctx, w, db.AppliedUsers, []int64{1, 2})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return w, r
}
func exec(t *testing.T, w *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := w.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func usage(t *testing.T, w *sql.DB, id int64) amount {
	t.Helper()
	var result amount
	if err := w.QueryRow("SELECT traffic_up,traffic_down FROM users WHERE id=?", id).Scan(&result.up, &result.down); err != nil {
		t.Fatal(err)
	}
	return result
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func counter(user, up, down int64) firewall.Counter {
	return firewall.Counter{UserID: user, Upload: up, Download: down}
}

// Model a panel that established its initial empty baseline before traffic.
func runningCollector(t *testing.T, w, r *sql.DB, fetch FetchFunc) *Collector {
	t.Helper()
	c := NewCollector(w, r, func(context.Context) ([]firewall.Counter, error) { return nil, nil })
	must(t, c.Poll(context.Background()))
	c.fetch = fetch
	return c
}

func TestNodeCollectorResumesFromCommittedKernelCheckpoint(t *testing.T) {
	w, r := fixture(t)
	ctx := t.Context()
	raw := int64(40)
	fetch := func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{counter(1, 0, raw)}, nil
	}
	collector := NewNodeCollector(w, r, fetch, "same-boot")
	must(t, collector.Poll(ctx))
	must(t, collector.Flush(ctx)) // Persist the baseline even without a delta.
	raw = 70
	must(t, collector.Poll(ctx)) // Simulate exit before committing this sample.
	raw = 100
	collector = NewNodeCollector(w, r, fetch, "same-boot")
	must(t, collector.Poll(ctx))
	must(t, collector.Flush(ctx))
	if got := usage(t, w, 1).down; got != 60 {
		t.Fatalf("same-boot restart recovered %d bytes, want 60", got)
	}
	collector = NewNodeCollector(w, r, fetch, "same-boot")
	must(t, collector.Poll(ctx))
	must(t, collector.Flush(ctx))
	if got := usage(t, w, 1).down; got != 60 {
		t.Fatalf("same-boot restart counted the previous sample again: %d", got)
	}
	raw = 110 // Already higher than the old counter after the host reboot.
	collector = NewNodeCollector(w, r, fetch, "new-boot")
	must(t, collector.Poll(ctx))
	must(t, collector.Flush(ctx))
	if got := usage(t, w, 1).down; got != 170 {
		t.Fatalf("new-boot counters were compared against the old boot: %d", got)
	}
}

func TestCollectorDeltasRecreationUnknownAndDelete(t *testing.T) {
	w, r := fixture(t)
	ctx := context.Background()
	response := []firewall.Counter{counter(1, 1000, 2000), counter(2, 30, 40), counter(99, 500, 500)}
	fail := false
	c := NewCollector(w, r, func(context.Context) ([]firewall.Counter, error) {
		if fail {
			return nil, errors.New("offline")
		}
		return response, nil
	})
	must(t, c.Poll(ctx)) // baseline only, including existing counters
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{}) {
		t.Fatal(got)
	}
	response = []firewall.Counter{counter(1, 1015, 2035), counter(2, 35, 50)}
	must(t, c.Poll(ctx))
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{15, 35}) {
		t.Fatal(got)
	}
	// Recreated upload counter, unchanged download counter: independent deltas.
	response = []firewall.Counter{counter(1, 5, 2043)}
	must(t, c.Poll(ctx))
	response = []firewall.Counter{counter(1, 8, 2050)}
	fail = true
	if c.Poll(ctx) == nil {
		t.Fatal("expected fetch error")
	}
	fail = false
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{23, 50}) {
		t.Fatal(got)
	}
	exec(t, w, "DELETE FROM users WHERE id=1")
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if len(c.previous) != 0 {
		t.Fatal("deleted/removed user baselines retained")
	}
	if got := usage(t, w, 2); got != (amount{5, 10}) {
		t.Fatal(got)
	}
}
func TestCollectorBatchFailureRetryResetAndOverflow(t *testing.T) {
	w, r := fixture(t)
	ctx := context.Background()
	response := []firewall.Counter{counter(1, 10, 20), counter(2, 30, 40)}
	c := runningCollector(t, w, r, func(context.Context) ([]firewall.Counter, error) { return response, nil })
	must(t, c.Poll(ctx))
	exec(t, w, `CREATE TRIGGER fail_usage BEFORE UPDATE OF traffic_up ON users WHEN NEW.id=2 BEGIN SELECT RAISE(ABORT,'disk failure'); END`)
	if c.Flush(ctx) == nil {
		t.Fatal("expected failed batch")
	}
	if got := usage(t, w, 1); got != (amount{}) {
		t.Fatalf("partial batch: %v", got)
	}
	exec(t, w, "DROP TRIGGER fail_usage")
	must(t, c.Flush(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{10, 20}) {
		t.Fatal(got)
	}
	response = []firewall.Counter{counter(1, 50, 60)}
	must(t, c.Poll(ctx))
	// Two resets in one second are distinct epochs. Pending increments cannot undo either.
	exec(t, w, "UPDATE users SET traffic_up=0,traffic_down=0,traffic_epoch=traffic_epoch+2,traffic_reset_at=100 WHERE id=1")
	must(t, c.Flush(ctx))
	response = []firewall.Counter{counter(1, 60, 70)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{}) {
		t.Fatalf("old epoch restored: %v", got)
	}
	response = []firewall.Counter{counter(1, 62, 73)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{2, 3}) {
		t.Fatal(got)
	}
	exec(t, w, "UPDATE users SET traffic_up=?,traffic_down=? WHERE id=1", int64(math.MaxInt64-1), int64(math.MaxInt64-1))
	response = []firewall.Counter{counter(1, 70, 100)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{math.MaxInt64, math.MaxInt64}) {
		t.Fatalf("overflow: %v", got)
	}
	response = []firewall.Counter{counter(1, 80, 110)}
	must(t, c.Poll(ctx))
	exec(t, w, "DELETE FROM users WHERE id=1")
	exec(t, w, `INSERT INTO users(id,name,uuid,password,sub_token,traffic_epoch,created_at) VALUES(1,'new','new-uuid','new-password','new-token',1000,1)`)
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{}) {
		t.Fatal("reused user ID inherited traffic")
	}
}
func TestCollectorReadsDuringApplyAndRetainsOnWriteTimeout(t *testing.T) {
	w, r := fixture(t)
	ctx := context.Background()
	c := runningCollector(t, w, r, func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{counter(1, 10, 20)}, nil
	})
	tx, err := w.BeginTx(ctx, nil)
	must(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE users SET traffic_up=1000 WHERE id=1")
	must(t, err)
	deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	must(t, c.Poll(deadline)) // independent WAL snapshot must stay available
	if got := usage(t, r, 1); got != (amount{}) {
		t.Fatal("uncommitted data visible")
	}
	if c.Flush(deadline) == nil {
		t.Fatal("expected write-pool timeout")
	}
	must(t, tx.Rollback())
	must(t, c.Flush(ctx))
	if got := usage(t, r, 1); got != (amount{10, 20}) {
		t.Fatal(got)
	}
}

type fakeApply struct {
	writer *sql.DB
	calls  int
	fail   bool
	now    *time.Time
}

func (f *fakeApply) Apply(ctx context.Context, mutation singbox.Mutation) error {
	f.calls++
	tx, err := f.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = mutation(ctx, tx); err != nil {
		return err
	}
	if f.fail {
		return errors.New("check failed")
	}
	users, _, err := loadUsers(ctx, tx)
	if err != nil {
		return err
	}
	active := []int64{}
	for _, u := range users {
		if confgen.UserEligible(u.User, f.now.Unix()) {
			active = append(active, u.ID)
		}
	}
	if err = db.SetSetting(ctx, tx, db.AppliedUsers, active); err != nil {
		return err
	}
	return tx.Commit()
}
func schedulerFixture(t *testing.T) (*Scheduler, *fakeApply, *time.Time, *sql.DB) {
	w, r := fixture(t)
	now := time.Date(2026, 11, 15, 23, 59, 59, 0, time.UTC)
	c := runningCollector(t, w, r, func(context.Context) ([]firewall.Counter, error) {
		return []firewall.Counter{counter(2, 50, 60)}, nil
	})
	apply := &fakeApply{writer: w, now: &now}
	s := NewScheduler(c, apply, func() time.Time { return now }, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return s, apply, &now, w
}
func TestSchedulerExpiryQuotaMergedAndRecovery(t *testing.T) {
	s, apply, now, w := schedulerFixture(t)
	ctx := context.Background()
	exec(t, w, "UPDATE users SET expire_at=? WHERE id IN (1,3)", now.Unix()) // 3 already absent from applied config
	exec(t, w, "UPDATE users SET traffic_limit=100 WHERE id=2")
	must(t, s.collector.Poll(ctx))
	must(t, s.Check(ctx))
	if apply.calls != 1 {
		t.Fatal(apply.calls)
	}
	if got := usage(t, w, 2); got != (amount{50, 60}) {
		t.Fatal(got)
	}
	active, err := db.GetSetting(ctx, w, db.AppliedUsers)
	must(t, err)
	if len(active) != 0 {
		t.Fatal(active)
	}
	must(t, s.Check(ctx))
	if apply.calls != 1 {
		t.Fatal("repeated unnecessary restart")
	}
	// Existing resource edits recover users through the same Apply path.
	must(t, apply.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET expire_at=0,traffic_limit=1000")
		return err
	}))
	must(t, s.Check(ctx))
	active, err = db.GetSetting(ctx, w, db.AppliedUsers)
	must(t, err)
	if len(active) != 3 {
		t.Fatal(active)
	}
}
func TestSchedulerMonthlyUTCAndNoRepeat(t *testing.T) {
	s, apply, now, w := schedulerFixture(t)
	ctx := context.Background()
	exec(t, w, "UPDATE users SET traffic_reset_day=16,traffic_up=999 WHERE id=1")
	must(t, s.Check(ctx))
	if apply.calls != 0 {
		t.Fatal("reset before UTC midnight")
	}
	*now = now.Add(time.Second).In(time.FixedZone("west", -8*3600))
	must(t, s.Check(ctx))
	if got := usage(t, w, 1); got != (amount{}) {
		t.Fatal(got)
	}
	exec(t, w, "UPDATE users SET traffic_up=17 WHERE id=1")
	must(t, s.Check(ctx))
	if apply.calls != 1 || usage(t, w, 1).up != 17 {
		t.Fatal("repeated monthly reset")
	}
	// Offline recovery resets once for the current month, not once per missed month.
	*now = time.Date(2027, 2, 28, 12, 0, 0, 0, time.UTC)
	must(t, s.Check(ctx))
	if apply.calls != 2 || usage(t, w, 1).up != 0 {
		t.Fatal("missed month recovery")
	}
	must(t, s.Check(ctx))
	if apply.calls != 2 {
		t.Fatal("duplicate after restart boundary")
	}
}
func TestSchedulerApplyFailureBackoffAndMonthlyRollback(t *testing.T) {
	s, apply, now, w := schedulerFixture(t)
	ctx := context.Background()
	exec(t, w, "UPDATE users SET traffic_reset_day=15,traffic_up=999,traffic_limit=100 WHERE id=1")
	must(t, s.collector.Poll(ctx))
	apply.fail = true
	for i := 0; i < 7; i++ {
		if s.Check(ctx) == nil {
			t.Fatal("expected apply failure")
		}
		if usage(t, w, 1).up != 999 || usage(t, w, 2).up != 0 {
			t.Fatal("failed apply committed traffic/reset")
		}
		if len(s.collector.pending) == 0 {
			t.Fatal("failed apply lost increments")
		}
		expected := min(30*time.Second*time.Duration(1<<i), 10*time.Minute)
		if s.nextAttempt.Sub(*now) != expected {
			t.Fatal(s.nextAttempt.Sub(*now))
		}
		calls := apply.calls
		*now = now.Add(expected - time.Second)
		must(t, s.Check(ctx))
		if apply.calls != calls {
			t.Fatal("ignored backoff")
		}
		*now = now.Add(time.Second)
	}
	apply.fail = false
	must(t, s.Check(ctx))
	if usage(t, w, 1).up != 0 || usage(t, w, 2).up != 50 {
		t.Fatal("retry lost/reset usage")
	}
	must(t, s.Check(ctx))
	if !s.nextAttempt.IsZero() {
		t.Fatal("backoff not cleared")
	}
}
func TestRunFlushesOnNormalExit(t *testing.T) {
	s, _, _, w := schedulerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	must(t, s.collector.Poll(ctx))
	cancel()
	s.Run(ctx)
	if got := usage(t, w, 2); got != (amount{50, 60}) {
		t.Fatal(got)
	}
}

func TestCollectorReconcilesCommittedBatchBeforeRetry(t *testing.T) {
	w, r := fixture(t)
	ctx := context.Background()
	response := []firewall.Counter{counter(1, 10, 20)}
	c := runningCollector(t, w, r, func(context.Context) ([]firewall.Counter, error) { return response, nil })
	must(t, c.Poll(ctx))
	// Simulate a COMMIT that reached SQLite but whose result was not known to
	// the caller (also possible after Manager.Apply's generation read fails).
	tx, err := w.BeginTx(ctx, nil)
	must(t, err)
	must(t, c.writePending(ctx, tx))
	must(t, tx.Commit())
	response = []firewall.Counter{counter(1, 13, 27)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{13, 27}) {
		t.Fatalf("committed batch counted twice: %v", got)
	}
}

// Each process reads the retained kernel counters once without recounting.
func TestCollectorPanelRestartDoesNotRecount(t *testing.T) {
	w, r := fixture(t)
	ctx := context.Background()
	response := []firewall.Counter{counter(1, 1000, 2000)}
	fetch := func(context.Context) ([]firewall.Counter, error) { return response, nil }
	c := NewCollector(w, r, fetch)
	must(t, c.Poll(ctx))
	response = []firewall.Counter{counter(1, 1100, 2300)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	c = NewCollector(w, r, fetch)
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	response = []firewall.Counter{counter(1, 1115, 2325)}
	must(t, c.Poll(ctx))
	must(t, c.Flush(ctx))
	if got := usage(t, w, 1); got != (amount{115, 325}) {
		t.Fatal(got)
	}
}

func TestRunAccountingKeepsSamplingWithoutEnforcingUserPolicy(t *testing.T) {
	s, apply, now, w := schedulerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exec(t, w, "UPDATE users SET expire_at=?,traffic_limit=1,traffic_reset_day=15 WHERE id=2", now.Unix())
	polls := 0
	s.collector.fetch = func(context.Context) ([]firewall.Counter, error) {
		polls++
		cancel()
		return []firewall.Counter{counter(2, 50, 60)}, nil
	}
	s.RunAccounting(ctx)
	if apply.calls != 0 || polls == 0 || usage(t, w, 2) != (amount{50, 60}) {
		t.Fatal("accounting mode did not sample or attempted local enforcement")
	}
}
