package alerts

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/host"
)

type fakeNetwork struct {
	host.Native
	ipv4, ipv6 string
	rx, tx     uint64
}

func (n *fakeNetwork) ReadFile(path string) ([]byte, error) {
	if path != "/proc/net/dev" {
		return nil, fmt.Errorf("unexpected read %s", path)
	}
	return []byte(fmt.Sprintf(`Inter-|   Receive                                                |  Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
   lo: 1 0 0 0 0 0 0 0 2 0 0 0 0 0 0 0
 eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0
 eth1: 99999 0 0 0 0 0 0 0 99999 0 0 0 0 0 0 0
`, n.rx, n.tx)), nil
}

func (n *fakeNetwork) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch name + " " + fmt.Sprint(args) {
	case "ip [-4 route show default]":
		return []byte(n.ipv4), nil
	case "ip [-6 route show default]":
		return []byte(n.ipv6), nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func TestBillingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		now, start, end string
		day             int
	}{
		{"2028-02-28T07:59:59+08:00", "2028-01-28", "2028-02-28", 28},
		{"2028-02-28T08:00:00+08:00", "2028-02-28", "2028-03-28", 28},
		{"2028-02-29T23:59:59Z", "2028-02-01", "2028-03-01", 1},
		{"2029-02-28T23:59:59Z", "2029-02-01", "2029-03-01", 1},
		{"2026-01-03T00:00:00Z", "2025-12-05", "2026-01-05", 5},
	} {
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		start, end := billingPeriod(now, tc.day)
		if start.Format(time.DateOnly) != tc.start || end.Format(time.DateOnly) != tc.end || start.Hour() != 0 || end.Location() != time.UTC {
			t.Errorf("cycle %s day %d = %s -> %s", tc.now, tc.day, start, end)
		}
	}
}

func TestNetSamplesResetRestartAndBillingModes(t *testing.T) {
	ctx := context.Background()
	driver := &fakeNetwork{ipv4: "default via 192.0.2.1 dev eth1 metric 100\ndefault via 192.0.2.1 dev eth0 metric 10", rx: 100, tx: 150}
	network := &Network{Files: driver, Runner: driver}
	f := newFixture(t, Options{Network: network})
	now := time.Date(2028, 2, 29, 23, 57, 0, 0, time.UTC)
	f.now = now
	sample := func(rx, tx uint64) {
		t.Helper()
		driver.rx, driver.tx = rx, tx
		f.now = now
		if err := f.service.sampleTraffic(ctx, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	sample(100, 150) // baseline
	sample(500, 750) // +400/+600
	sample(70, 850)  // rx reset +70, tx +100
	sample(90, 1000) // first March sample +20/+150
	for mode, want := range map[string]int64{"out": 150, "in": 20, "both": 170, "max": 150} {
		if _, err := f.service.SaveConfig(ctx, ConfigUpdate{Mode: pointer(mode), QuotaGB: pointer(int64(1))}); err != nil {
			t.Fatal(err)
		}
		view, err := f.service.Overview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := view.Billing
		if got.Used != want || got.RX != 20 || got.TX != 150 || got.Interface != "eth0" || got.Quota != bytesPerGB || got.StartsAt != time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC).Unix() {
			t.Fatalf("%s: %+v (want %d)", mode, got, want)
		}
	}
	if f.count("SELECT count(*) FROM server_traffic_samples WHERE server_id=1 AND rx=70 AND tx=100") != 1 {
		t.Fatal("counter reset was not recorded")
	}
	f.restart() // kernel counters rose while panel was offline
	sample(900, 1200)
	sample(930, 1250)
	if f.count("SELECT count(*) FROM server_traffic_samples WHERE rx=0 AND tx=0") != 2 ||
		f.count("SELECT count(*) FROM server_traffic_samples WHERE rx=30 AND tx=50") != 1 {
		t.Fatal("restart did not establish a fresh baseline")
	}
	if _, err := f.service.SaveConfig(ctx, ConfigUpdate{Interface: pointer("eth1")}); err != nil {
		t.Fatal(err)
	}
	sample(1000, 1500)
	view, err := f.service.Overview(ctx)
	if err != nil || view.Billing.Interface != "eth1" || view.Billing.RX != 50 || view.Billing.TX != 200 {
		t.Fatalf("interface change: %+v, %v", view.Billing, err)
	}
	settings, err := f.service.Settings(ctx)
	if err != nil || !reflect.DeepEqual(settings.Interfaces, []string{"eth0", "eth1"}) {
		t.Fatalf("interfaces = %v, %v", settings.Interfaces, err)
	}
}

func TestNetSampleRetriesWithoutLosingDelta(t *testing.T) {
	ctx := context.Background()
	driver := &fakeNetwork{ipv6: "default via fe80::1 dev eth0 metric 1024", rx: 100, tx: 150}
	f := newFixture(t, Options{Network: &Network{Files: driver, Runner: driver}})
	now := f.now
	if err := f.service.sampleTraffic(ctx, now); err != nil {
		t.Fatal(err)
	}
	f.exec(`CREATE TRIGGER block_sample BEFORE INSERT ON server_traffic_samples BEGIN SELECT raise(ABORT, 'sample failed'); END;`)
	driver.rx, driver.tx = 140, 180
	if err := f.service.sampleTraffic(ctx, now.Add(time.Minute)); err == nil {
		t.Fatal("sample should fail with a pending delta")
	}
	f.exec("DROP TRIGGER block_sample")
	driver.rx, driver.tx = 160, 210
	if err := f.service.sampleTraffic(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.count("SELECT count(*) FROM server_traffic_samples WHERE rx=40 AND tx=30") != 1 ||
		f.count("SELECT count(*) FROM server_traffic_samples WHERE rx=20 AND tx=30") != 1 {
		t.Fatal("sample failure lost or double-counted bytes")
	}
}
