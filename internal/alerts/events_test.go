package alerts

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/traffic"
)

func pointer[T any](v T) *T { return &v }

type fixture struct {
	t              *testing.T
	writer, reader *sql.DB
	now            time.Time
	opts           Options
	service        *Service
}

func newFixture(t *testing.T, opts Options) *fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "alerts.db")
	writer, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	reader, err := db.OpenReader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	f := &fixture{t: t, writer: writer, reader: reader, now: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC), opts: opts}
	f.restart()
	if err := db.SetSetting(ctx, writer, db.PublicHost, "node.example.com"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) restart() {
	f.opts.Now = func() time.Time { return f.now }
	f.service = New(f.writer, f.reader, f.opts)
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.writer.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.reader.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) events(kind string) int {
	return f.count("SELECT count(*) FROM alert_events WHERE kind=?", kind)
}

func (f *fixture) evaluate() {
	f.t.Helper()
	if err := f.service.evaluate(context.Background(), f.now); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) user(used, expiry int64) {
	f.exec(`INSERT INTO users(name,uuid,password,sub_token,traffic_limit,traffic_up,expire_at,created_at,traffic_epoch,alert_identity)
		VALUES('alice','private-uuid','private-password','private-token',100,?,?,1,101,101)`, used, expiry)
}

func (f *fixture) enableBark() {
	f.t.Helper()
	_, err := f.service.SaveChannel(context.Background(), Bark, ChannelUpdate{Credential: pointer("bark-secret-value"), Enabled: pointer(true)})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestUserThresholdsAndExpiryDeduplicate(t *testing.T) {
	f := newFixture(t, Options{})
	f.enableBark()
	f.user(79, f.now.Add(72*time.Hour+time.Second).Unix())
	f.evaluate()
	if f.count("SELECT count(*) FROM alert_events") != 0 {
		t.Fatal("alert fired before threshold")
	}
	f.now = f.now.Add(time.Second)
	f.exec("UPDATE users SET traffic_up=80")
	f.evaluate()
	f.evaluate()
	if f.events(userExpiring) != 1 || f.events(userQuota80) != 1 || f.events(userQuota100) != 0 {
		t.Fatal("threshold/expiry events incorrect")
	}
	if _, err := f.service.SaveConfig(context.Background(), ConfigUpdate{Events: map[string]*bool{userQuota100: pointer(false)}}); err != nil {
		t.Fatal(err)
	}
	f.exec("UPDATE users SET traffic_up=100")
	f.evaluate()
	if f.events(userQuota100) != 0 {
		t.Fatal("disabled threshold still emitted")
	}
	if _, err := f.service.SaveConfig(context.Background(), ConfigUpdate{Events: map[string]*bool{userQuota100: pointer(true)}}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(72 * time.Hour)
	f.restart()
	f.evaluate()
	f.evaluate()
	if f.events(userExpired) != 1 || f.events(userQuota100) != 1 {
		t.Fatal("expiry/100% missed or repeated")
	}
	f.exec("UPDATE users SET traffic_epoch=traffic_epoch+1") // reset then consume, same expiration
	f.evaluate()
	if f.events(userQuota80) != 2 || f.events(userQuota100) != 2 || f.events(userExpired) != 1 {
		t.Fatal("new quota epoch or same expiry mishandled")
	}
	f.exec("UPDATE users SET alert_identity=102") // a distinct user reusing the same ID and expiry
	f.evaluate()
	if f.events(userExpired) != 2 {
		t.Fatal("a recreated user was deduplicated against the old identity")
	}
	if f.count("SELECT count(*) FROM alert_events") != f.count("SELECT count(*) FROM alert_outbox") {
		t.Fatal("an enabled channel did not receive exactly one row per event")
	}
	if thresholdReached(0, 1, 80) || !thresholdReached(1, 1, 80) || !thresholdReached(math.MaxInt64, math.MaxInt64, 100) {
		t.Fatal("threshold rounding or overflow")
	}
}

func TestServerThresholdsAndHealthIncidents(t *testing.T) {
	ctx := context.Background()
	running := true
	cert := acme.Status{Mode: "acme", Warning: true, NotAfter: time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC).Unix()}
	f := newFixture(t, Options{
		Network: &Network{}, CoreRunning: func(context.Context) bool { return running },
		Certificate: func(context.Context) (acme.Status, error) { return cert, nil },
	})
	if _, err := f.service.SaveConfig(ctx, ConfigUpdate{QuotaGB: pointer(int64(1))}); err != nil {
		t.Fatal(err)
	}
	sample := func(sent int64) {
		f.exec("INSERT INTO server_traffic_samples VALUES(1,?,'eth0',0,?)", f.now.UnixNano(), sent)
		f.evaluate()
		f.now = f.now.Add(time.Minute)
	}
	sample(800_000_000)
	if f.events(serverQuota80) != 1 || f.events(serverQuota90) != 0 || f.events(certWarning) != 1 {
		t.Fatal("missing initial server events")
	}
	running = false
	sample(100_000_000)
	if f.events(coreDown) != 0 || f.events(serverQuota90) != 1 {
		t.Fatal("single fail or 90% threshold handled incorrectly")
	}
	f.restart()
	sample(100_000_000)
	f.evaluate()
	if f.events(coreDown) != 1 || f.events(serverQuota100) != 1 || f.events(certWarning) != 1 {
		t.Fatal("missing or repeated incident")
	}
	running = true
	f.evaluate()
	f.evaluate()
	if f.events(coreRecovered) != 1 {
		t.Fatal("recovery emitted once per outage")
	}
	if _, err := f.service.SaveConfig(ctx, ConfigUpdate{Events: map[string]*bool{coreDown: pointer(false)}}); err != nil {
		t.Fatal(err)
	}
	running = false
	f.evaluate()
	f.evaluate()
	running = true
	f.evaluate()
	if f.events(coreDown) != 1 || f.events(coreRecovered) != 1 {
		t.Fatal("recovery sent for an unreported outage")
	}
	f.now = time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC)
	sample(1_000_000_000)
	if f.events(serverQuota80) != 2 || f.events(serverQuota90) != 2 || f.events(serverQuota100) != 2 {
		t.Fatal("thresholds did not reopen next billing period")
	}
}

