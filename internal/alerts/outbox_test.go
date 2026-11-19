package alerts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *fixture) queue(channel string) int64 {
	f.t.Helper()
	id, err := f.service.QueueTest(context.Background(), channel)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) claim(channel string, want bool) claimedDelivery {
	f.t.Helper()
	job, err := f.service.claim(context.Background(), channel, f.now)
	if err != nil || (job.id != 0) != want {
		f.t.Fatalf("claim returned attempt %d (%v), want delivery=%t", job.id, err, want)
	}
	return job
}

func (f *fixture) finish(job claimedDelivery, err error) {
	f.t.Helper()
	if err := f.service.finish(context.Background(), job, f.now, err); err != nil {
		f.t.Fatal(err)
	}
}

func TestNotificationRetriesSurviveRestartAndExpire(t *testing.T) {
	f := newFixture(t, Options{})
	f.enableBark()
	f.user(80, 0)
	f.evaluate()
	start := f.now
	job := f.claim(Bark, true)
	f.claim(Bark, false) // leased while the request is in flight
	f.finish(job, errors.New("provider returned a private URL and token"))
	f.restart()
	f.now = start.Add(29 * time.Second)
	f.claim(Bark, false)
	f.now = start.Add(30 * time.Second)
	for _, delay := range []time.Duration{60, 120, 240, 480, 960, 1920, 3600, 3600} {
		job = f.claim(Bark, true)
		f.finish(job, errTimeout)
		f.now = f.now.Add((delay - 1) * time.Second)
		f.claim(Bark, false)
		f.now = f.now.Add(time.Second)
	}
	f.now = start.Add(24 * time.Hour)
	f.claim(Bark, false)
	out, err := f.service.Overview(context.Background())
	if err != nil || out.UnreadCount != 1 || len(out.Recent) != 1 || out.Recent[0].Status != "failed" || out.Recent[0].Error != "timeout" || out.Recent[0].Attempts != 9 {
		t.Fatalf("fallback and failure summary: %+v, %v", out, err)
	}
	f.evaluate()
	if f.count("SELECT count(*) FROM alert_outbox") != 1 {
		t.Fatal("terminal failure caused duplicate alert")
	}
}

func TestNotificationRateAndOverflowAcrossRestart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.enableBark()
	if _, err := f.service.SaveChannel(ctx, Telegram, ChannelUpdate{Credential: pointer("123:abcDEF"), ChatID: pointer("-1234")}); err != nil {
		t.Fatal(err)
	}

	for range 29 {
		f.queue(Bark)
		f.finish(f.claim(Bark, true), nil)
	}
	for range 4 {
		f.queue(Bark)
	}
	job := f.claim(Bark, true)
	if job.message.Title != "boxpanel 通知摘要" || !strings.Contains(job.message.Body, "共 4 条") {
		t.Fatal("last slot did not combine a burst")
	}
	f.finish(job, nil)
	for range 3 {
		f.queue(Bark)
	}
	f.claim(Bark, false)
	if f.count("SELECT count(*) FROM alert_attempts WHERE channel=?", Bark) != 30 {
		t.Fatal("exceeded hourly budget")
	}
	f.restart()
	f.now = f.now.Add(59 * time.Minute)
	f.claim(Bark, false)
	f.queue(Telegram)
	f.finish(f.claim(Telegram, true), nil)
	f.now = f.now.Add(time.Minute)
	job = f.claim(Bark, true)
	if !strings.Contains(job.message.Body, "共 3 条") {
		t.Fatal("overflow did not survive restart as a summary")
	}
	f.finish(job, nil)
	f.claim(Bark, false)
	if f.count("SELECT count(*) FROM alert_outbox WHERE status='sent' AND channel=?", Bark) != 36 {
		t.Fatal("not all notifications were delivered")
	}
}

func TestOutboxConcurrentClaimsAndExpiredLease(t *testing.T) {
	f := newFixture(t, Options{})
	f.enableBark()
	f.queue(Bark)
	other := New(f.writer, f.reader, Options{})
	var wg sync.WaitGroup
	results := make(chan claimedDelivery, 2)
	for _, service := range []*Service{f.service, other} {
		wg.Go(func() {
			job, err := service.claim(context.Background(), Bark, f.now)
			if err != nil {
				t.Error(err)
			}
			results <- job
		})
	}
	wg.Wait()
	first, second := <-results, <-results
	if (first.id == 0) == (second.id == 0) {
		t.Fatal("expected just one worker to claim a delivery")
	}
	f.now = f.now.Add(claimLease)
	f.restart()
	f.finish(f.claim(Bark, true), nil)
	if f.count("SELECT count(*) FROM alert_outbox WHERE status='sent' AND attempts=2") != 1 {
		t.Fatal("expired claim did not recover")
	}
}

func TestNotificationChannelMaskingAndDisable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.enableBark()
	f.user(80, 0)
	f.evaluate()
	f.queue(Bark)
	view, err := f.service.SaveChannel(ctx, Bark, ChannelUpdate{Enabled: pointer(false)})
	if err != nil || view.CredentialMask != "****alue" || view.Enabled {
		t.Fatalf("masked configuration: %+v, %v", view, err)
	}
	if f.count("SELECT count(*) FROM alert_outbox WHERE status='failed' AND error='channel_disabled'") != 1 {
		t.Fatal("disabling should cancel pending automatic deliveries")
	}
	f.finish(f.claim(Bark, true), nil) // saved credentials can still be tested
	if _, err := f.service.SaveChannel(ctx, Bark, ChannelUpdate{Credential: pointer("")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.QueueTest(ctx, Bark); !errors.Is(err, ValidationError("credentials_required")) {
		t.Fatal("test queued without credentials", err)
	}
}
