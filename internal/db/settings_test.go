package db

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func roundTripSetting[T any](t *testing.T, conn *sql.DB, key SettingKey[T], value T) {
	t.Helper()
	ctx := context.Background()
	if err := SetSetting(ctx, conn, key, value); err != nil {
		t.Fatal(err)
	}
	got, err := GetSetting(ctx, conn, key)
	if err != nil || !reflect.DeepEqual(got, value) {
		t.Fatalf("setting %s round trip: got=%#v want=%#v err=%v", key.name, got, value, err)
	}
}

func TestSettingsTypedRoundTrip(t *testing.T) {
	conn := openTestDB(t)
	roundTripSetting(t, conn, WebPort, 2053)
	roundTripSetting(t, conn, WebPath, "") // Empty dev path is a stored value.
	roundTripSetting(t, conn, SubPath, "k3m9x2p7q1ab")
	roundTripSetting(t, conn, PublicHost, "example.com")
	roundTripSetting(t, conn, PanelTLSMode, "self_signed")
	roundTripSetting(t, conn, SetupCompleted, false)
	roundTripSetting(t, conn, SetupStep, "welcome")
	roundTripSetting(t, conn, SingboxVersion, "test-version")
	roundTripSetting(t, conn, BlockBittorrent, true)
	roundTripSetting(t, conn, PanelName, "面板 \"name\"\n'; DROP TABLE settings; --")
	rules := RulesSettings{
		Preset: "bypass_cn", BlockAds: true, CustomDirect: []string{"example.cn"},
		CustomProxy: []string{"example.com"}, Source: "metacubex", Mirror: "",
		UpdateInterval: "1d", DNS: DNSSettings{Remote: "https://1.1.1.1/dns-query", Local: "https://223.5.5.5/dns-query", FakeIP: false},
		IPv6: false,
	}
	roundTripSetting(t, conn, Rules, rules)
	roundTripSetting(t, conn, WebPort, 443)
	roundTripSetting(t, conn, SetupCompleted, true)
	var raw string
	if err := conn.QueryRow("SELECT value FROM settings WHERE key = 'rules'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want := `{"preset":"bypass_cn","block_ads":true,"custom_direct":["example.cn"],"custom_proxy":["example.com"],"source":"metacubex","mirror":"","update_interval":"1d","dns":{"remote":"https://1.1.1.1/dns-query","local":"https://223.5.5.5/dns-query","fakeip":false},"ipv6":false}`
	if raw != want {
		t.Fatalf("rules JSON = %s, want %s", raw, want)
	}
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM settings").Scan(&count); err != nil || count != 11 {
		t.Fatalf("upsert created extra rows: count=%d err=%v", count, err)
	}
}

func TestSettingsMissingInvalidAndCancelled(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t)
	if got, err := GetSetting(ctx, conn, WebPort); got != 0 || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing setting: value=%d err=%v", got, err)
	}
	for _, raw := range []string{`"secret-value"`, `true`, `12.5`, `{"port":2053}`, `{`, `2053 443`, ` null `} {
		mustExec(t, conn, `INSERT INTO settings VALUES ('web_port', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, raw)
		got, err := GetSetting(ctx, conn, WebPort)
		if got != 0 || err == nil || errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "web_port") || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid setting should return a contextual error and zero value: got=%d err=%v", got, err)
		}
	}
	mustExec(t, conn, `INSERT INTO settings VALUES ('rules', '{"preset":"global","ipv6":"wrong-type"}')`)
	if got, err := GetSetting(ctx, conn, Rules); err == nil || !reflect.DeepEqual(got, RulesSettings{}) {
		t.Fatalf("partial decode should not return partial settings: got=%#v err=%v", got, err)
	}
	if _, err := GetSetting(ctx, conn, SettingKey[string]{}); err == nil {
		t.Fatal("accepted empty key on read")
	}
	if err := SetSetting(ctx, conn, SettingKey[string]{}, "value"); err == nil {
		t.Fatal("accepted empty key on write")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := GetSetting(cancelled, conn, WebPort); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	if err := SetSetting(cancelled, conn, WebPort, 443); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
}

func TestSettingsTransactions(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t)
	roundTripSetting(t, conn, WebPort, 2053)
	for _, commit := range []bool{false, true} {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := SetSetting(ctx, tx, WebPort, 443); err != nil {
			t.Fatal(err)
		}
		if got, err := GetSetting(ctx, tx, WebPort); err != nil || got != 443 {
			t.Fatalf("transaction read: value=%d err=%v", got, err)
		}
		want := 2053
		if commit {
			err = tx.Commit()
			want = 443
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, err := GetSetting(ctx, conn, WebPort); err != nil || got != want {
			t.Fatalf("after commit=%t: value=%d want=%d err=%v", commit, got, want, err)
		}
	}
}