type transactionApplier struct {
	db   *sql.DB
	fail bool
}

func (a *transactionApplier) Apply(ctx context.Context, mutate singbox.Mutation) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := mutate(ctx, tx); err != nil {
		return err
	}
	if a.fail {
		return errors.New("fake restart failure")
	}
	if err := db.SetSetting(ctx, tx, db.AppliedUsers, []int64{}); err != nil {
		return err
	}
	return tx.Commit()
}

func TestSchedulerEventPersistsOnlyOnSuccessfulApply(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.enableBark()
	f.user(100, 0)
	if err := db.SetSetting(ctx, f.writer, db.AppliedUsers, []int64{1}); err != nil {
		t.Fatal(err)
	}
	applier := &transactionApplier{db: f.writer, fail: true}
	scheduler := traffic.NewScheduler(traffic.NewCollector(f.writer, f.reader, nil), applier, f.opts.Now, nil)
	scheduler.OnStopped = f.service.UserStopped
	if err := scheduler.Check(ctx); err == nil {
		t.Fatal("fake apply should fail")
	}
	if f.count("SELECT count(*) FROM alert_events") != 0 || f.count("SELECT count(*) FROM alert_outbox") != 0 {
		t.Fatal("failed apply persisted alerts")
	}
	applier.fail = false
	f.now = f.now.Add(time.Minute)
	if err := scheduler.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if f.events(userStopped) != 1 || f.events(userQuota100) != 1 {
		t.Fatal("successful stop did not record events")
	}
	job, err := f.service.claim(ctx, Bark, f.now)
	if err != nil || job.id == 0 {
		t.Fatalf("claim: %v", err)
	}
	if !strings.Contains(job.message.Body, "alice") || !strings.Contains(job.message.Body, "node.example.com") ||
		!strings.Contains(job.message.Body, "100 / 100") || !strings.Contains(job.message.Body, "UTC") {
		t.Fatal("message missing alert context")
	}
	for _, forbidden := range []string{"private-token", "private-uuid", "private-password", "bark-secret-value", "https://"} {
		if strings.Contains(job.message.Body, forbidden) {
			t.Fatal("outbound message revealed private data")
		}
	}
	out, err := f.service.Overview(ctx)
	if err != nil || out.UnreadCount != 3 || len(out.Recent) != 3 {
		t.Fatalf("fallback unavailable: %+v, %v", out, err)
	}
	for range 2 {
		ok, err := f.service.ReadEvent(ctx, out.Unread[0].Key)
		if !ok || err != nil {
			t.Fatalf("mark read: %v, %v", ok, err)
		}
	}
	if f.count("SELECT count(*) FROM alert_events WHERE read_at=0") != 2 {
		t.Fatal("alert was not marked read")
	}
}
